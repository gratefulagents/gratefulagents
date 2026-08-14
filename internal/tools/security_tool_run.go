package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/security"
	"github.com/gratefulagents/gratefulagents/internal/securitytoolpacks"
	"github.com/gratefulagents/gratefulagents/internal/securitytoolrun"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

const (
	securityToolRunDefaultTimeout = 900
	securityToolRunMinTimeout     = 5
	securityToolRunMaxTimeout     = 1800
	securityToolRunPollInterval   = 2 * time.Second
	// maxStagedTargetBytes caps the workspace content one request may stage.
	maxStagedTargetBytes  int64 = 512 << 20
	stagedTargetMediaType       = "application/gzip"
)

var securityToolRunDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var securityToolRunLocatorDigestPattern = regexp.MustCompile(`@(sha256:[0-9a-f]{64})(?:$|/)`)

// securityToolDirectoryMediaTypes mirrors the directory media types
// securitytoolpacks.Registry.BuildInvocation requires inside the Job. Only a
// staged archive can produce them — the Job sets them when it extracts the
// archive — so these tools cannot run against an unstaged target.
var securityToolStagedMediaTypes = map[string]map[string]string{
	"aderyn": {
		"solidity_project": "application/vnd.gratefulagents.solidity-project.v1+directory",
	},
	"forge-security-tests": {
		"foundry_project": "application/vnd.gratefulagents.foundry-security-project.v1+directory",
	},
	"echidna": {
		"solidity_project": "application/vnd.gratefulagents.solidity-project.v1+directory",
	},
	"slither": {
		"solidity_project": "application/vnd.gratefulagents.solidity-project.v1+directory",
	},
	"halmos": {
		"foundry_project": "application/vnd.gratefulagents.foundry-security-project.v1+directory",
	},
	"go-fuzz-tests": {
		"go_fuzz_project": "application/vnd.gratefulagents.go-fuzz-project.v1+directory",
	},
	"cargo-fuzz": {
		"rust_fuzz_project": "application/vnd.gratefulagents.rust-fuzz-project.v1+directory",
	},
}

var securityToolRunLabelValuePattern = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)

// SecurityToolRunBlobStore is the object-storage surface run_security_tool
// needs: staged targets go up, result documents come down.
type SecurityToolRunBlobStore interface {
	Put(ctx context.Context, key string, content []byte, mediaType string) error
	Get(ctx context.Context, key string) ([]byte, error)
}

// SecurityToolRunDeps carries the platform handles run_security_tool needs.
// Blobs may be nil when the object store could not be built; BlobsErr then
// explains why in every result that would have needed it.
type SecurityToolRunDeps struct {
	Client       client.Client
	Blobs        SecurityToolRunBlobStore
	BlobsErr     error
	Namespace    string
	RunName      string
	RunUID       string
	WorkspaceDir string
}

// RegisterSecurityToolRunTool registers run_security_tool, the only path an
// agent has to a real scanner: it records a SecurityToolRun the platform
// executes in a hardened Job running the pinned security-tools image, then
// ingests the produced findings through the scan pipeline.
func RegisterSecurityToolRunTool(registry *Registry, state *securityScanState, deps SecurityToolRunDeps) {
	if registry == nil || state == nil || deps.Client == nil {
		return
	}
	if strings.TrimSpace(deps.Namespace) == "" || strings.TrimSpace(deps.RunName) == "" {
		return
	}
	packs, err := securitytoolrun.DefaultRegistry()
	registry.Register(&runSecurityToolTool{
		state:        state,
		deps:         deps,
		packs:        packs,
		packsErr:     err,
		pollInterval: securityToolRunPollInterval,
	})
}

type runSecurityToolTool struct {
	// restoredFuzzInputs records how many persisted corpus inputs were
	// restored into the staged target, so the summary can say whether a clean
	// campaign started cold or warm.
	restoredFuzzInputs int
	state              *securityScanState
	deps               SecurityToolRunDeps
	packs              *securitytoolpacks.Registry
	packsErr           error
	pollInterval       time.Duration
}

type runSecurityToolTarget struct {
	Type     string `json:"type"`
	Locator  string `json:"locator"`
	Revision string `json:"revision,omitempty"`
}

type runSecurityToolInput struct {
	Tool            string                `json:"tool"`
	Target          runSecurityToolTarget `json:"target"`
	Arguments       map[string]string     `json:"arguments,omitempty"`
	Scope           []string              `json:"scope,omitempty"`
	Seed            *int64                `json:"seed,omitempty"`
	SensitiveFields []string              `json:"sensitive_fields,omitempty"`
	TimeoutSeconds  int                   `json:"timeout_seconds,omitempty"`
}

func (t *runSecurityToolTool) Name() string { return "run_security_tool" }

