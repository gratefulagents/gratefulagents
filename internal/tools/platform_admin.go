package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	platformAdminDefaultLimit = 50
	platformAdminMaxLimit     = 200
	platformAdminMaxBodyBytes = 4 << 20
)

// RegisterPlatformAdminTools registers read-only Kubernetes/platform
// introspection tools. Callers must only invoke this for AgentRuns whose spec
// has kubernetesAdmin=true; the tools themselves do not grant credentials.
func RegisterPlatformAdminTools(registry *Registry, crdClient client.Client, k8sClient kubernetes.Interface, currentNamespace string) {
	registerPlatformAdminTools(registry, crdClient, k8sClient, nil, currentNamespace)
}

// RegisterPlatformAdminToolsWithStore also enables PostgreSQL-backed activity
// lookup when an AgentRun does not yet have an S3 events log artifact.
func RegisterPlatformAdminToolsWithStore(registry *Registry, crdClient client.Client, k8sClient kubernetes.Interface, stateStore store.StateStore, currentNamespace string) {
	registerPlatformAdminTools(registry, crdClient, k8sClient, stateStore, currentNamespace)
}

func registerPlatformAdminTools(registry *Registry, crdClient client.Client, k8sClient kubernetes.Interface, stateStore store.StateStore, currentNamespace string) {
	if registry == nil || crdClient == nil || k8sClient == nil {
		return
	}
	base := platformAdminToolBase{
		crdClient:        crdClient,
		k8sClient:        k8sClient,
		stateStore:       stateStore,
		currentNamespace: strings.TrimSpace(currentNamespace),
	}
	registry.Register(&platformListRunsTool{platformAdminToolBase: base})
	registry.Register(&platformGetRunTool{platformAdminToolBase: base})
	registry.Register(&platformRunActivityTool{platformAdminToolBase: base})
	registry.Register(&platformRunTraceTool{platformAdminToolBase: base})
	registry.Register(&platformListPodsTool{platformAdminToolBase: base})
	registry.Register(&platformPodLogsTool{platformAdminToolBase: base})
}

type platformAdminToolBase struct {
	crdClient        client.Client
	k8sClient        kubernetes.Interface
	stateStore       store.StateStore
	currentNamespace string
}

func (b platformAdminToolBase) namespaceOrCurrent(namespace string) string {
	if namespace = strings.TrimSpace(namespace); namespace != "" {
		return namespace
	}
	return b.currentNamespace
}

func (b platformAdminToolBase) getRun(ctx context.Context, namespace, name string) (*platformv1alpha1.AgentRun, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	namespace = b.namespaceOrCurrent(namespace)
	if namespace == "" {
		return nil, fmt.Errorf("namespace is required")
	}
	run := &platformv1alpha1.AgentRun{}
	if err := b.crdClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, run); err != nil {
		return nil, err
	}
	return run, nil
}

type platformAdminBaseTool struct{}

