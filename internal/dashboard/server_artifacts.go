package dashboard

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/agentinfra"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// GetDiff returns the unified git diff for an AgentRun.
// Strategy 1: terminal task with S3 diff URL → fetch from S3 (cached).
// Strategy 2: running task with sandbox → exec git diff in pod.
// Strategy 3: unavailable.
func (s *Server) GetDiff(ctx context.Context, req *platform.GetDiffRequest) (*platform.GetDiffResponse, error) {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return nil, err
	}
	return s.buildDiff(ctx, req)
}

// buildDiff builds the diff response without authorization (callers must
// have checked access). Builds are coalesced across concurrent callers —
// every open diff view of the same run plus unary refreshes — for
// probeDiffTTL: the underlying work is a pod exec (live runs) or an S3 fetch
// (terminal runs) and must not scale with the number of open pages. The
// returned response is shared; callers must treat it as read-only.
func (s *Server) buildDiff(ctx context.Context, req *platform.GetDiffRequest) (*platform.GetDiffResponse, error) {
	key := "diff|" + req.Namespace + "/" + req.Name + "|" + req.ResourceType + "|" + req.RepoPath
	return probeCacheDo(ctx, &s.probes, key, probeDiffTTL, func(ctx context.Context) (*platform.GetDiffResponse, error) {
		return s.buildDiffUncached(ctx, req)
	})
}

func (s *Server) buildDiffUncached(ctx context.Context, req *platform.GetDiffRequest) (*platform.GetDiffResponse, error) {
	sandboxName, namespace, baseBranch, diffURL, isTerminal, err := s.resolveSandbox(ctx, req.Namespace, req.Name, req.ResourceType)
	if err != nil {
		return nil, err
	}
	// Validate the requested repository up front so bad input surfaces as
	// InvalidArgument instead of an "unavailable" diff.
	_, primaryRepo, err := resolveWorkspaceRepoPath(req.RepoPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	sandboxMissing := false

	// Strategy 1: terminal task — fetch from S3. The final diff artifact only
	// covers the primary repository, so extra repos never use it.
	if primaryRepo && isTerminal && diffURL != "" && s.s3Diff != nil {
		diff, err := s.s3Diff.FetchDiff(ctx, diffURL)
		if err != nil {
			log.Printf("WARN: failed to fetch diff from S3 (%s): %v", diffURL, err)
		} else {
			truncated := len(diff) > maxDiffSize
			if truncated {
				diff = diff[:maxDiffSize]
			}
			resp := &platform.GetDiffResponse{Diff: diff, IsComplete: true, Truncated: truncated, Source: "s3"}
			// The final diff artifact predates untracked-file metadata. While the
			// terminal sandbox still exists, enrich the final response with paths
			// only so users can continue to lazy-load selected files.
			if sandboxName != "" && s.clientset != nil {
				newFiles, newFilesTruncated, listErr := execListNewFiles(ctx, s.clientset, s.restConfig, sandboxName, namespace, req.RepoPath)
				if listErr == nil {
					resp.NewFiles = newFiles
					resp.NewFilesTruncated = newFilesTruncated
				} else if !isTransientPodStartupExecError(listErr) && !isPodNotFoundExecError(listErr) {
					log.Printf("WARN: failed to list new files in terminal pod %s/%s: %v", namespace, sandboxName, listErr)
				}
			}
			return resp, nil
		}
	}

	// Strategy 2: live fallback — exec in pod when a sandbox is still available.
	if sandboxName != "" && s.clientset != nil {
		diff, truncated, err := execGetDiff(ctx, s.clientset, s.restConfig, sandboxName, namespace, baseBranch, req.RepoPath)
		if err != nil {
			if isPodNotFoundExecError(err) {
				sandboxMissing = true
			} else if !isTransientPodStartupExecError(err) {
				log.Printf("WARN: failed to exec git diff in pod %s/%s: %v", namespace, sandboxName, err)
			}
		} else {
			newFiles, newFilesTruncated, listErr := execListNewFiles(ctx, s.clientset, s.restConfig, sandboxName, namespace, req.RepoPath)
			if listErr != nil && !isTransientPodStartupExecError(listErr) && !isPodNotFoundExecError(listErr) {
				log.Printf("WARN: failed to list new files in pod %s/%s: %v", namespace, sandboxName, listErr)
			}
			return &platform.GetDiffResponse{
				Diff: diff, IsComplete: false, Truncated: truncated, Source: "pod",
				NewFiles: newFiles, NewFilesTruncated: newFilesTruncated,
			}, nil
		}
	}

	return &platform.GetDiffResponse{
		Source:     "unavailable",
		IsComplete: isTerminal && (sandboxName == "" || s.clientset == nil || sandboxMissing),
	}, nil
}

// ListFiles lists files/directories under a path in the sandbox pod.
// Only available for running tasks — returns FailedPrecondition for terminal tasks.
func (s *Server) ListFiles(ctx context.Context, req *platform.ListFilesRequest) (*platform.ListFilesResponse, error) {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return nil, err
	}
	sandboxName, namespace, _, _, isTerminal, err := s.resolveSandbox(ctx, req.Namespace, req.Name, req.ResourceType)
	if err != nil {
		return nil, err
	}

	if isTerminal {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("ListFiles is only available for running tasks"))
	}
	if sandboxName == "" || s.clientset == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("sandbox not available"))
	}

	entries, err := execListFiles(ctx, s.clientset, s.restConfig, sandboxName, namespace, req.Path)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing files: %w", err))
	}
	return &platform.ListFilesResponse{Files: entries}, nil
}