func (t *runSecurityToolTool) Description() string {
	return fmt.Sprintf("Execute one registered, deterministic security tool against a target and ingest "+
		"its findings into this scan. The platform runs the tool in a dedicated, hardened "+
		"Kubernetes Job on a digest-pinned image with normal outbound network access; you cannot "+
		"choose the image, the command line, or any scanner flag — only the registered tool name, "+
		"its typed arguments, and the target. Workspace paths are staged as a content-addressed "+
		"archive (max %d MiB) so the execution is replayable. Findings are normalized, "+
		"secret-redacted, deduplicated by fingerprint and correlated exactly like "+
		"ingest_scanner_results. The call blocks until the run finishes or timeout_seconds "+
		"expires (default %d, max %d); a failed or timed-out run is never reported as a pass. "+
		"Available tools with the target types they accept: %s.",
		maxStagedTargetBytes>>20, securityToolRunDefaultTimeout,
		securityToolRunMaxTimeout, t.enabledToolSummary())
}

// enabledToolSummary lists the executable tools with their accepted target
// types so the model can pick a valid combination without guessing.
func (t *runSecurityToolTool) enabledToolSummary() string {
	if t.packs == nil {
		return "unavailable (tool registry could not be loaded)"
	}
	var entries []string
	for _, tool := range t.packs.Manifest().Tools {
		if !tool.Enabled {
			continue
		}
		entries = append(entries, fmt.Sprintf("%s [%s]", tool.Name, strings.Join(tool.TargetTypes, "|")))
	}
	slices.Sort(entries)
	return strings.Join(entries, ", ")
}