func (platformAdminBaseTool) IsReadOnly() bool                      { return true }
func (platformAdminBaseTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (platformAdminBaseTool) NeedsApproval() bool                   { return false }
func (platformAdminBaseTool) TimeoutSeconds() int                   { return 30 }

type platformListRunsTool struct {
	platformAdminBaseTool
	platformAdminToolBase
}

func (t *platformListRunsTool) Name() string { return "platform_list_runs" }
func (t *platformListRunsTool) Description() string {
	return "List AgentRuns from the Kubernetes API for platform introspection. Read-only; available only on Kubernetes-admin runs."
}
func (t *platformListRunsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "namespace": {"type": "string", "description": "Namespace to list. Defaults to this run's namespace unless all_namespaces is true."},
    "all_namespaces": {"type": "boolean", "description": "List AgentRuns across all namespaces."},
    "limit": {"type": "integer", "description": "Maximum runs to return (default 50, max 200)."}
  }
}`)
}

type listRunsInput struct {
	Namespace     string `json:"namespace"`
	AllNamespaces bool   `json:"all_namespaces"`
	Limit         int    `json:"limit"`
}

type runSummary struct {
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	Phase           string `json:"phase,omitempty"`
	CurrentStep     string `json:"current_step,omitempty"`
	DisplayName     string `json:"display_name,omitempty"`
	KubernetesAdmin bool   `json:"kubernetes_admin,omitempty"`
	CreatedAtUnix   int64  `json:"created_at_unix,omitempty"`
	StartedAtUnix   int64  `json:"started_at_unix,omitempty"`
	CompletedAtUnix int64  `json:"completed_at_unix,omitempty"`
	TraceID         string `json:"trace_id,omitempty"`
	EventsLogURL    string `json:"events_log_url,omitempty"`
	LastError       string `json:"last_error,omitempty"`
}

func summarizeRun(run *platformv1alpha1.AgentRun) runSummary {
	out := runSummary{
		Namespace:       run.Namespace,
		Name:            run.Name,
		Phase:           string(run.Status.Phase),
		CurrentStep:     run.Status.CurrentStep,
		DisplayName:     run.Status.DisplayName,
		KubernetesAdmin: run.Spec.KubernetesAdmin,
		CreatedAtUnix:   run.CreationTimestamp.Unix(),
		LastError:       run.Status.LastError,
	}
	if run.Status.StartedAt != nil {
		out.StartedAtUnix = run.Status.StartedAt.Unix()
	}
	if run.Status.CompletedAt != nil {
		out.CompletedAtUnix = run.Status.CompletedAt.Unix()
	}
	if run.Status.Artifacts != nil {
		out.TraceID = run.Status.Artifacts.TraceID
		out.EventsLogURL = run.Status.Artifacts.EventsLogURL
	}
	return out
}

func (t *platformListRunsTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in listRunsInput
	if err := json.Unmarshal(input, &in); err != nil && len(input) > 0 {
		return errorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	limit := clampLimit(in.Limit)
	var opts []client.ListOption
	if !in.AllNamespaces {
		ns := t.namespaceOrCurrent(in.Namespace)
		if ns == "" {
			return errorResult("namespace is required unless all_namespaces is true"), nil
		}
		opts = append(opts, client.InNamespace(ns))
	}
	var runs platformv1alpha1.AgentRunList
	if err := t.crdClient.List(ctx, &runs, opts...); err != nil {
		return errorResult(fmt.Sprintf("list AgentRuns: %v", err)), nil
	}
	out := make([]runSummary, 0, minInt(len(runs.Items), limit))
	for i := range runs.Items {
		if len(out) >= limit {
			break
		}
		out = append(out, summarizeRun(&runs.Items[i]))
	}
	return jsonResult(map[string]any{"runs": out, "truncated": len(runs.Items) > len(out)}), nil
}

type platformGetRunTool struct {
	platformAdminBaseTool
	platformAdminToolBase
}

func (t *platformGetRunTool) Name() string { return "platform_get_run" }
func (t *platformGetRunTool) Description() string {
	return "Get an AgentRun's spec/status summary from the Kubernetes API. Read-only; available only on Kubernetes-admin runs."
}
func (t *platformGetRunTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "namespace": {"type": "string", "description": "AgentRun namespace. Defaults to this run's namespace."},
    "name": {"type": "string", "description": "AgentRun name."}
  },
  "required": ["name"]
}`)
}