// ListWorkspaceFiles returns a flat, recursive list of workspace file paths for
// fuzzy-finding (e.g. the chat composer "@" file picker). Uses a filesystem walk
// rather than git so it works across multiple repos.
// Only available for running tasks — returns FailedPrecondition for terminal tasks.
func (s *Server) ListWorkspaceFiles(ctx context.Context, req *platform.ListWorkspaceFilesRequest) (*platform.ListWorkspaceFilesResponse, error) {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return nil, err
	}
	sandboxName, namespace, _, _, isTerminal, err := s.resolveSandbox(ctx, req.Namespace, req.Name, req.ResourceType)
	if err != nil {
		return nil, err
	}

	if isTerminal {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("ListWorkspaceFiles is only available for running tasks"))
	}
	if sandboxName == "" || s.clientset == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("sandbox not available"))
	}

	paths, truncated, err := execListWorkspaceFiles(ctx, s.clientset, s.restConfig, sandboxName, namespace, int(req.Limit))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing workspace files: %w", err))
	}
	return &platform.ListWorkspaceFilesResponse{Paths: paths, Truncated: truncated}, nil
}

// ListRepositories lists the git repositories present in a running run's sandbox
// (the original repo plus any cloned at runtime).
// Only available for running tasks — the sandbox is gone for terminal tasks.
func (s *Server) ListRepositories(ctx context.Context, req *platform.ListRepositoriesRequest) (*platform.ListRepositoriesResponse, error) {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return nil, err
	}
	sandboxName, namespace, _, _, isTerminal, err := s.resolveSandbox(ctx, req.Namespace, req.Name, req.ResourceType)
	if err != nil {
		return nil, err
	}
	if isTerminal {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("ListRepositories is only available for running tasks"))
	}
	if sandboxName == "" || s.clientset == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("sandbox not available"))
	}

	repos, err := execListRepos(ctx, s.clientset, s.restConfig, sandboxName, namespace)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing repositories: %w", err))
	}
	return &platform.ListRepositoriesResponse{Repositories: repos}, nil
}