func (t *runSecurityToolTool) InputSchema() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"tool": {"type": "string", "description": "Registered tool name, e.g. nuclei or aderyn"},
			"target": {
				"type": "object",
				"additionalProperties": false,
				"required": ["type", "locator"],
				"properties": {
					"type": {"type": "string", "description": "Registry target type accepted by the tool, e.g. base_url, solidity_project, openapi"},
					"locator": {"type": "string", "description": "Workspace path (staged as an archive) or a network locator such as an https URL"},
					"revision": {"type": "string", "description": "Commit SHA, tag, or sha256:<hex> image/content digest pinning the target"}
				}
			},
			"arguments": {"type": "object", "additionalProperties": {"type": "string"}, "description": "Typed registry arguments by name; unknown names and values are rejected"},
			"scope": {"type": "array", "items": {"type": "string"}, "maxItems": 64, "description": "Authorized assets for this execution; required by tools that touch the network"},
			"seed": {"type": "integer", "description": "Seed pinning randomized tool behavior; required by seeded tools"},
			"sensitive_fields": {"type": "array", "items": {"type": "string"}, "maxItems": 32, "description": "Argument names whose values must be redacted from artifacts and replay metadata"},
			"timeout_seconds": {"type": "integer", "minimum": %d, "maximum": %d, "description": "How long to wait for the execution (default %d)"}
		},
		"required": ["tool", "target"]
	}`, securityToolRunMinTimeout, securityToolRunMaxTimeout, securityToolRunDefaultTimeout))
}

// IsReadOnly is true because the tool only records platform state, exactly
// like ingest_scanner_results, so read-only security-scan roles can still run
// deterministic tools. The Job's outbound network activity is bounded by the
// registry's required `scope` for the tool, not by this classification.
func (t *runSecurityToolTool) IsReadOnly() bool                      { return true }
func (t *runSecurityToolTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *runSecurityToolTool) NeedsApproval() bool                   { return false }

// TimeoutSeconds exceeds the longest wait the tool itself will perform so the
// agent runtime never cancels a call that is about to report a verdict.
func (t *runSecurityToolTool) TimeoutSeconds() int { return securityToolRunMaxTimeout + 120 }

func (t *runSecurityToolTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	in, failure := decodeRunSecurityToolInput(input)
	if failure != nil {
		return *failure, nil
	}
	if t.packs == nil {
		return errorResultf("the compiled security tool registry is unavailable: %v", t.packsErr), nil
	}
	tool, ok := t.packs.Tool(in.Tool)
	switch {
	case !ok:
		return errorResultf("unknown security tool %q; available tools: %s", in.Tool, t.enabledToolSummary()), nil
	case !tool.Enabled:
		return errorResultf("tool %s is not executable: %s", tool.Name, tool.DisabledReason), nil
	case !slices.Contains(tool.TargetTypes, strings.TrimSpace(in.Target.Type)):
		return errorResultf("tool %s does not accept target type %q; it accepts %s",
			tool.Name, in.Target.Type, strings.Join(tool.TargetTypes, ", ")), nil
	}

	name, err := securityToolRunName(t.deps.RunName, tool.Name)
	if err != nil {
		return errorResultf("naming the security tool run: %v", err), nil
	}
	spec, staging, failure := t.buildSpec(ctx, in, name)
	if failure != nil {
		return *failure, nil
	}
	if media := securityToolStagedMediaTypes[tool.Name][spec.Target.Type]; media != "" && spec.Target.StagedObjectKey == "" {
		return errorResultf("tool %s requires target media type %s, which only exists after the platform extracts a staged archive; point the locator at the target in this run's workspace instead of %q",
			tool.Name, media, in.Target.Locator), nil
	}
	config, err := securitytoolrun.RunConfigFor(spec)
	if err != nil {
		return errorResultf("invalid request: %v", err), nil
	}
	if _, err := securitytoolrun.Validate(t.packs, config); err != nil {
		return errorResultf("request rejected by the tool registry: %v (arguments and target types are fixed by the registry; no scanner flags are accepted)", err), nil
	}

	run := t.newSecurityToolRun(name, spec)
	if err := t.deps.Client.Create(ctx, run); err != nil {
		return errorResultf("creating SecurityToolRun %s: %v", name, err), nil
	}

	final, outcome, err := t.wait(ctx, name, time.Duration(in.timeout())*time.Second)
	if err != nil {
		return errorResultf("SecurityToolRun %s was created but its status could not be read: %v", name, err), nil
	}
	switch outcome {
	case securityToolRunWaitDisappeared:
		return errorResultf("SecurityToolRun %s no longer exists: it was deleted or garbage-collected before reporting a verdict (last phase %s), so nothing was executed to completion and nothing was ingested — this is not a pass",
			name, securityToolRunPhase(final)), nil
	case securityToolRunWaitUnfinished:
		return errorResultf("SecurityToolRun %s did not finish before the wait ended (budget %ds, last phase %s); it keeps running — inspect it for the verdict rather than assuming a pass",
			name, in.timeout(), securityToolRunPhase(final)), nil
	}
	return t.summarize(ctx, final, staging, in), nil
}

func decodeRunSecurityToolInput(input json.RawMessage) (runSecurityToolInput, *Result) {
	var in runSecurityToolInput
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		result := errorResultf("invalid input: %v (see the tool schema)", err)
		return in, &result
	}
	in.Tool = strings.TrimSpace(in.Tool)
	in.Target.Type = strings.TrimSpace(in.Target.Type)
	in.Target.Locator = strings.TrimSpace(in.Target.Locator)
	in.Target.Revision = strings.TrimSpace(in.Target.Revision)
	if in.Tool == "" {
		result := errorResultf("tool is required")
		return in, &result
	}
	if in.Target.Type == "" || in.Target.Locator == "" {
		result := errorResultf("target.type and target.locator are required")
		return in, &result
	}
	return in, nil
}

func (in runSecurityToolInput) timeout() int {
	seconds := in.TimeoutSeconds
	if seconds <= 0 {
		seconds = securityToolRunDefaultTimeout
	}
	return min(max(seconds, securityToolRunMinTimeout), securityToolRunMaxTimeout)
}

// stagedTarget records what a request staged into object storage, for the
// summary the agent gets back.
type stagedTarget struct {
	ObjectKey string `json:"object_key,omitempty"`
	Digest    string `json:"digest,omitempty"`
	Path      string `json:"path,omitempty"`
	Bytes     int    `json:"bytes,omitempty"`
	Entries   int    `json:"entries,omitempty"`
	Skipped   int    `json:"skipped_entries,omitempty"`
}

// buildSpec assembles the immutable request. Workspace paths are archived and
// uploaded first: the staged key is part of the spec, and the spec cannot be
// edited after creation.
func (t *runSecurityToolTool) buildSpec(ctx context.Context, in runSecurityToolInput, name string) (platformv1alpha1.SecurityToolRunSpec, stagedTarget, *Result) {
	spec := platformv1alpha1.SecurityToolRunSpec{
		Tool: in.Tool,
		Target: platformv1alpha1.SecurityToolTarget{
			Type:     in.Target.Type,
			Locator:  in.Target.Locator,
			Revision: in.Target.Revision,
		},
		Seed:            in.Seed,
		Scope:           in.Scope,
		SensitiveFields: in.SensitiveFields,
		RequestedBy:     t.deps.RunName,
	}
	for _, argName := range slices.Sorted(maps.Keys(in.Arguments)) {
		spec.Arguments = append(spec.Arguments, platformv1alpha1.SecurityToolArgument{
			Name:  argName,
			Value: in.Arguments[argName],
		})
	}

	local, relative, err := t.resolveWorkspacePath(in.Target.Locator)
	if err != nil {
		result := errorResultf("%v", err)
		return spec, stagedTarget{}, &result
	}
	if local == "" {
		// Unstaged targets are network locators. A digest pinned by the
		// locator itself (an image reference) wins; otherwise the control
		// plane treats the digest as an unverified assertion about a live
		// endpoint, which is only ever attached to a network locator — never
		// to a path, because a path can only reach the Job as staged content.
		switch match := securityToolRunLocatorDigestPattern.FindStringSubmatch(in.Target.Locator); {
		case match != nil:
			spec.Target.Digest = match[1]
		case securityToolRunDigestPattern.MatchString(in.Target.Revision):
			spec.Target.Digest = in.Target.Revision
		}
		return spec, stagedTarget{}, nil
	}
	if t.deps.Blobs == nil {
		result := errorResultf("cannot stage workspace path %s: object storage is unavailable (%v)", relative, t.deps.BlobsErr)
		return spec, stagedTarget{}, &result
	}
	// A fuzz campaign only compounds if it starts from what earlier campaigns
	// found, so the persisted corpus is restored into the package's seed
	// corpus before the target is archived. It has to happen here: the staged
	// archive is the only channel into the execution Job.
	t.restoredFuzzInputs = t.restoreGoFuzzCorpus(ctx, in, local)
	archive, entries, skipped, err := archiveWorkspaceTarget(local)
	if err != nil {
		result := errorResultf("staging %s: %v", relative, err)
		return spec, stagedTarget{}, &result
	}
	digest := securityToolRunDigest(archive)
	key := securitytoolrun.TargetObjectKey(t.deps.Namespace, name)
	if err := t.deps.Blobs.Put(ctx, key, archive, stagedTargetMediaType); err != nil {
		result := errorResultf("uploading the staged target for %s: %v", relative, err)
		return spec, stagedTarget{}, &result
	}
	spec.Target.Locator = relative
	spec.Target.Digest = digest
	spec.Target.StagedObjectKey = key
	spec.Target.MediaType = stagedTargetMediaType
	spec.Target.Revision = t.stagedTargetRevision(in.Target.Revision, local, digest)
	return spec, stagedTarget{
		ObjectKey: key,
		Digest:    digest,
		Path:      relative,
		Bytes:     len(archive),
		Entries:   entries,
		Skipped:   skipped,
	}, nil
}

// stagedTargetRevision derives the revision recorded for a staged target. It
// must never be empty: a SecurityScan may scan the head of its base branch
// without pinning a revision, and the tool call's revision is optional too,
// yet the control plane requires both a revision and a digest. The preference
// order is the caller's revision, the scan context's revision, the commit the
// staged checkout has at HEAD, and finally the staged content digest, which
// always describes exactly what was scanned.
func (t *runSecurityToolTool) stagedTargetRevision(callerRevision, local, digest string) string {
	if revision := strings.TrimSpace(callerRevision); revision != "" {
		return revision
	}
	if revision := strings.TrimSpace(t.state.scanCtx.Revision); revision != "" {
		return revision
	}
	root, err := t.workspaceRoot()
	if err == nil {
		if commit := gitHeadCommit(local, root); commit != "" {
			return commit
		}
	}
	return "staged:" + digest
}

var securityToolRunCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}(?:[0-9a-f]{24})?$`)