type namedRunInput struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func (t *platformGetRunTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in namedRunInput
	if err := json.Unmarshal(input, &in); err != nil {
		return errorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	run, err := t.getRun(ctx, in.Namespace, in.Name)
	if err != nil {
		return errorResult(fmt.Sprintf("get AgentRun: %v", err)), nil
	}
	return jsonResult(map[string]any{
		"summary": summarizeRun(run),
		"spec": map[string]any{
			"trigger":             run.Spec.Trigger,
			"repository":          run.Spec.Repository,
			"workflow_mode":       run.Spec.WorkflowMode,
			"execution_mode":      run.Spec.ExecutionMode,
			"model":               run.Spec.Model,
			"runtime_profile_ref": run.Spec.RuntimeProfileRef,
			"mcp_policy_ref":      run.Spec.MCPPolicyRef,
			"kubernetes_admin":    run.Spec.KubernetesAdmin,
		},
		"status": run.Status,
	}), nil
}

type platformRunActivityTool struct {
	platformAdminBaseTool
	platformAdminToolBase
}

func (t *platformRunActivityTool) Name() string { return "platform_run_activity" }
func (t *platformRunActivityTool) Description() string {
	return "Fetch a run's recent activity stream from S3 or its durable PostgreSQL session."
}
func (t *platformRunActivityTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "namespace": {"type": "string", "description": "AgentRun namespace. Defaults to this run's namespace."},
    "name": {"type": "string", "description": "AgentRun name."},
    "limit": {"type": "integer", "description": "Maximum events to return (default 50, max 200)."}
  },
  "required": ["name"]
}`)
}

func (t *platformRunActivityTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in namedRunInput
	if err := json.Unmarshal(input, &in); err != nil {
		return errorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	var limitIn struct {
		Limit int `json:"limit"`
	}
	_ = json.Unmarshal(input, &limitIn)
	limit := clampLimit(limitIn.Limit)
	run, err := t.getRun(ctx, in.Namespace, in.Name)
	if err != nil {
		return errorResult(fmt.Sprintf("get AgentRun: %v", err)), nil
	}
	eventsLogURL := ""
	if run.Status.Artifacts != nil {
		eventsLogURL = strings.TrimSpace(run.Status.Artifacts.EventsLogURL)
	}
	var s3Err error
	if eventsLogURL != "" {
		events, truncated, err := fetchS3JSONLEvents(ctx, eventsLogURL, limit)
		if err == nil {
			return jsonResult(map[string]any{"source": "s3", "events_log_url": eventsLogURL, "events": events, "truncated": truncated}), nil
		}
		s3Err = err
	}
	if t.stateStore == nil {
		if s3Err != nil {
			return errorResult(s3Err.Error()), nil
		}
		return errorResult("run has no status.artifacts.eventsLogURL and PostgreSQL activity is unavailable"), nil
	}
	session, err := t.stateStore.GetSessionByRun(ctx, run.Name, run.Namespace)
	if err != nil {
		return errorResult(platformActivityFallbackError(s3Err, fmt.Errorf("get PostgreSQL session for AgentRun: %w", err))), nil
	}
	activity, err := t.stateStore.GetRecentActivity(ctx, session.ID, int32(limit+1))
	if err != nil {
		return errorResult(platformActivityFallbackError(s3Err, fmt.Errorf("get PostgreSQL activity: %w", err))), nil
	}
	events, truncated := postgresActivityEvents(activity, limit)
	result := map[string]any{"source": "postgres", "events": events, "truncated": truncated}
	if s3Err != nil {
		result["s3_error"] = s3Err.Error()
	}
	return jsonResult(result), nil
}

type platformRunTraceTool struct {
	platformAdminBaseTool
	platformAdminToolBase
}

func (t *platformRunTraceTool) Name() string { return "platform_run_trace" }
func (t *platformRunTraceTool) Description() string {
	return "Fetch a run's Jaeger trace using status.artifacts.traceID and JAEGER_QUERY_URL (or derived from OTEL_EXPORTER_OTLP_ENDPOINT)."
}
func (t *platformRunTraceTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "namespace": {"type": "string", "description": "AgentRun namespace. Defaults to this run's namespace."},
    "name": {"type": "string", "description": "AgentRun name."}
  },
  "required": ["name"]
}`)
}

var platformTraceIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{1,32}$`)

func (t *platformRunTraceTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in namedRunInput
	if err := json.Unmarshal(input, &in); err != nil {
		return errorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	run, err := t.getRun(ctx, in.Namespace, in.Name)
	if err != nil {
		return errorResult(fmt.Sprintf("get AgentRun: %v", err)), nil
	}
	traceID := ""
	if run.Status.Artifacts != nil {
		traceID = strings.TrimSpace(run.Status.Artifacts.TraceID)
	}
	if traceID == "" {
		return errorResult("run has no status.artifacts.traceID"), nil
	}
	if !platformTraceIDPattern.MatchString(traceID) {
		return errorResult(fmt.Sprintf("invalid trace ID %q", traceID)), nil
	}
	baseURL := derivePlatformJaegerQueryURL()
	if baseURL == "" {
		return jsonResult(map[string]any{"trace_id": traceID, "message": "JAEGER_QUERY_URL is not set and no URL could be derived from OTEL_EXPORTER_OTLP_ENDPOINT"}), nil
	}
	body, err := fetchPlatformTrace(ctx, baseURL, traceID)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return jsonResult(map[string]any{"trace_id": traceID, "jaeger_query_url": baseURL, "response": body}), nil
}

type platformListPodsTool struct {
	platformAdminBaseTool
	platformAdminToolBase
}

func (t *platformListPodsTool) Name() string { return "platform_list_pods" }
func (t *platformListPodsTool) Description() string {
	return "List Kubernetes pods for platform introspection. Read-only; available only on Kubernetes-admin runs."
}
func (t *platformListPodsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "namespace": {"type": "string", "description": "Namespace to list. Defaults to this run's namespace unless all_namespaces is true."},
    "all_namespaces": {"type": "boolean", "description": "List pods across all namespaces."},
    "label_selector": {"type": "string", "description": "Kubernetes label selector."},
    "limit": {"type": "integer", "description": "Maximum pods to return (default 50, max 200)."}
  }
}`)
}

type listPodsInput struct {
	Namespace     string `json:"namespace"`
	AllNamespaces bool   `json:"all_namespaces"`
	LabelSelector string `json:"label_selector"`
	Limit         int    `json:"limit"`
}

type podSummary struct {
	Namespace  string            `json:"namespace"`
	Name       string            `json:"name"`
	Phase      corev1.PodPhase   `json:"phase"`
	NodeName   string            `json:"node_name,omitempty"`
	PodIP      string            `json:"pod_ip,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	StartedAt  int64             `json:"started_at_unix,omitempty"`
	Containers []string          `json:"containers,omitempty"`
}

func (t *platformListPodsTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in listPodsInput
	if err := json.Unmarshal(input, &in); err != nil && len(input) > 0 {
		return errorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	limit := clampLimit(in.Limit)
	namespace := metav1.NamespaceAll
	if !in.AllNamespaces {
		namespace = t.namespaceOrCurrent(in.Namespace)
		if namespace == "" {
			return errorResult("namespace is required unless all_namespaces is true"), nil
		}
	}
	pods, err := t.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: strings.TrimSpace(in.LabelSelector)})
	if err != nil {
		return errorResult(fmt.Sprintf("list pods: %v", err)), nil
	}
	out := make([]podSummary, 0, minInt(len(pods.Items), limit))
	for i := range pods.Items {
		if len(out) >= limit {
			break
		}
		pod := pods.Items[i]
		containers := make([]string, 0, len(pod.Spec.Containers))
		for _, c := range pod.Spec.Containers {
			containers = append(containers, c.Name)
		}
		entry := podSummary{Namespace: pod.Namespace, Name: pod.Name, Phase: pod.Status.Phase, NodeName: pod.Spec.NodeName, PodIP: pod.Status.PodIP, Labels: pod.Labels, Containers: containers}
		if pod.Status.StartTime != nil {
			entry.StartedAt = pod.Status.StartTime.Unix()
		}
		out = append(out, entry)
	}
	return jsonResult(map[string]any{"pods": out, "truncated": len(pods.Items) > len(out)}), nil
}

type platformPodLogsTool struct {
	platformAdminBaseTool
	platformAdminToolBase
}

func (t *platformPodLogsTool) Name() string { return "platform_pod_logs" }
func (t *platformPodLogsTool) Description() string {
	return "Read recent logs from a Kubernetes pod. Read-only; available only on Kubernetes-admin runs."
}
func (t *platformPodLogsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "namespace": {"type": "string", "description": "Pod namespace. Defaults to this run's namespace."},
    "name": {"type": "string", "description": "Pod name."},
    "container": {"type": "string", "description": "Container name; optional when the pod has one container."},
    "tail_lines": {"type": "integer", "description": "Number of log lines to fetch (default 200, max 2000)."}
  },
  "required": ["name"]
}`)
}