// CloneRepository clones an additional git repository into a running run's
// sandbox using the pod's existing git credentials. Requires collaborator access.
func (s *Server) CloneRepository(ctx context.Context, req *platform.CloneRepositoryRequest) (*platform.CloneRepositoryResponse, error) {
	if err := s.requireAgentRunCollaborator(ctx, req.Namespace, req.Name, "clone a repository into"); err != nil {
		return nil, err
	}

	repoURL := strings.TrimSpace(req.RepoUrl)
	if err := validateCloneURL(repoURL); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	baseBranch := strings.TrimSpace(req.BaseBranch)
	if strings.HasPrefix(baseBranch, "-") {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid base_branch %q", baseBranch))
	}
	destName, err := deriveRepoDirName(repoURL)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if destName == filepath.Base(repoDir) {
		return nil, connect.NewError(connect.CodeAlreadyExists,
			fmt.Errorf("%q is reserved for the run's primary repository", destName))
	}

	sandboxName, namespace, _, _, isTerminal, err := s.resolveSandbox(ctx, req.Namespace, req.Name, req.ResourceType)
	if err != nil {
		return nil, err
	}
	if isTerminal {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("CloneRepository is only available for running tasks"))
	}
	if sandboxName == "" || s.clientset == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("sandbox not available"))
	}

	repo, err := execCloneRepo(ctx, s.clientset, s.restConfig, sandboxName, namespace, repoURL, baseBranch, destName)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "already exists and is not an empty directory") {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("repository %q is already cloned", destName))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cloning repository: %w", err))
	}
	return &platform.CloneRepositoryResponse{Repository: repo}, nil
}

// validateCloneURL ensures the URL is a recognised git transport so it cannot be
// mistaken for a git CLI flag and only references a remote.
func validateCloneURL(repoURL string) error {
	if repoURL == "" {
		return fmt.Errorf("repo_url is required")
	}
	switch {
	case strings.HasPrefix(repoURL, "https://"),
		strings.HasPrefix(repoURL, "http://"),
		strings.HasPrefix(repoURL, "git@"),
		strings.HasPrefix(repoURL, "ssh://"):
		return nil
	default:
		return fmt.Errorf("repo_url must be an http(s), ssh, or git URL")
	}
}

// deriveRepoDirName extracts a safe single-segment directory name from a git URL
// (e.g. https://github.com/owner/my-repo.git -> "my-repo").
func deriveRepoDirName(repoURL string) (string, error) {
	return agentinfra.DeriveRepoDirName(repoURL)
}

// ReadFile reads file content from the sandbox pod.
// Only available for running tasks — returns FailedPrecondition for terminal tasks.
func (s *Server) ReadFile(ctx context.Context, req *platform.ReadFileRequest) (*platform.ReadFileResponse, error) {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return nil, err
	}
	sandboxName, namespace, _, _, isTerminal, err := s.resolveSandbox(ctx, req.Namespace, req.Name, req.ResourceType)
	if err != nil {
		return nil, err
	}

	// A terminal run can still have a live sandbox briefly; allow lazy reads
	// while that sandbox exists. Once it is gone, content is unavailable.
	if isTerminal && sandboxName == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("file content is unavailable after the sandbox is removed"))
	}
	if sandboxName == "" || s.clientset == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("sandbox not available"))
	}

	content, truncated, err := execReadFile(ctx, s.clientset, s.restConfig, sandboxName, namespace, req.RepoPath, req.Path, int(req.MaxLines))
	if err != nil {
		if _, _, pathErr := resolveWorkspaceRepoPath(req.RepoPath); pathErr != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, pathErr)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reading file: %w", err))
	}
	return &platform.ReadFileResponse{Content: content, Truncated: truncated}, nil
}