// gitHeadCommit resolves the commit HEAD points at for the git checkout that
// contains path, looking no further up than ceiling. It reads the plumbing
// files rather than shelling out to git, which the agent image is not
// guaranteed to carry.
func gitHeadCommit(path, ceiling string) string {
	if strings.TrimSpace(ceiling) == "" {
		return ""
	}
	dir := path
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	for {
		if commit := gitDirHeadCommit(filepath.Join(dir, ".git")); commit != "" {
			return commit
		}
		parent := filepath.Dir(dir)
		if dir == ceiling || parent == dir {
			return ""
		}
		dir = parent
	}
}

// gitDirHeadCommit reads HEAD from a git directory, following the "gitdir:"
// indirection a worktree or submodule uses, the loose ref HEAD names, and
// packed-refs when the ref was packed.
func gitDirHeadCommit(gitPath string) string {
	info, err := os.Stat(gitPath)
	if err != nil {
		return ""
	}
	if !info.IsDir() {
		data, readErr := os.ReadFile(gitPath) // #nosec G304 -- workspace content the agent already reads
		if readErr != nil {
			return ""
		}
		target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
		if !ok {
			return ""
		}
		target = strings.TrimSpace(target)
		if target == "" {
			return ""
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(gitPath), target)
		}
		gitPath = target
	}
	head, err := os.ReadFile(filepath.Join(gitPath, "HEAD")) // #nosec G304 -- workspace content the agent already reads
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(string(head))
	if securityToolRunCommitPattern.MatchString(value) {
		return value
	}
	ref, ok := strings.CutPrefix(value, "ref:")
	if !ok {
		return ""
	}
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, "refs/") || slices.Contains(strings.Split(ref, "/"), "..") {
		return ""
	}
	if data, err := os.ReadFile(filepath.Join(gitPath, filepath.FromSlash(ref))); err == nil { // #nosec G304 -- workspace content the agent already reads
		if commit := strings.TrimSpace(string(data)); securityToolRunCommitPattern.MatchString(commit) {
			return commit
		}
	}
	return packedRefCommit(filepath.Join(gitPath, "packed-refs"), ref)
}

// packedRefCommit looks a ref up in packed-refs, ignoring comments and the
// "^" peel lines that follow an annotated tag.
func packedRefCommit(path, ref string) string {
	data, err := os.ReadFile(path) // #nosec G304 -- workspace content the agent already reads
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		commit, name, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found || strings.TrimSpace(name) != ref || !securityToolRunCommitPattern.MatchString(commit) {
			continue
		}
		return commit
	}
	return ""
}