type podLogsInput struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Container string `json:"container"`
	TailLines int64  `json:"tail_lines"`
}

func (t *platformPodLogsTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in podLogsInput
	if err := json.Unmarshal(input, &in); err != nil {
		return errorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return errorResult("name is required"), nil
	}
	namespace := t.namespaceOrCurrent(in.Namespace)
	if namespace == "" {
		return errorResult("namespace is required"), nil
	}
	tail := in.TailLines
	if tail <= 0 {
		tail = 200
	}
	if tail > 2000 {
		tail = 2000
	}
	req := t.k8sClient.CoreV1().Pods(namespace).GetLogs(name, &corev1.PodLogOptions{Container: strings.TrimSpace(in.Container), TailLines: &tail})
	logs, err := req.DoRaw(ctx)
	if err != nil {
		return errorResult(fmt.Sprintf("get pod logs: %v", err)), nil
	}
	content := string(logs)
	truncated := false
	if len(content) > platformAdminMaxBodyBytes {
		content = content[:platformAdminMaxBodyBytes]
		truncated = true
	}
	return jsonResult(map[string]any{"namespace": namespace, "name": name, "container": strings.TrimSpace(in.Container), "tail_lines": tail, "logs": content, "truncated": truncated}), nil
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return platformAdminDefaultLimit
	}
	if limit > platformAdminMaxLimit {
		return platformAdminMaxLimit
	}
	return limit
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func jsonResult(v any) Result {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errorResult(fmt.Sprintf("marshal result: %v", err))
	}
	return Result{Content: string(b)}
}

func errorResult(message string) Result { return Result{Content: message, IsError: true} }

func platformActivityFallbackError(s3Err, postgresErr error) string {
	if s3Err == nil {
		return postgresErr.Error()
	}
	return fmt.Sprintf("S3 activity failed: %v; PostgreSQL fallback failed: %v", s3Err, postgresErr)
}

func postgresActivityEvents(activity []store.ActivityEvent, limit int) ([]any, bool) {
	truncated := len(activity) > limit
	if truncated {
		activity = activity[:limit]
	}
	events := make([]any, 0, len(activity))
	// GetRecentActivity returns newest-first. Match events.jsonl by presenting
	// the selected window in chronological order.
	for i := len(activity) - 1; i >= 0; i-- {
		event := activity[i]
		decoded := make(map[string]any)
		if len(event.Detail) > 0 {
			var detail any
			if json.Unmarshal(event.Detail, &detail) == nil {
				if object, ok := detail.(map[string]any); ok {
					decoded = object
				} else if detail != nil {
					decoded["detail"] = detail
				}
			} else {
				decoded["raw"] = string(event.Detail)
			}
		}
		if _, ok := decoded["type"]; !ok {
			decoded["type"] = event.EventType
		}
		if event.Summary != "" {
			if _, ok := decoded["message"]; !ok {
				decoded["message"] = event.Summary
			}
		}
		if _, ok := decoded["ts"]; !ok {
			decoded["ts"] = event.CreatedAt
		}
		events = append(events, decoded)
	}
	return events, truncated
}