// resolveSandbox looks up the sandbox name, namespace, base branch, diff URL, and terminal
// state for an AgentRun-backed public run surface.
func (s *Server) resolveSandbox(ctx context.Context, namespace, name, resourceType string) (sandboxName, ns, baseBranch, diffURL string, isTerminal bool, err error) {
	ns = namespace
	resourceType = strings.TrimSpace(resourceType)
	if resourceType == "" {
		resourceType = "AgentRun"
	}
	switch resourceType {
	case "AgentRun":
		run := &platformv1alpha1.AgentRun{}
		if err = s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, run); err != nil {
			err = mapK8sError(fmt.Sprintf("get AgentRun %s/%s", namespace, name), err)
			return
		}
		sandboxName, baseBranch, diffURL, isTerminal, err = s.resolveAgentRunSandbox(ctx, run)
		if err != nil {
			return
		}
	default:
		err = connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("unsupported resource type %q: expected AgentRun", resourceType))
		return
	}
	return
}

func (s *Server) resolveAgentRunSandbox(ctx context.Context, run *platformv1alpha1.AgentRun) (sandboxName, baseBranch, diffURL string, isTerminal bool, err error) {
	if run == nil {
		return "", "", "", false, nil
	}

	if sandboxName == "" && run.Status.Sandbox != nil && run.Status.Sandbox.SandboxRef != nil {
		sandboxName = run.Status.Sandbox.SandboxRef.Name
	}
	if baseBranch == "" {
		baseBranch = run.Spec.Repository.BaseBranch
	}
	if diffURL == "" && run.Status.Artifacts != nil {
		diffURL = run.Status.Artifacts.DiffURL
	}
	if !isTerminal {
		isTerminal = isTerminalAgentRunPhase(run.Status.Phase)
	}
	return sandboxName, baseBranch, diffURL, isTerminal, nil
}

// GetAgentTrace returns the OTel trace for an agent run, fetched from Jaeger.
func (s *Server) GetAgentTrace(ctx context.Context, req *platform.GetAgentTraceRequest) (*platform.GetAgentTraceResponse, error) {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return nil, err
	}
	if s.jaeger == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("Jaeger not configured (set JAEGER_QUERY_URL or OTEL_EXPORTER_OTLP_ENDPOINT)"))
	}
	return s.buildAgentTrace(ctx, req)
}

// buildAgentTrace builds the trace response without authorization (callers
// must have checked access). Builds are coalesced across concurrent callers
// for probeTraceTTL: each build is a full Jaeger HTTP fetch plus JSON decode
// and must not scale with the number of open trace views. The returned
// response is shared and fully formed inside the build (including
// IsComplete); callers must treat it as read-only.
func (s *Server) buildAgentTrace(ctx context.Context, req *platform.GetAgentTraceRequest) (*platform.GetAgentTraceResponse, error) {
	key := "trace|" + req.Namespace + "/" + req.Name
	return probeCacheDo(ctx, &s.probes, key, probeTraceTTL, func(ctx context.Context) (*platform.GetAgentTraceResponse, error) {
		return s.buildAgentTraceUncached(ctx, req)
	})
}

func (s *Server) buildAgentTraceUncached(ctx context.Context, req *platform.GetAgentTraceRequest) (*platform.GetAgentTraceResponse, error) {
	// Look up the AgentRun to get the trace ID.
	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, run); err != nil {
		return nil, mapK8sError("GetAgentTrace", err)
	}

	// Mark trace complete when the agent run has reached a terminal phase.
	// Compute this before the empty-trace-ID return so watchers polling
	// until IsComplete terminate for runs that never published a trace ID.
	isTerminal := run.Status.Phase == platformv1alpha1.AgentRunPhaseSucceeded ||
		run.Status.Phase == platformv1alpha1.AgentRunPhaseFailed ||
		run.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled

	traceID := ""
	if run.Status.Artifacts != nil {
		traceID = run.Status.Artifacts.TraceID
	}
	if traceID == "" {
		return &platform.GetAgentTraceResponse{IsComplete: isTerminal}, nil
	}

	resp, err := s.jaeger.FetchTrace(traceID)
	if err != nil {
		log.Printf("GetAgentTrace: jaeger fetch failed for %s: %v", traceID, err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("jaeger fetch: %w", err))
	}
	resp.IsComplete = isTerminal

	return resp, nil
}