// workspaceRoot is the resolved run workspace directory, or "" when this run
// has none.
func (t *runSecurityToolTool) workspaceRoot() (string, error) {
	root := strings.TrimSpace(t.deps.WorkspaceDir)
	if root == "" {
		return "", nil
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if evaluated, err := filepath.EvalSymlinks(root); err == nil {
		root = evaluated
	}
	return root, nil
}

// resolveWorkspacePath classifies a target locator. It returns the absolute
// and workspace-relative form of content that lives inside the run workspace,
// ("", "", nil) for a network locator that runs without staged content, and an
// error for a locator that names a filesystem path which does not resolve to
// existing content inside the workspace. Anything that is not staged has to be
// a network locator the control plane accepts, so a path pointing outside the
// workspace is refused here instead of travelling into the spec.
func (t *runSecurityToolTool) resolveWorkspacePath(locator string) (string, string, error) {
	if looksLikeNetworkLocator(locator) {
		return "", "", nil
	}
	pathLike := looksLikeFilesystemLocator(locator)
	reject := func(reason string) error {
		return fmt.Errorf("target locator %q %s; a filesystem target must name existing content inside the run workspace so it can be staged, and an unstaged target must be a network locator (an absolute http(s) URL, host[:port], or an image reference pinned with @sha256:)",
			locator, reason)
	}
	root, err := t.workspaceRoot()
	if err != nil {
		return "", "", reject(fmt.Sprintf("cannot be resolved against the run workspace: %v", err))
	}
	if root == "" {
		if pathLike {
			return "", "", reject("cannot be staged because this run has no workspace directory")
		}
		return "", "", nil
	}
	candidate := locator
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, filepath.Clean(candidate))
	}
	if evaluated, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = evaluated
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", reject("resolves to " + candidate + ", outside the run workspace " + root)
	}
	if _, err := os.Lstat(candidate); err != nil {
		if pathLike {
			return "", "", reject("does not exist in the run workspace")
		}
		return "", "", nil
	}
	return candidate, filepath.ToSlash(relative), nil
}

// looksLikeNetworkLocator matches the unstaged forms the control plane
// accepts: an absolute URL and an image reference pinned by digest. A bare
// host[:port] carries no distinguishing syntax and is only treated as a
// network locator when it names nothing in the workspace.
func looksLikeNetworkLocator(locator string) bool {
	return strings.Contains(locator, "://") || securityToolRunLocatorDigestPattern.MatchString(locator)
}

// looksLikeFilesystemLocator reports whether a locator names a path rather
// than a host: anything with a path separator, a home-relative prefix, or the
// current/parent directory.
func looksLikeFilesystemLocator(locator string) bool {
	return strings.Contains(locator, "/") || strings.HasPrefix(locator, "~") ||
		locator == "." || locator == ".."
}

// archiveWorkspaceTarget streams a deterministic tar.gz of a workspace path:
// stable ordering, zeroed timestamps and ownership, sockets/devices and
// escaping symlinks skipped, and a hard total-size cap.
func archiveWorkspaceTarget(root string) (archive []byte, entries, skipped int, err error) {
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil, 0, 0, err
	}
	writer := tar.NewWriter(gz)
	var total int64

	add := func(path string, info fs.FileInfo, name string) error {
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			target, readErr := os.Readlink(path)
			if readErr != nil {
				skipped++
				return nil
			}
			if filepath.IsAbs(target) || escapesArchiveRoot(name, target) {
				skipped++
				return nil
			}
			link = target
		}
		header, headerErr := tar.FileInfoHeader(info, link)
		if headerErr != nil {
			return headerErr
		}
		header.Name = name
		header.Uid, header.Gid = 0, 0
		header.Uname, header.Gname = "", ""
		header.ModTime = time.Unix(0, 0).UTC()
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		if info.Mode().IsRegular() {
			total += info.Size()
			if total > maxStagedTargetBytes {
				return fmt.Errorf("target content exceeds the %d MiB staging limit; scan a smaller subdirectory", maxStagedTargetBytes>>20)
			}
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		entries++
		if !info.Mode().IsRegular() {
			return nil
		}
		file, openErr := os.Open(path) // #nosec G304 -- staged path is workspace content the agent already reads
		if openErr != nil {
			return openErr
		}
		defer func() { _ = file.Close() }()
		_, copyErr := io.Copy(writer, file)
		return copyErr
	}

	info, err := os.Lstat(root)
	if err != nil {
		return nil, 0, 0, err
	}
	if info.IsDir() {
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			if relative == "." {
				return nil
			}
			entryInfo, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			if !entryInfo.IsDir() && !entryInfo.Mode().IsRegular() && entryInfo.Mode()&fs.ModeSymlink == 0 {
				skipped++
				return nil
			}
			return add(path, entryInfo, filepath.ToSlash(relative))
		})
	} else if info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		err = add(root, info, filepath.Base(root))
	} else {
		err = errors.New("target is neither a regular file nor a directory")
	}
	if err != nil {
		return nil, 0, 0, err
	}
	if err := writer.Close(); err != nil {
		return nil, 0, 0, err
	}
	if err := gz.Close(); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), entries, skipped, nil
}

func escapesArchiveRoot(name, link string) bool {
	joined := filepath.Join(filepath.Dir(filepath.FromSlash(name)), filepath.FromSlash(link))
	return joined == ".." || strings.HasPrefix(joined, ".."+string(filepath.Separator))
}

func securityToolRunDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// securityToolRunName mirrors generateName: a readable prefix built from the
// AgentRun and tool names plus a random suffix. The name is chosen client-side
// because the staged target key — part of the immutable spec — embeds it, so
// the suffix carries enough entropy that two concurrent requests cannot land
// on the same key and overwrite each other's staged target.
func securityToolRunName(runName, toolName string) (string, error) {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	prefix := securityToolRunNameSegment(runName, 30)
	if prefix == "" {
		prefix = "agentrun"
	}
	tool := securityToolRunNameSegment(toolName, 20)
	if tool == "" {
		tool = "tool"
	}
	return fmt.Sprintf("%s-%s-%s", prefix, tool, hex.EncodeToString(suffix)), nil
}

var securityToolRunNameInvalid = regexp.MustCompile(`[^a-z0-9-]+`)

func securityToolRunNameSegment(value string, limit int) string {
	cleaned := securityToolRunNameInvalid.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-")
	if len(cleaned) > limit {
		cleaned = cleaned[:limit]
	}
	return strings.Trim(cleaned, "-")
}

func (t *runSecurityToolTool) newSecurityToolRun(name string, spec platformv1alpha1.SecurityToolRunSpec) *platformv1alpha1.SecurityToolRun {
	labels := map[string]string{
		"app.kubernetes.io/name":               "gratefulagents-security-tool-run",
		"platform.gratefulagents.dev/agentrun": t.deps.RunName,
	}
	scanCtx := t.state.scanCtx
	if scanCtx.ScanName != "" {
		labels["security.gratefulagents.dev/scan-name"] = scanCtx.ScanName
	}
	if scanCtx.ExecutionID != "" {
		labels["security.gratefulagents.dev/execution-id"] = scanCtx.ExecutionID
	}
	for key, value := range labels {
		if len(value) > 63 || !securityToolRunLabelValuePattern.MatchString(value) {
			delete(labels, key)
		}
	}
	meta := metav1.ObjectMeta{Name: name, Namespace: t.deps.Namespace, Labels: labels}
	if uid := strings.TrimSpace(t.deps.RunUID); uid != "" {
		meta.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: platformv1alpha1.GroupVersion.String(),
			Kind:       "AgentRun",
			Name:       t.deps.RunName,
			UID:        types.UID(uid),
		}}
	}
	return &platformv1alpha1.SecurityToolRun{ObjectMeta: meta, Spec: spec}
}

// securityToolRunWaitOutcome distinguishes the three ways a wait can end: the
// run reached a verdict, it is still running, or it stopped existing.
type securityToolRunWaitOutcome int

const (
	securityToolRunWaitFinished securityToolRunWaitOutcome = iota
	securityToolRunWaitUnfinished
	securityToolRunWaitDisappeared
)

// wait polls the SecurityToolRun until it reaches a terminal phase, disappears,
// the wait budget expires, or the caller's context is cancelled.
func (t *runSecurityToolTool) wait(ctx context.Context, name string, budget time.Duration) (*platformv1alpha1.SecurityToolRun, securityToolRunWaitOutcome, error) {
	key := client.ObjectKey{Namespace: t.deps.Namespace, Name: name}
	deadline := time.Now().Add(budget)
	interval := t.pollInterval
	if interval <= 0 {
		interval = securityToolRunPollInterval
	}
	var last *platformv1alpha1.SecurityToolRun
	observed := false
	for {
		run := &platformv1alpha1.SecurityToolRun{}
		switch err := t.deps.Client.Get(ctx, key, run); {
		case err == nil:
			observed = true
			last = run
			switch run.Status.Phase {
			case platformv1alpha1.SecurityToolRunPhaseSucceeded, platformv1alpha1.SecurityToolRunPhaseFailed:
				return run, securityToolRunWaitFinished, nil
			}
		case apierrors.IsNotFound(err):
			if observed {
				// Deleted or garbage-collected mid-flight: no verdict is coming.
				return last, securityToolRunWaitDisappeared, nil
			}
		default:
			return last, securityToolRunWaitUnfinished, err
		}
		if time.Now().After(deadline) {
			if !observed {
				// The object this call created never became readable.
				return last, securityToolRunWaitDisappeared, nil
			}
			return last, securityToolRunWaitUnfinished, nil
		}
		select {
		case <-ctx.Done():
			// A cancelled call is still an unfinished execution, never a pass.
			return last, securityToolRunWaitUnfinished, nil
		case <-time.After(interval):
		}
	}
}

func securityToolRunPhase(run *platformv1alpha1.SecurityToolRun) string {
	if run == nil || run.Status.Phase == "" {
		return "Pending"
	}
	return run.Status.Phase
}

// runSecurityToolSummary is the compact structured report the agent sees.
type runSecurityToolSummary struct {
	Tool            string                      `json:"tool"`
	SecurityToolRun string                      `json:"security_tool_run"`
	Phase           string                      `json:"phase"`
	Status          string                      `json:"status"`
	Message         string                      `json:"message,omitempty"`
	Findings        runSecurityToolFindings     `json:"findings"`
	Coverage        *securitytoolpacks.Coverage `json:"coverage,omitempty"`
	Errors          []string                    `json:"errors,omitempty"`
	Result          *runSecurityToolResultRef   `json:"result,omitempty"`
	Artifacts       []runSecurityToolArtifact   `json:"artifacts,omitempty"`
	Replay          *runSecurityToolReplay      `json:"replay,omitempty"`
	StagedTarget    *stagedTarget               `json:"staged_target,omitempty"`
	Notes           []string                    `json:"notes,omitempty"`
}

