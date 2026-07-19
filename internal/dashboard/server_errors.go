package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

const (
	recentErrorEventScanLimit = 1000 // fallback for non-Postgres stores
	maxAgentRunErrors         = 200
	maxAgentRunErrorBytes     = 8 * 1024
	podErrorLogTailLines      = 2000
	podErrorLogLimitBytes     = 1024 * 1024
)

type recentErrorActivityStore interface {
	GetRecentErrorActivity(context.Context, uuid.UUID, int32) ([]store.ActivityEvent, error)
}

// GetAgentRunErrors returns only actionable errors from the durable activity
// stream and the current worker pod. It intentionally excludes ordinary pod
// output and tracing data so opening the dashboard cannot pull a full log or
// trace payload.
func (s *Server) GetAgentRunErrors(ctx context.Context, req *platform.GetAgentRunErrorsRequest) (*platform.GetAgentRunErrorsResponse, error) {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return nil, err
	}

	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, run); err != nil {
		return nil, mapK8sError("GetAgentRunErrors", err)
	}

	out := &platform.GetAgentRunErrorsResponse{IsComplete: isTerminalAgentRunPhase(run.Status.Phase)}
	if s.stateStore != nil {
		sess, err := s.cachedSessionByRun(ctx, run.Name, run.Namespace)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("loading durable run errors: %w", err))
		}
		// A worker can fail before initSessionClient creates its Postgres row.
		// Missing durable history must not hide the CRD LastError or pod fallback.
		if err == nil && sess != nil {
			var events []store.ActivityEvent
			if errorStore, ok := s.stateStore.(recentErrorActivityStore); ok {
				events, err = errorStore.GetRecentErrorActivity(ctx, sess.ID, maxAgentRunErrors+1)
				if len(events) > maxAgentRunErrors {
					out.Truncated = true
					events = events[:maxAgentRunErrors]
				}
			} else {
				events, err = s.stateStore.GetRecentActivity(ctx, sess.ID, recentErrorEventScanLimit)
				if len(events) == recentErrorEventScanLimit {
					out.Truncated = true
				}
			}
			if err != nil {
				return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("loading durable run errors: %w", err))
			}
			for _, event := range events {
				var content agent.ContentEvent
				if json.Unmarshal(event.Detail, &content) != nil || !isErrorContentEvent(&content, event.EventType) {
					continue
				}
				message := firstNonEmpty(content.Message, content.Output, content.Reason, event.Summary)
				if message == "" {
					continue
				}
				out.Errors = append(out.Errors, &platform.AgentRunError{
					TimestampUnix: event.CreatedAt.Unix(),
					Message:       boundedErrorMessage(message),
					Source:        "activity",
					Kind:          firstNonEmpty(content.Type, event.EventType),
				})
			}
		}
	}

	// LastError covers bootstrap failures (for example git clone) that happen
	// before the structured activity stream is fully initialized.
	if message := strings.TrimSpace(run.Status.LastError); message != "" {
		timestamp := run.CreationTimestamp.Time
		if run.Status.CompletedAt != nil {
			timestamp = run.Status.CompletedAt.Time
		}
		out.Errors = append(out.Errors, &platform.AgentRunError{
			TimestampUnix: timestamp.Unix(),
			Message:       boundedErrorMessage(message),
			Source:        "status",
			Kind:          "run",
		})
	}

	if podName := agentRunPodName(run); podName != "" && s.clientset != nil {
		// The worker can update AgentRun status, so never trust sandboxRef as an
		// authorization boundary. Verify the referenced pod still carries the
		// controller-owned run identity before using the dashboard's privileged
		// Kubernetes client to read its logs.
		if pod, err := s.clientset.CoreV1().Pods(run.Namespace).Get(ctx, podName, metav1.GetOptions{}); err == nil && isPodOwnedByAgentRun(pod, run) {
			tail, limit := int64(podErrorLogTailLines), int64(podErrorLogLimitBytes)
			raw, err := s.clientset.CoreV1().Pods(run.Namespace).GetLogs(podName, &corev1.PodLogOptions{
				Container:  "worker",
				TailLines:  &tail,
				LimitBytes: &limit,
				Timestamps: true,
			}).DoRaw(ctx)
			if err == nil {
				out.Errors = append(out.Errors, podErrorEntries(string(raw))...)
			}
		}
	}

	out.Errors = dedupeAndSortErrors(out.Errors)
	if len(out.Errors) > maxAgentRunErrors {
		out.Errors = out.Errors[len(out.Errors)-maxAgentRunErrors:]
		out.Truncated = true
	}
	return out, nil
}

