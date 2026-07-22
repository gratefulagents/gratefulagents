package dashboard

import (
	"context"
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const (
	agentRunWorkerContainerName = "worker"
	defaultAgentRunLogTailLines = int32(2000)
	maxAgentRunLogTailLines     = int32(5000)
	maxAgentRunLogBytes         = int64(2 * 1024 * 1024)
)

// GetAgentRunLogs returns a bounded tail of the current worker container log.
// The referenced pod is verified against the controller-owned run identity
// before the dashboard's privileged Kubernetes client reads any output.
func (s *Server) GetAgentRunLogs(ctx context.Context, req *platform.GetAgentRunLogsRequest) (*platform.GetAgentRunLogsResponse, error) {
	if err := s.requireAgentRunViewer(ctx, req.GetNamespace(), req.GetName()); err != nil {
		return nil, err
	}

	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.GetNamespace(), Name: req.GetName()}, run); err != nil {
		return nil, mapK8sError("GetAgentRunLogs", err)
	}

	out := &platform.GetAgentRunLogsResponse{IsComplete: isTerminalAgentRunPhase(run.Status.Phase)}
	podName := agentRunPodName(run)
	out.PodName = podName
	if podName == "" || s.clientset == nil {
		return out, nil
	}

	pod, err := s.clientset.CoreV1().Pods(run.Namespace).Get(ctx, podName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return nil, mapK8sError("GetAgentRunLogs", err)
	}
	if !isPodOwnedByAgentRun(pod, run) {
		// Do not allow a mutable status reference to turn this endpoint into an
		// arbitrary pod-log reader, and do not disclose the referenced pod.
		out.PodName = ""
		return out, nil
	}

	tailLines := req.GetTailLines()
	if tailLines <= 0 {
		tailLines = defaultAgentRunLogTailLines
	}
	if tailLines > maxAgentRunLogTailLines {
		tailLines = maxAgentRunLogTailLines
	}
	if !isWorkerContainerReadyForLogs(pod) {
		return out, nil
	}

	// Ask for one extra line so the response can accurately tell the UI when
	// older output was omitted. Read the stream into a bounded suffix buffer
	// rather than using PodLogOptions.LimitBytes: Kubernetes applies that limit
	// to the beginning of the selected stream, which can discard the newest
	// output that a tail viewer must preserve.
	requestedLines := int64(tailLines) + 1
	stream, err := s.clientset.CoreV1().Pods(run.Namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container:  agentRunWorkerContainerName,
		TailLines:  &requestedLines,
		Timestamps: true,
	}).Stream(ctx)
	if apierrors.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("reading worker logs: %w", err))
	}
	defer func() { _ = stream.Close() }()

	content, byteTruncated, err := readBoundedLogSuffix(stream, int(maxAgentRunLogBytes))
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("reading worker logs: %w", err))
	}
	out.Content, out.Truncated = trimAgentRunLogTail(content, int(tailLines))
	out.Truncated = out.Truncated || byteTruncated
	out.Available = true
	return out, nil
}

func isWorkerContainerReadyForLogs(pod *corev1.Pod) bool {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == agentRunWorkerContainerName {
			return status.State.Waiting == nil
		}
	}
	// A newly created pod may not have container statuses yet. Treat that as
	// temporarily unavailable so the UI keeps polling instead of receiving a
	// PodInitializing error and stopping its automatic refresh loop.
	return false
}

type suffixBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (b *suffixBuffer) Write(chunk []byte) (int, error) {
	written := len(chunk)
	if b.limit <= 0 {
		b.truncated = b.truncated || written > 0
		return written, nil
	}
	if len(chunk) >= b.limit {
		b.data = append(b.data[:0], chunk[len(chunk)-b.limit:]...)
		b.truncated = true
		return written, nil
	}
	if overflow := len(b.data) + len(chunk) - b.limit; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
		b.truncated = true
	}
	b.data = append(b.data, chunk...)
	return written, nil
}

func readBoundedLogSuffix(reader io.Reader, maxBytes int) (string, bool, error) {
	buffer := &suffixBuffer{limit: maxBytes, data: make([]byte, 0, maxBytes)}
	if _, err := io.Copy(buffer, reader); err != nil {
		return "", false, err
	}
	content := string(buffer.data)
	if buffer.truncated {
		// A byte boundary can land in the middle of a timestamped log line.
		// Drop that partial line before displaying the retained suffix.
		if _, suffix, ok := strings.Cut(content, "\n"); ok {
			content = suffix
		} else {
			content = ""
		}
	}
	return strings.ToValidUTF8(content, ""), buffer.truncated, nil
}

func trimAgentRunLogTail(content string, maxLines int) (string, bool) {
	content = strings.ToValidUTF8(content, "�")
	if maxLines <= 0 || content == "" {
		return content, false
	}
	hasTrailingNewline := strings.HasSuffix(content, "\n")
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if len(lines) <= maxLines {
		return content, false
	}
	trimmed := strings.Join(lines[len(lines)-maxLines:], "\n")
	if hasTrailingNewline {
		trimmed += "\n"
	}
	return trimmed, true
}