type runSecurityToolFindings struct {
	Reported     int `json:"reported"`
	IngestedNew  int `json:"ingested_new"`
	Merged       int `json:"merged"`
	NotIngested  int `json:"not_ingested,omitempty"`
	Correlations int `json:"new_correlations,omitempty"`
}

type runSecurityToolResultRef struct {
	ObjectKey string `json:"object_key"`
	Digest    string `json:"digest"`
}

type runSecurityToolArtifact struct {
	Name      string `json:"name,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	ObjectKey string `json:"object_key"`
	Digest    string `json:"digest"`
}

type runSecurityToolReplay struct {
	ToolVersion  string `json:"tool_version,omitempty"`
	ImageDigest  string `json:"image_digest,omitempty"`
	ConfigDigest string `json:"config_digest,omitempty"`
}

// summarize turns a terminal SecurityToolRun into the agent-facing verdict. A
// failed run, an unreadable result, and a digest mismatch all stay errors: none
// of them may look like a clean scan.
func (t *runSecurityToolTool) summarize(ctx context.Context, run *platformv1alpha1.SecurityToolRun, staging stagedTarget, in runSecurityToolInput) Result {
	summary := runSecurityToolSummary{
		Tool:            run.Spec.Tool,
		SecurityToolRun: run.Name,
		Phase:           securityToolRunPhase(run),
		Status:          "error",
		Message:         run.Status.Message,
	}
	if staging.ObjectKey != "" {
		summary.StagedTarget = &staging
	}
	result := run.Status.Result
	if result != nil {
		summary.Status = result.Status
		summary.Errors = result.Errors
		summary.Findings.Reported = int(result.FindingCount)
	}
	if run.Status.Phase == platformv1alpha1.SecurityToolRunPhaseFailed {
		// A failed run may still carry the retired "pass" verdict from an older
		// executor; it is spelled literally so this keeps working once the
		// deprecated constant is removed.
		if summary.Status == "" || summary.Status == "pass" {
			summary.Status = string(securitytoolpacks.StatusError)
		}
		return securityToolRunResult(summary, true)
	}
	if result == nil || strings.TrimSpace(result.ResultObjectKey) == "" {
		summary.Status = string(securitytoolpacks.StatusError)
		summary.Notes = append(summary.Notes, "the execution reported no result document")
		return securityToolRunResult(summary, true)
	}
	summary.Result = &runSecurityToolResultRef{ObjectKey: result.ResultObjectKey, Digest: result.ResultDigest}
	for _, artifact := range result.Artifacts {
		summary.Artifacts = append(summary.Artifacts, runSecurityToolArtifact{
			Name:      artifact.Name,
			MediaType: artifact.MediaType,
			ObjectKey: artifact.ObjectKey,
			Digest:    artifact.Digest,
		})
	}
	// The campaign's new inputs become the next campaign's seed corpus, and
	// the note says whether this run started cold or warm — a clean result
	// from a cold first campaign means much less than one from a warm tenth.
	if note := t.persistGoFuzzCorpus(ctx, in, summary.Artifacts); note != "" {
		summary.Notes = append(summary.Notes, note)
	}

	document, err := t.fetchResult(ctx, result)
	if err != nil {
		summary.Status = string(securitytoolpacks.StatusError)
		summary.Notes = append(summary.Notes, err.Error())
		return securityToolRunResult(summary, true)
	}
	summary.Status = string(document.Status)
	summary.Findings.Reported = len(document.Findings)
	summary.Errors = document.Errors
	summary.Coverage = coverageOrNil(document.Coverage)
	summary.Replay = &runSecurityToolReplay{
		ToolVersion:  document.Replay.ToolVersion,
		ImageDigest:  document.Replay.ImageDigest,
		ConfigDigest: document.Replay.ConfigurationID,
	}

	outcome := t.ingest(ctx, document.Findings)
	summary.Findings.IngestedNew = outcome.created
	summary.Findings.Merged = outcome.merged
	summary.Findings.NotIngested = outcome.notIngested
	summary.Findings.Correlations = outcome.correlated
	summary.Notes = append(summary.Notes, outcome.notes...)

	failed := outcome.failed ||
		summary.Status == string(securitytoolpacks.StatusError) ||
		summary.Status == string(securitytoolpacks.StatusTimeout)
	return securityToolRunResult(summary, failed)
}

func coverageOrNil(coverage securitytoolpacks.Coverage) *securitytoolpacks.Coverage {
	if len(coverage.Examined) == 0 && len(coverage.Skipped) == 0 && len(coverage.Uncovered) == 0 {
		return nil
	}
	const maxCoverageEntries = 20
	trim := func(values []string) []string {
		if len(values) <= maxCoverageEntries {
			return values
		}
		return append(slices.Clone(values[:maxCoverageEntries]),
			fmt.Sprintf("... %d more", len(values)-maxCoverageEntries))
	}
	return &securitytoolpacks.Coverage{
		Examined:  trim(coverage.Examined),
		Skipped:   trim(coverage.Skipped),
		Uncovered: trim(coverage.Uncovered),
	}
}

// fetchResult downloads result.json and refuses to decode bytes that do not
// match the digest the controller recorded.
func (t *runSecurityToolTool) fetchResult(ctx context.Context, result *platformv1alpha1.SecurityToolRunResult) (securitytoolpacks.Result, error) {
	if t.deps.Blobs == nil {
		return securitytoolpacks.Result{}, fmt.Errorf("result %s cannot be read: object storage is unavailable (%v)", result.ResultObjectKey, t.deps.BlobsErr)
	}
	raw, err := t.deps.Blobs.Get(ctx, result.ResultObjectKey)
	if err != nil {
		if strings.Contains(err.Error(), "exceeds the") {
			// Object reads go through the project-content store, whose cap is
			// far below what a large scan can produce. Say so explicitly: the
			// run succeeded and the document is intact, it just cannot be
			// ingested from here.
			return securitytoolpacks.Result{}, fmt.Errorf("result %s is larger than the %d MiB object-read cap, so no finding could be ingested; the execution succeeded and the document is intact in object storage (digest %s) — narrow the target or scope and re-run to get an ingestable result (%v)",
				result.ResultObjectKey, store.MaxProjectContentVersionBytes>>20, result.ResultDigest, err)
		}
		return securitytoolpacks.Result{}, fmt.Errorf("downloading result %s: %v", result.ResultObjectKey, err)
	}
	if actual := securityToolRunDigest(raw); actual != result.ResultDigest {
		return securitytoolpacks.Result{}, fmt.Errorf("result %s digest mismatch: recorded %s, downloaded %s; the document was not trusted and nothing was ingested",
			result.ResultObjectKey, result.ResultDigest, actual)
	}
	var document securitytoolpacks.Result
	if err := json.Unmarshal(raw, &document); err != nil {
		return securitytoolpacks.Result{}, fmt.Errorf("decoding result %s: %v", result.ResultObjectKey, err)
	}
	return document, nil
}

type securityToolIngestOutcome struct {
	created     int
	merged      int
	correlated  int
	notIngested int
	notes       []string
	failed      bool
}

// ingest pushes the produced records through the same normalization, budget,
// dedupe and correlation path as ingest_scanner_results.
func (t *runSecurityToolTool) ingest(ctx context.Context, records []security.ScannerRecord) securityToolIngestOutcome {
	outcome := securityToolIngestOutcome{}
	if len(records) == 0 {
		return outcome
	}
	if len(records) > maxScannerBatchRecords {
		outcome.notIngested = len(records) - maxScannerBatchRecords
		outcome.notes = append(outcome.notes, fmt.Sprintf("only the first %d of %d records were ingested (batch cap); the full document is in object storage",
			maxScannerBatchRecords, len(records)))
		records = records[:maxScannerBatchRecords]
	}
	scanCtx := t.state.scanCtx
	findings := make([]security.Finding, 0, len(records))
	accepted := make([]security.ScannerRecord, 0, len(records))
	rejected := 0
	for _, record := range records {
		finding, err := security.NormalizeScannerRecord(record, scanCtx.Repository, scanCtx.Revision)
		if err != nil {
			rejected++
			continue
		}
		finding.SourceAgent = scanCtx.RunName
		findings = append(findings, finding)
		accepted = append(accepted, record)
	}
	if rejected > 0 {
		outcome.notIngested += rejected
		outcome.notes = append(outcome.notes, fmt.Sprintf("%d record(s) were not ingestable and were skipped", rejected))
	}

	scanID, err := t.state.ensureScan(ctx)
	if err != nil {
		outcome.failed = true
		outcome.notIngested += len(findings)
		outcome.notes = append(outcome.notes, fmt.Sprintf("failed to open the scan record: %v", err))
		return outcome
	}
	created, merged, stoppedAt, err := ingestNormalizedScannerFindings(ctx, t.state, scanID, findings, accepted)
	outcome.created, outcome.merged = created, merged
	if err != nil {
		outcome.failed = true
		outcome.notIngested += len(findings) - stoppedAt
		outcome.notes = append(outcome.notes, fmt.Sprintf("failed to persist finding %d: %v", stoppedAt, err))
		return outcome
	}
	correlated, err := t.state.correlateScanFindings(ctx)
	if err != nil {
		outcome.failed = true
		outcome.notes = append(outcome.notes, fmt.Sprintf("findings were ingested but correlation failed: %v", err))
		return outcome
	}
	outcome.correlated = correlated
	return outcome
}

func securityToolRunResult(summary runSecurityToolSummary, isError bool) Result {
	encoded, err := json.Marshal(summary)
	if err != nil {
		return errorResultf("encoding the security tool summary: %v", err)
	}
	return Result{Content: string(encoded), IsError: isError}
}

func errorResultf(format string, args ...any) Result {
	return Result{Content: fmt.Sprintf(format, args...), IsError: true}
}