func fetchS3JSONLEvents(ctx context.Context, s3URL string, limit int) ([]any, bool, error) {
	bucket, key, err := parsePlatformS3URL(s3URL)
	if err != nil {
		return nil, false, err
	}
	region := strings.TrimSpace(os.Getenv("S3_REGION"))
	if region == "" {
		region = "us-east-1"
	}
	opts := s3.Options{Region: region, UsePathStyle: true}
	if ak, sk := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"); ak != "" && sk != "" {
		opts.Credentials = credentials.NewStaticCredentialsProvider(ak, sk, "")
	}
	if endpoint := strings.TrimSpace(os.Getenv("S3_ENDPOINT")); endpoint != "" {
		opts.BaseEndpoint = aws.String(endpoint)
	}
	byteRange := fmt.Sprintf("bytes=-%d", platformAdminMaxBodyBytes)
	out, err := s3.New(opts).GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Range:  aws.String(byteRange),
	})
	if err != nil {
		return nil, false, fmt.Errorf("S3 GetObject %s: %w", s3URL, err)
	}
	defer out.Body.Close()
	body, err := io.ReadAll(io.LimitReader(out.Body, platformAdminMaxBodyBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("read S3 object %s: %w", s3URL, err)
	}
	contentRange := aws.ToString(out.ContentRange)
	if len(body) > platformAdminMaxBodyBytes && strings.TrimSpace(contentRange) == "" {
		return nil, false, fmt.Errorf("S3 endpoint ignored suffix range for %s", s3URL)
	}
	bodyTruncated := s3ContentRangeStartsAfterZero(contentRange)
	if len(body) > platformAdminMaxBodyBytes {
		body = body[len(body)-platformAdminMaxBodyBytes:]
		bodyTruncated = true
	}
	lines := strings.Split(string(body), "\n")
	if bodyTruncated && len(lines) > 0 {
		// A suffix range can begin in the middle of an event.
		lines = lines[1:]
	}
	events, truncated := decodeRecentJSONLLines(lines, limit, bodyTruncated)
	return events, truncated, nil
}

func s3ContentRangeStartsAfterZero(contentRange string) bool {
	contentRange = strings.TrimSpace(contentRange)
	return contentRange != "" && !strings.HasPrefix(contentRange, "bytes 0-")
}

func decodeRecentJSONLLines(lines []string, limit int, sourceTruncated bool) ([]any, bool) {
	decodedLines := make([]any, 0, minInt(len(lines), limit+1))
	for i := len(lines) - 1; i >= 0 && len(decodedLines) <= limit; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			decoded = map[string]any{"raw": line, "parse_error": err.Error()}
		}
		decodedLines = append(decodedLines, decoded)
	}
	truncated := sourceTruncated || len(decodedLines) > limit
	if len(decodedLines) > limit {
		decodedLines = decodedLines[:limit]
	}
	for left, right := 0, len(decodedLines)-1; left < right; left, right = left+1, right-1 {
		decodedLines[left], decodedLines[right] = decodedLines[right], decodedLines[left]
	}
	return decodedLines, truncated
}

func parsePlatformS3URL(s3URL string) (bucket, key string, err error) {
	path := strings.TrimPrefix(strings.TrimSpace(s3URL), "s3://")
	if path == s3URL || path == "" {
		return "", "", fmt.Errorf("invalid S3 URL %q (must start with s3://)", s3URL)
	}
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid S3 URL %q (missing bucket or key)", s3URL)
	}
	return parts[0], parts[1], nil
}

func derivePlatformJaegerQueryURL() string {
	if endpoint := strings.TrimSpace(os.Getenv("JAEGER_QUERY_URL")); endpoint != "" {
		return strings.TrimRight(endpoint, "/")
	}
	host := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if host == "" {
		return ""
	}
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+len("://"):]
	}
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		return ""
	}
	return "http://" + host + ":16686"
}

func fetchPlatformTrace(ctx context.Context, baseURL, traceID string) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/traces/"+traceID, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jaeger request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, platformAdminMaxBodyBytes+1))
	truncated := len(body) > platformAdminMaxBodyBytes
	if truncated {
		body = body[:platformAdminMaxBodyBytes]
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jaeger returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return map[string]any{"raw": string(body), "truncated": truncated}, nil
	}
	if truncated {
		return map[string]any{"data": decoded, "truncated": true}, nil
	}
	return decoded, nil
}