func agentRunPodName(run *platformv1alpha1.AgentRun) string {
	if run == nil || run.Status.Sandbox == nil || run.Status.Sandbox.SandboxRef == nil {
		return ""
	}
	return strings.TrimSpace(run.Status.Sandbox.SandboxRef.Name)
}

func isPodOwnedByAgentRun(pod *corev1.Pod, run *platformv1alpha1.AgentRun) bool {
	if pod == nil || run == nil || pod.Namespace != run.Namespace {
		return false
	}
	if pod.Labels["platform.gratefulagents.dev/owner-run"] != run.Name ||
		pod.Labels["platform.gratefulagents.dev/owner-run-uid"] != string(run.UID) {
		return false
	}
	for _, container := range pod.Spec.Containers {
		if container.Name == "worker" {
			return true
		}
	}
	return false
}

func isErrorContentEvent(event *agent.ContentEvent, storedType string) bool {
	if event == nil {
		return false
	}
	if event.IsError || strings.TrimSpace(event.FailureKind) != "" {
		return true
	}
	for _, value := range []string{event.Type, event.AttemptStatus, event.Status, storedType} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "error", "failed", "failure", "fatal", "runtime_error":
			return true
		}
	}
	return false
}

func podErrorEntries(logs string) []*platform.AgentRunError {
	var entries []*platform.AgentRunError
	for _, line := range strings.Split(logs, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		timestamp, message := splitKubernetesLogTimestamp(line)
		if !isPodErrorMessage(message) {
			continue
		}
		entries = append(entries, &platform.AgentRunError{
			TimestampUnix: timestamp,
			Message:       boundedErrorMessage(message),
			Source:        "pod",
			Kind:          "worker",
		})
	}
	return entries
}

func splitKubernetesLogTimestamp(line string) (int64, string) {
	prefix, message, ok := strings.Cut(line, " ")
	if !ok {
		return 0, line
	}
	parsed, err := time.Parse(time.RFC3339Nano, prefix)
	if err != nil {
		return 0, line
	}
	return parsed.Unix(), strings.TrimSpace(message)
}

func isPodErrorMessage(message string) bool {
	upper := strings.ToUpper(message)
	normalized := strings.ToLower(strings.ReplaceAll(message, " ", ""))
	return strings.Contains(upper, "ERROR:") ||
		strings.Contains(upper, "FATAL:") ||
		strings.HasPrefix(upper, "PANIC:") ||
		strings.Contains(normalized, `"level":"error"`) ||
		strings.Contains(normalized, `"level":"fatal"`) ||
		strings.Contains(normalized, `level=error`) ||
		strings.Contains(normalized, `level=fatal`)
}

func boundedErrorMessage(message string) string {
	message = strings.TrimSpace(message)
	if len(message) <= maxAgentRunErrorBytes {
		return message
	}
	const suffix = "…"
	limit := maxAgentRunErrorBytes - len(suffix)
	for limit > 0 && !utf8.ValidString(message[:limit]) {
		limit--
	}
	return message[:limit] + suffix
}

func dedupeAndSortErrors(errors []*platform.AgentRunError) []*platform.AgentRunError {
	sort.SliceStable(errors, func(i, j int) bool { return errors[i].TimestampUnix < errors[j].TimestampUnix })
	seen := make(map[string]struct{}, len(errors))
	out := errors[:0]
	for _, entry := range errors {
		if entry == nil || strings.TrimSpace(entry.Message) == "" {
			continue
		}
		key := entry.Source + "\x00" + entry.Message
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, entry)
	}
	return out
}
