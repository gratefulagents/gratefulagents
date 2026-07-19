package agentrun

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	teamParentLabel           = "platform.gratefulagents.dev/team-parent"
	teamStepLabel             = "platform.gratefulagents.dev/team-step"
	teamTaskLabel             = "platform.gratefulagents.dev/team-task"
	teamRoleLabel             = "platform.gratefulagents.dev/team-role"
	childAutonomousAnnotation = "platform.gratefulagents.dev/child-autonomous"

	defaultPlanArtifactKey = "plan.md"
)

var (
	ErrTeamStepNotFound   = errors.New("team step was not found on parent AgentRun")
	ErrTeamTaskNotFound   = errors.New("team task was not found on parent AgentRun step")
	ErrChildRunNotOwned   = errors.New("child AgentRun does not belong to parent")
	ErrTeamScopeInvalid   = errors.New("wait scope must be parent or children")
	ErrTeamClientRequired = errors.New("team service kubernetes client is required")
	ErrTeamSchemeRequired = errors.New("team service runtime scheme is required")
)

// KubeTeamService implements TeamService against AgentRun resources.
type KubeTeamService struct {
	client       client.Client
	scheme       *runtime.Scheme
	pollInterval time.Duration
	stateStore   store.StateStore // optional — used for Postgres message routing
	mu           sync.Mutex       // serializes child-count-sensitive mutations (CreateChildRun, RetryChildRun)
}

// NewKubeTeamService returns the shared team orchestration implementation used by adapters.
func NewKubeTeamService(c client.Client, scheme *runtime.Scheme) *KubeTeamService {
	return &KubeTeamService{client: c, scheme: scheme, pollInterval: 500 * time.Millisecond}
}

// WithStateStore sets an optional Postgres state store for message routing.
func (s *KubeTeamService) WithStateStore(ss store.StateStore) *KubeTeamService {
	s.stateStore = ss
	return s
}

func (s *KubeTeamService) CreateChildRun(ctx context.Context, req CreateChildRunRequest) (*ChildRunStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	parent, err := s.loadParent(ctx, req.Parent)
	if err != nil {
		return nil, err
	}
	stepName := strings.TrimSpace(req.StepName)
	taskName := strings.TrimSpace(req.TaskName)
	task, err := resolveTeamTaskForCreate(parent, stepName, taskName, req.Instructions)
	if err != nil {
		return nil, err
	}

	// C-04: enforce delegation policy limits (maxChildren, maxDepth).
	if err := s.enforceDelegationLimits(ctx, parent); err != nil {
		return nil, err
	}

	// C-01 + C-03: enforce dependsOn readiness and upstream artifact contracts.
	if err := s.enforceDependencies(ctx, parent, stepName, task); err != nil {
		return nil, err
	}

	childName := buildTeamChildName(parent.Name, stepName, taskName)
	key := client.ObjectKey{Namespace: parent.Namespace, Name: childName}
	existing := &platformv1alpha1.AgentRun{}
	if err := s.client.Get(ctx, key, existing); err == nil {
		if !isOwnedChild(parent, existing) {
			return nil, fmt.Errorf("%w: %s/%s", ErrChildRunNotOwned, existing.Namespace, existing.Name)
		}
		return childRunStatusFromAgentRun(existing), nil
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get child AgentRun %s/%s: %w", key.Namespace, key.Name, err)
	}

	child, childTitle := newChildRunFromParent(parent, stepName, task, childName, req.Instructions, req.Mode)
	if err := ctrl.SetControllerReference(parent, child, s.scheme); err != nil {
		return nil, fmt.Errorf("set child owner reference: %w", err)
	}
	if err := s.client.Create(ctx, child); err != nil {
		if apierrors.IsAlreadyExists(err) {
			if err := s.client.Get(ctx, key, existing); err != nil {
				return nil, fmt.Errorf("get existing child AgentRun %s/%s: %w", key.Namespace, key.Name, err)
			}
			if !isOwnedChild(parent, existing) {
				return nil, fmt.Errorf("%w: %s/%s", ErrChildRunNotOwned, existing.Namespace, existing.Name)
			}
			return childRunStatusFromAgentRun(existing), nil
		}
		return nil, fmt.Errorf("create child AgentRun %s/%s: %w", child.Namespace, child.Name, err)
	}

	// Seed the child's Postgres session with instructions so the child pod
	// has a user message to work on when it starts polling.
	if s.stateStore != nil && childTitle != "" {
		if sess, err := s.stateStore.CreateSession(ctx, child.Name, child.Namespace, "pending", "setup"); err != nil {
			log.Printf("WARN: failed to create Postgres session for child %s: %v", child.Name, err)
		} else if _, err := s.stateStore.AppendMessage(ctx, sess.ID, "user", childTitle, nil); err != nil {
			log.Printf("WARN: failed to seed instructions for child %s: %v", child.Name, err)
		}
	}

	return childRunStatusFromAgentRun(child), nil
}

func (s *KubeTeamService) ListChildRuns(ctx context.Context, req ListChildRunsRequest) (*ListChildRunsResponse, error) {
	parent, err := s.loadParent(ctx, req.Parent)
	if err != nil {
		return nil, err
	}
	children, err := s.listOwnedChildren(ctx, parent)
	if err != nil {
		return nil, err
	}
	resp := &ListChildRunsResponse{}
	for _, child := range children {
		if req.StepName != "" && child.Step != req.StepName {
			continue
		}
		resp.Children = append(resp.Children, *child)
	}
	return resp, nil
}

func (s *KubeTeamService) GetChildRunStatus(ctx context.Context, req GetChildRunStatusRequest) (*ChildRunStatus, error) {
	parent, err := s.loadParent(ctx, req.Parent)
	if err != nil {
		return nil, err
	}
	child, err := s.loadOwnedChild(ctx, parent, req.Child)
	if err != nil {
		return nil, err
	}
	return s.childRunStatusWithReport(ctx, child)
}

func (s *KubeTeamService) GetChildRunArtifact(ctx context.Context, req GetChildRunArtifactRequest) (*ChildRunArtifact, error) {
	parent, err := s.loadParent(ctx, req.Parent)
	if err != nil {
		return nil, err
	}
	child, err := s.loadOwnedChild(ctx, parent, req.Child)
	if err != nil {
		return nil, err
	}
	return s.readChildArtifact(ctx, child, req.Artifact)
}

func (s *KubeTeamService) GetChildRunLogs(ctx context.Context, req GetChildRunStatusRequest) (*ChildRunLogs, error) {
	parent, err := s.loadParent(ctx, req.Parent)
	if err != nil {
		return nil, err
	}
	child, err := s.loadOwnedChild(ctx, parent, req.Child)
	if err != nil {
		return nil, err
	}
	status, err := s.childRunStatusWithReport(ctx, child)
	if err != nil {
		return nil, err
	}
	logs := &ChildRunLogs{
		Status:         *status,
		LastError:      strings.TrimSpace(child.Status.LastError),
		ActivityLogURL: "",
	}
	if s.stateStore != nil {
		if sess, err := s.stateStore.GetSessionByRun(ctx, child.Name, child.Namespace); err == nil {
			if pendingType := strings.TrimSpace(sess.PendingInputType); pendingType != "" {
				logs.UserInputType = pendingType
			}
			logs.PendingQuestion = strings.TrimSpace(sess.PendingQuestion)
			if msgs, err := s.stateStore.GetMessages(ctx, sess.ID); err == nil {
				logs.ConversationTail = tailStoreMessages(msgs, 12)
			}
			if events, err := s.stateStore.GetRecentActivity(ctx, sess.ID, 12); err == nil {
				logs.RecentActivity = recentActivityFromStoreEvents(events)
			}
		}
	}
	if child.Status.Artifacts != nil {
		logs.ActivityLogURL = strings.TrimSpace(child.Status.Artifacts.ActivityLogURL)
	}
	if child.Status.Phase == platformv1alpha1.AgentRunPhaseBlocked {
		logs.CanUnblockWithRetry = true
		logs.SuggestedUnblockAction = "retry_child_run"
	}
	return logs, nil
}

func tailStoreMessages(messages []store.Message, limit int) []platformv1alpha1.AgentRunChatMessage {
	if limit <= 0 || len(messages) == 0 {
		return nil
	}
	start := 0
	if len(messages) > limit {
		start = len(messages) - limit
	}
	out := make([]platformv1alpha1.AgentRunChatMessage, 0, len(messages)-start)
	for _, msg := range messages[start:] {
		out = append(out, platformv1alpha1.AgentRunChatMessage{
			Role:      msg.Role,
			Content:   msg.Content,
			Timestamp: metav1.NewTime(msg.CreatedAt),
		})
	}
	return out
}

func recentActivityFromStoreEvents(events []store.ActivityEvent) []platformv1alpha1.AgentRunActivity {
	if len(events) == 0 {
		return nil
	}
	out := make([]platformv1alpha1.AgentRunActivity, 0, len(events))
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		out = append(out, platformv1alpha1.AgentRunActivity{
			Timestamp: metav1.NewTime(ev.CreatedAt),
			EventType: ev.EventType,
			Summary:   ev.Summary,
		})
	}
	return out
}

func (s *KubeTeamService) GetParentTeamStatus(ctx context.Context, req GetParentTeamStatusRequest) (*platformv1alpha1.AgentRunTeamSummary, error) {
	parent, err := s.loadParent(ctx, req.Parent)
	if err != nil {
		return nil, err
	}
	children, err := s.listOwnedChildren(ctx, parent)
	if err != nil {
		return nil, err
	}

	summary := &platformv1alpha1.AgentRunTeamSummary{}
	if parent.Status.TeamSummary != nil {
		summary = parent.Status.TeamSummary.DeepCopy()
	}
	if summary.CurrentStep == "" {
		summary.CurrentStep = strings.TrimSpace(parent.Status.CurrentStep)
	}
	if summary.ApprovalState == "" {
		summary.ApprovalState = deriveApprovalState(parent)
	}
	if summary.BlockedReason == "" && parent.Status.Queue != nil {
		summary.BlockedReason = parent.Status.Queue.BlockedReason
	}
	applyChildCounts(summary, children)
	return summary, nil
}

func (s *KubeTeamService) WaitForRunChange(ctx context.Context, req WaitForRunChangeRequest) (*WaitForRunChangeResponse, error) {
	parent, err := s.loadParent(ctx, req.Parent)
	if err != nil {
		return nil, err
	}
	interval := s.pollInterval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	deadline := time.Now().Add(defaultWaitTimeout(req.TimeoutMS))
	initialKey, initialPhase, err := s.snapshotScope(ctx, parent, normalizeWaitScope(req.Scope))
	if err != nil {
		return nil, err
	}
	if phaseMatches(initialPhase, req.UntilPhases) {
		return &WaitForRunChangeResponse{Changed: true, RunName: initialKey, Phase: initialPhase}, nil
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			key, phase, err := s.snapshotScope(ctx, parent, normalizeWaitScope(req.Scope))
			if err != nil {
				return nil, err
			}
			if phaseMatches(phase, req.UntilPhases) || key != initialKey || phase != initialPhase {
				return &WaitForRunChangeResponse{Changed: true, RunName: key, Phase: phase}, nil
			}
			if time.Now().After(deadline) {
				return &WaitForRunChangeResponse{Changed: false, RunName: key, Phase: phase}, nil
			}
		}
	}
}

func (s *KubeTeamService) CancelChildRun(ctx context.Context, req CancelChildRunRequest) (*ChildRunStatus, error) {
	parent, err := s.loadParent(ctx, req.Parent)
	if err != nil {
		return nil, err
	}
	child, err := s.loadOwnedChild(ctx, parent, req.Child)
	if err != nil {
		return nil, err
	}
	if isTerminalPhase(child.Status.Phase) {
		return childRunStatusFromAgentRun(child), nil
	}

	before := child.DeepCopy()
	now := metav1.Now()
	child.Status.Phase = platformv1alpha1.AgentRunPhaseCancelled
	child.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Cancelled"}
	child.Status.CompletedAt = &now
	if err := s.client.Status().Patch(ctx, child, client.MergeFrom(before)); err != nil {
		return nil, fmt.Errorf("cancel child AgentRun %s/%s: %w", child.Namespace, child.Name, err)
	}
	return childRunStatusFromAgentRun(child), nil
}

func (s *KubeTeamService) RetryChildRun(ctx context.Context, req RetryChildRunRequest) (*ChildRunStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	parent, err := s.loadParent(ctx, req.Parent)
	if err != nil {
		return nil, err
	}
	child, err := s.loadOwnedChild(ctx, parent, req.Child)
	if err != nil {
		return nil, err
	}

	// C-02: enforce maxRetries budget from the task spec.
	if err := enforceRetryBudget(parent, child); err != nil {
		return nil, err
	}

	before := child.DeepCopy()
	child.Status.Phase = platformv1alpha1.AgentRunPhasePending
	child.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Queued"}
	child.Status.Sandbox = nil
	child.Status.Artifacts = nil
	child.Status.CurrentStep = ""
	child.Status.LastError = ""
	child.Status.StartedAt = nil
	child.Status.CompletedAt = nil
	child.Status.RetryCount++
	if err := s.client.Status().Patch(ctx, child, client.MergeFrom(before)); err != nil {
		return nil, fmt.Errorf("retry child AgentRun %s/%s: %w", child.Namespace, child.Name, err)
	}
	return childRunStatusFromAgentRun(child), nil
}

func (s *KubeTeamService) SendMessageToChild(ctx context.Context, req SendMessageToChildRequest) (*ChildRunStatus, error) {
	parent, err := s.loadParent(ctx, req.Parent)
	if err != nil {
		return nil, err
	}
	child, err := s.loadOwnedChild(ctx, parent, req.Child)
	if err != nil {
		return nil, err
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		return nil, ErrChildMessageRequired
	}

	if s.stateStore == nil {
		return nil, fmt.Errorf("stateStore required to send messages to child %s/%s", child.Namespace, child.Name)
	}

	// Find existing session or create one (child may not have started yet).
	sess, err := s.stateStore.GetSessionByRun(ctx, child.Name, child.Namespace)
	if err != nil {
		sess, err = s.stateStore.CreateSession(ctx, child.Name, child.Namespace, "pending", "setup")
		if err != nil {
			return nil, fmt.Errorf("create session for child %s/%s: %w", child.Namespace, child.Name, err)
		}
	}
	if _, err := s.stateStore.AppendMessage(ctx, sess.ID, "user", message, nil); err != nil {
		return nil, fmt.Errorf("write message to child session %s/%s: %w", child.Namespace, child.Name, err)
	}

	// Clear the pending question in Postgres so the child's PollForUserMessage
	// picks up the new message and re-enters the agent loop.
	if err := s.stateStore.ClearPendingQuestion(ctx, sess.ID, "running"); err != nil {
		log.Printf("WARN: failed to clear pending question for child %s/%s: %v", child.Namespace, child.Name, err)
	}

	// Re-read child to return fresh status after unblocking.
	refreshed := &platformv1alpha1.AgentRun{}
	if err := s.client.Get(ctx, client.ObjectKeyFromObject(child), refreshed); err == nil {
		return childRunStatusFromAgentRun(refreshed), nil
	}
	return childRunStatusFromAgentRun(child), nil
}

func (s *KubeTeamService) GetApprovalStatus(ctx context.Context, req GetApprovalStatusRequest) (*ApprovalStatus, error) {
	parent, err := s.loadParent(ctx, req.Parent)
	if err != nil {
		return nil, err
	}
	status := deriveApprovalState(parent)
	if parent.Status.TeamSummary != nil && strings.TrimSpace(parent.Status.TeamSummary.ApprovalState) != "" {
		status = parent.Status.TeamSummary.ApprovalState
	}
	return &ApprovalStatus{State: status}, nil
}

func (s *KubeTeamService) HasActiveChildren(ctx context.Context, ref ParentRunRef) (bool, error) {
	parent, err := s.loadParent(ctx, ref)
	if err != nil {
		return false, err
	}
	children, err := s.listOwnedChildren(ctx, parent)
	if err != nil {
		return false, err
	}
	for _, child := range children {
		phase := strings.ToLower(strings.TrimSpace(child.Phase))
		if phase != "succeeded" && phase != "failed" && phase != "cancelled" {
			return true, nil
		}
	}
	return false, nil
}

func (s *KubeTeamService) loadParent(ctx context.Context, ref ParentRunRef) (*platformv1alpha1.AgentRun, error) {
	if s == nil || s.client == nil {
		return nil, ErrTeamClientRequired
	}
	if s.scheme == nil {
		return nil, ErrTeamSchemeRequired
	}
	key := client.ObjectKey{Namespace: strings.TrimSpace(ref.Namespace), Name: strings.TrimSpace(ref.Name)}
	if err := ValidateRuntimeParentBinding(ParentRunRef{Namespace: key.Namespace, Name: key.Name}); err != nil {
		return nil, err
	}
	parent := &platformv1alpha1.AgentRun{}
	if err := s.client.Get(ctx, key, parent); err != nil {
		return nil, fmt.Errorf("get parent AgentRun %s/%s: %w", key.Namespace, key.Name, err)
	}
	if err := ValidateRuntimeParentUID(parent); err != nil {
		return nil, err
	}
	if err := ValidateTeamParent(parent); err != nil {
		return nil, err
	}
	return parent, nil
}

func (s *KubeTeamService) listOwnedChildren(ctx context.Context, parent *platformv1alpha1.AgentRun) ([]*ChildRunStatus, error) {
	runs := &platformv1alpha1.AgentRunList{}
	if err := s.client.List(ctx, runs, client.InNamespace(parent.Namespace)); err != nil {
		return nil, fmt.Errorf("list child AgentRuns for %s/%s: %w", parent.Namespace, parent.Name, err)
	}
	children := make([]*ChildRunStatus, 0, len(runs.Items))
	for i := range runs.Items {
		child := &runs.Items[i]
		if !isOwnedChild(parent, child) {
			continue
		}
		children = append(children, childRunStatusFromAgentRun(child))
	}
	sort.Slice(children, func(i, j int) bool {
		if children[i].Step == children[j].Step {
			return children[i].Name < children[j].Name
		}
		return children[i].Step < children[j].Step
	})
	return children, nil
}

func (s *KubeTeamService) loadOwnedChild(ctx context.Context, parent *platformv1alpha1.AgentRun, ref ChildRunRef) (*platformv1alpha1.AgentRun, error) {
	child := &platformv1alpha1.AgentRun{}
	key := client.ObjectKey{Namespace: strings.TrimSpace(ref.Namespace), Name: strings.TrimSpace(ref.Name)}
	if key.Namespace != parent.Namespace {
		return nil, fmt.Errorf("%w: child=%s/%s parent=%s/%s", ErrChildCrossNamespace, key.Namespace, key.Name, parent.Namespace, parent.Name)
	}
	if err := s.client.Get(ctx, key, child); err != nil {
		return nil, fmt.Errorf("get child AgentRun %s/%s: %w", key.Namespace, key.Name, err)
	}
	if !isOwnedChild(parent, child) {
		return nil, fmt.Errorf("%w: %s/%s", ErrChildRunNotOwned, child.Namespace, child.Name)
	}
	return child, nil
}

func (s *KubeTeamService) snapshotScope(ctx context.Context, parent *platformv1alpha1.AgentRun, scope string) (string, string, error) {
	switch scope {
	case "parent":
		fresh := &platformv1alpha1.AgentRun{}
		key := client.ObjectKeyFromObject(parent)
		if err := s.client.Get(ctx, key, fresh); err != nil {
			return "", "", fmt.Errorf("refresh parent AgentRun %s/%s: %w", key.Namespace, key.Name, err)
		}
		return fresh.Name, string(fresh.Status.Phase), nil
	case "children":
		children, err := s.listOwnedChildren(ctx, parent)
		if err != nil {
			return "", "", err
		}
		if len(children) == 0 {
			return "", "", nil
		}
		parts := make([]string, 0, len(children))
		for _, child := range children {
			parts = append(parts, child.Name+":"+child.Phase)
		}
		joined := strings.Join(parts, ",")
		if len(joined) <= 63 {
			return joined, children[0].Phase, nil
		}
		sum := sha1.Sum([]byte(joined))
		return hex.EncodeToString(sum[:]), children[0].Phase, nil
	default:
		return "", "", ErrTeamScopeInvalid
	}
}

func normalizeWaitScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", "parent", "run", "self":
		return "parent"
	case "children", "child", "child_runs":
		return "children"
	default:
		return strings.ToLower(strings.TrimSpace(scope))
	}
}

func defaultWaitTimeout(timeoutMS int64) time.Duration {
	if timeoutMS <= 0 {
		return 30 * time.Second
	}
	return time.Duration(timeoutMS) * time.Millisecond
}

func phaseMatches(phase string, until []string) bool {
	if len(until) == 0 || strings.TrimSpace(phase) == "" {
		return false
	}
	for _, candidate := range until {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(phase)) {
			return true
		}
	}
	return false
}

func findTeamTask(parent *platformv1alpha1.AgentRun, stepName, taskName string) (*platformv1alpha1.AgentRunTeamStep, *platformv1alpha1.AgentRunTeamTask, error) {
	if parent == nil || parent.Spec.Team == nil {
		return nil, nil, ErrTeamSpecRequired
	}
	for i := range parent.Spec.Team.Steps {
		step := &parent.Spec.Team.Steps[i]
		if step.Name != stepName {
			continue
		}
		for j := range step.Tasks {
			task := &step.Tasks[j]
			if task.Name == taskName {
				return step, task, nil
			}
		}
		return nil, nil, fmt.Errorf("%w: %s/%s", ErrTeamTaskNotFound, stepName, taskName)
	}
	return nil, nil, fmt.Errorf("%w: %s", ErrTeamStepNotFound, stepName)
}

func resolveTeamTaskForCreate(parent *platformv1alpha1.AgentRun, stepName, taskName, instructions string) (platformv1alpha1.AgentRunTeamTask, error) {
	if parent == nil {
		return platformv1alpha1.AgentRunTeamTask{}, ErrParentRunRequired
	}
	// If team spec exists, try matching a declared step/task first.
	if parent.Spec.Team != nil {
		_, task, err := findTeamTask(parent, stepName, taskName)
		if err == nil {
			return *task, nil
		}
		// Step/task not found in spec — fall through to ad-hoc creation.
		// The agent dynamically decides what children to create.
	}
	if !allowsAdHocTeamParent(parent) {
		return platformv1alpha1.AgentRunTeamTask{}, ErrTeamSpecRequired
	}
	return platformv1alpha1.AgentRunTeamTask{
		Name:      taskName,
		Role:      "worker",
		Objective: strings.TrimSpace(instructions),
	}, nil
}

func newChildRunFromParent(parent *platformv1alpha1.AgentRun, stepName string, task platformv1alpha1.AgentRunTeamTask, childName, instructions, mode string) (*platformv1alpha1.AgentRun, string) {
	spec := parent.Spec
	spec.ExecutionMode = platformv1alpha1.ExecutionModeLinear
	spec.Team = nil

	// Delegated children share the same finish-gated autonomous pacing. The mode
	// argument is retained on the delegation contract for compatibility.
	_ = mode
	spec.WorkflowMode = platformv1alpha1.WorkflowModeAuto
	if task.RuntimeProfileRef != nil {
		spec.RuntimeProfileRef = task.RuntimeProfileRef.DeepCopy()
	}
	title := strings.TrimSpace(instructions)
	if title == "" {
		title = strings.TrimSpace(task.Objective)
	}
	if title == "" {
		title = task.Name
	}
	isAuto := spec.WorkflowMode == platformv1alpha1.WorkflowModeAuto
	if isAuto {
		title = decorateAutonomousChildInstructions(title)
	}
	spec.SpecArtifactRef = parent.Spec.SpecArtifactRef
	annotations := map[string]string{}
	if isAuto {
		annotations[childAutonomousAnnotation] = "true"
	}
	// Child runs inherit the git settings snapshotted onto their parent.
	for _, key := range []string{
		platformv1alpha1.GitAuthorNameAnnotation,
		platformv1alpha1.GitAuthorEmailAnnotation,
	} {
		if value := strings.TrimSpace(parent.Annotations[key]); value != "" {
			annotations[key] = value
		}
	}
	return &platformv1alpha1.AgentRun{
		TypeMeta: metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        childName,
			Namespace:   parent.Namespace,
			Finalizers:  []string{platformv1alpha1.AgentRunCleanupFinalizer},
			Annotations: annotations,
			Labels: map[string]string{
				teamParentLabel: parent.Name,
				teamStepLabel:   sanitizeLabelValue(stepName),
				teamTaskLabel:   sanitizeLabelValue(task.Name),
				teamRoleLabel:   sanitizeLabelValue(task.Role),
			},
		},
		Spec: spec,
	}, title
}

func decorateAutonomousChildInstructions(task string) string {
	task = strings.TrimSpace(task)
	prefix := strings.TrimSpace(`
Autonomous child-run contract:
- You are completely autonomous for this delegated task.
- Do not ask clarifying questions.
- Do not request approval.
- Make reasonable assumptions and execute end-to-end.
- Return the requested report and/or create required artifacts.
`)
	if task == "" {
		return prefix
	}
	return prefix + "\n\nTask:\n" + task
}

func (s *KubeTeamService) readChildArtifact(ctx context.Context, child *platformv1alpha1.AgentRun, artifact string) (*ChildRunArtifact, error) {
	kind, ref, url, err := resolveChildArtifactReference(child, artifact)
	if err != nil {
		return nil, err
	}
	if ref == nil && strings.TrimSpace(url) != "" {
		return &ChildRunArtifact{
			Artifact: kind,
			Kind:     "URL",
			URL:      url,
			Content:  url,
		}, nil
	}
	if ref == nil {
		return nil, ErrChildArtifactNotFound
	}
	if !strings.EqualFold(strings.TrimSpace(ref.Kind), "ConfigMap") {
		return nil, fmt.Errorf("%w: unsupported artifact kind %q", ErrChildArtifactNotFound, ref.Kind)
	}

	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: child.Namespace, Name: strings.TrimSpace(ref.Name)}
	if key.Name == "" {
		return nil, fmt.Errorf("%w: missing ConfigMap name", ErrChildArtifactNotFound)
	}
	if err := s.client.Get(ctx, key, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: ConfigMap %s/%s", ErrChildArtifactNotFound, key.Namespace, key.Name)
		}
		return nil, fmt.Errorf("get child artifact ConfigMap %s/%s: %w", key.Namespace, key.Name, err)
	}

	refKey := strings.TrimSpace(ref.Key)
	if refKey == "" {
		refKey = defaultArtifactKey(kind)
	}
	if refKey == "" && len(cm.Data) == 1 {
		for onlyKey := range cm.Data {
			refKey = onlyKey
		}
	}
	content, ok := cm.Data[refKey]
	if !ok {
		return nil, fmt.Errorf("%w: key %q missing in ConfigMap %s/%s", ErrChildArtifactNotFound, refKey, key.Namespace, key.Name)
	}
	return &ChildRunArtifact{
		Artifact: kind,
		Kind:     "ConfigMap",
		Name:     key.Name,
		Key:      refKey,
		Content:  strings.TrimSpace(content),
	}, nil
}

func resolveChildArtifactReference(child *platformv1alpha1.AgentRun, artifact string) (string, *platformv1alpha1.ArtifactRef, string, error) {
	if child == nil {
		return "", nil, "", ErrParentRunRequired
	}
	normalized := normalizeArtifactSelector(artifact)
	if normalized == "" {
		normalized = "output"
	}

	switch normalized {
	case "output":
		return resolveChildOutputArtifactReference(child)
	case "diff":
		if child.Status.Artifacts == nil || strings.TrimSpace(child.Status.Artifacts.DiffURL) == "" {
			return "", nil, "", fmt.Errorf("%w: diff", ErrChildArtifactNotFound)
		}
		return "diff", nil, strings.TrimSpace(child.Status.Artifacts.DiffURL), nil
	default:
		return "", nil, "", fmt.Errorf("%w: %s", ErrChildArtifactNotFound, normalized)
	}
}

func normalizeArtifactSelector(artifact string) string {
	return strings.ToLower(strings.TrimSpace(artifact))
}

func defaultArtifactKey(artifact string) string {
	switch artifact {
	case "output":
		return defaultPlanArtifactKey
	default:
		return ""
	}
}

func resolveChildOutputArtifactReference(child *platformv1alpha1.AgentRun) (string, *platformv1alpha1.ArtifactRef, string, error) {
	if child == nil {
		return "", nil, "", ErrParentRunRequired
	}
	if child.Status.Artifacts != nil && child.Status.Artifacts.PlanRef != nil {
		return "output", artifactRefWithDefaultKey(child.Status.Artifacts.PlanRef, defaultPlanArtifactKey), "", nil
	}
	return "", nil, "", fmt.Errorf("%w: output", ErrChildArtifactNotFound)
}

func artifactRefWithDefaultKey(in *platformv1alpha1.ArtifactRef, defaultKey string) *platformv1alpha1.ArtifactRef {
	if in == nil {
		return nil
	}
	out := *in
	if strings.TrimSpace(out.Key) == "" {
		out.Key = strings.TrimSpace(defaultKey)
	}
	return &out
}

func childRunStatusFromAgentRun(run *platformv1alpha1.AgentRun) *ChildRunStatus {
	if run == nil {
		return nil
	}
	status := &ChildRunStatus{
		Name:          run.Name,
		Namespace:     run.Namespace,
		Step:          run.Labels[teamStepLabel],
		Role:          run.Labels[teamRoleLabel],
		Phase:         string(run.Status.Phase),
		CurrentTask:   run.Status.CurrentStep,
		CurrentWorker: "",
	}
	if run.Status.Queue != nil {
		status.BlockReason = run.Status.Queue.BlockedReason
	}
	return status
}

func (s *KubeTeamService) childRunStatusWithReport(ctx context.Context, run *platformv1alpha1.AgentRun) (*ChildRunStatus, error) {
	status := childRunStatusFromAgentRun(run)
	if run == nil {
		return status, nil
	}
	artifact, err := s.readChildArtifact(ctx, run, "output")
	if err != nil {
		if errors.Is(err, ErrChildArtifactNotFound) {
			return status, nil
		}
		return nil, err
	}
	status.Report = strings.TrimSpace(artifact.Content)
	return status, nil
}

func applyChildCounts(summary *platformv1alpha1.AgentRunTeamSummary, children []*ChildRunStatus) {
	if summary == nil {
		return
	}
	summary.TotalChildren = int32(len(children))
	summary.PendingChildren = 0
	summary.RunningChildren = 0
	summary.SucceededChildren = 0
	summary.FailedChildren = 0
	summary.CancelledChildren = 0
	for _, child := range children {
		switch child.Phase {
		case string(platformv1alpha1.AgentRunPhasePending), string(platformv1alpha1.AgentRunPhaseAdmitted), string(platformv1alpha1.AgentRunPhaseProvisioning):
			summary.PendingChildren++
		case string(platformv1alpha1.AgentRunPhaseRunning), string(platformv1alpha1.AgentRunPhaseBlocked), string(platformv1alpha1.AgentRunPhaseWaitingApproval):
			summary.RunningChildren++
		case string(platformv1alpha1.AgentRunPhaseSucceeded):
			summary.SucceededChildren++
		case string(platformv1alpha1.AgentRunPhaseFailed):
			summary.FailedChildren++
		case string(platformv1alpha1.AgentRunPhaseCancelled):
			summary.CancelledChildren++
		}
	}
}

func deriveApprovalState(parent *platformv1alpha1.AgentRun) string {
	if parent == nil {
		return "unknown"
	}
	if parent.Status.TeamSummary != nil && strings.TrimSpace(parent.Status.TeamSummary.ApprovalState) != "" {
		return parent.Status.TeamSummary.ApprovalState
	}
	if parent.Status.Phase == platformv1alpha1.AgentRunPhaseWaitingApproval {
		return "waiting"
	}
	if parent.Spec.Team != nil && parent.Spec.Team.CompletionPolicy != nil && parent.Spec.Team.CompletionPolicy.RequireApproval {
		return "pending"
	}
	return "not_required"
}

func isOwnedChild(parent, child *platformv1alpha1.AgentRun) bool {
	if parent == nil || child == nil {
		return false
	}
	if child.Namespace != parent.Namespace {
		return false
	}
	if child.Labels[teamParentLabel] == parent.Name {
		return true
	}
	for _, ownerRef := range child.OwnerReferences {
		if ownerRef.APIVersion == platformv1alpha1.GroupVersion.String() && ownerRef.Kind == "AgentRun" && ownerRef.Name == parent.Name && ownerRef.UID == parent.UID {
			return true
		}
	}
	return false
}

func buildTeamChildName(parentName, stepName, taskName string) string {
	parts := []string{sanitizeName(parentName), sanitizeName(stepName), sanitizeName(taskName)}
	base := strings.Trim(strings.Join(parts, "-"), "-")
	if base == "" {
		base = "team-child"
	}
	if len(base) <= 63 {
		return base
	}
	sum := sha1.Sum([]byte(base))
	hash := hex.EncodeToString(sum[:])[:8]
	trimmed := strings.Trim(base[:54], "-")
	if trimmed == "" {
		trimmed = "team-child"
	}
	return trimmed + "-" + hash
}

func sanitizeName(in string) string {
	in = strings.ToLower(strings.TrimSpace(in))
	var b strings.Builder
	lastDash := false
	for _, r := range in {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// sanitizeLabelValue makes a string safe for use as a K8s label value.
// Valid: alphanumeric, '-', '_', '.'; max 63 chars; must start/end alphanumeric.
func sanitizeLabelValue(in string) string {
	in = strings.TrimSpace(in)
	var b strings.Builder
	for _, r := range in {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if len(out) > 63 {
		out = out[:63]
	}
	out = strings.Trim(out, "-_.")
	return out
}

func isTerminalPhase(phase platformv1alpha1.AgentRunPhase) bool {
	switch phase {
	case platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhaseCancelled:
		return true
	default:
		return false
	}
}

// enforceDelegationLimits checks maxChildren and maxDepth from the parent's delegation policy.
// Falls back to mode snapshot constraints if delegation policy is not set.
func (s *KubeTeamService) enforceDelegationLimits(ctx context.Context, parent *platformv1alpha1.AgentRun) error {
	var maxChildren, maxDepth int32

	if parent.Spec.Team != nil && parent.Spec.Team.DelegationPolicy != nil {
		maxChildren = parent.Spec.Team.DelegationPolicy.MaxChildren
		maxDepth = parent.Spec.Team.DelegationPolicy.MaxDepth
	}

	// Fall back to mode snapshot constraints.
	if maxChildren == 0 && parent.Status.ModeSnapshot != nil && parent.Status.ModeSnapshot.Constraints != nil {
		maxChildren = parent.Status.ModeSnapshot.Constraints.MaxConcurrentSubAgents
	}

	// maxChildren: count existing owned children and reject if at limit.
	if maxChildren > 0 {
		children, err := s.listOwnedChildren(ctx, parent)
		if err != nil {
			return err
		}
		if int32(len(children)) >= maxChildren {
			return fmt.Errorf("%w: %d children exist, limit is %d", ErrMaxChildrenExceeded, len(children), maxChildren)
		}
	}

	// maxDepth: check the nesting level of the parent itself.
	if maxDepth > 0 {
		depth := parentNestingDepth(parent)
		if depth >= maxDepth {
			return fmt.Errorf("%w: parent depth %d, limit is %d", ErrMaxDepthExceeded, depth, maxDepth)
		}
	}

	return nil
}

// parentNestingDepth returns how many levels of team-parent ownership exist above the parent.
func parentNestingDepth(parent *platformv1alpha1.AgentRun) int32 {
	if parent == nil {
		return 0
	}
	if _, ok := parent.Labels[teamParentLabel]; ok {
		return 1
	}
	return 0
}

// enforceDependencies checks dependsOn readiness and upstream artifact contracts.
func (s *KubeTeamService) enforceDependencies(ctx context.Context, parent *platformv1alpha1.AgentRun, stepName string, task platformv1alpha1.AgentRunTeamTask) error {
	if len(task.DependsOn) == 0 {
		return nil
	}
	if parent.Spec.Team == nil {
		return nil
	}

	for _, depName := range task.DependsOn {
		depName = strings.TrimSpace(depName)
		if depName == "" {
			continue
		}

		// Find the dependency task spec to check its artifact contract.
		depTask, depChildName, err := s.resolveDependencyTask(parent, stepName, depName)
		if err != nil {
			return err
		}

		// C-01: check the dependency child run exists and is Succeeded.
		depChild := &platformv1alpha1.AgentRun{}
		depKey := client.ObjectKey{Namespace: parent.Namespace, Name: depChildName}
		if err := s.client.Get(ctx, depKey, depChild); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("%w: %q has not been created yet", ErrDependencyNotReady, depName)
			}
			return fmt.Errorf("get dependency child %s/%s: %w", depKey.Namespace, depKey.Name, err)
		}
		if depChild.Status.Phase != platformv1alpha1.AgentRunPhaseSucceeded {
			return fmt.Errorf("%w: %q is in phase %s, not Succeeded", ErrDependencyNotReady, depName, depChild.Status.Phase)
		}

		// C-03: if the upstream task declares an artifact contract, verify artifact exists.
		if depTask != nil && strings.TrimSpace(depTask.ArtifactContract) != "" {
			if err := s.checkArtifactContract(ctx, depChild, depTask.ArtifactContract); err != nil {
				return fmt.Errorf("%w: dependency %q artifact contract %q: %v", ErrArtifactContractNotMet, depName, depTask.ArtifactContract, err)
			}
		}
	}
	return nil
}

// resolveDependencyTask finds a task by name within the same step, or across all steps.
func (s *KubeTeamService) resolveDependencyTask(parent *platformv1alpha1.AgentRun, stepName, depName string) (*platformv1alpha1.AgentRunTeamTask, string, error) {
	if parent.Spec.Team == nil {
		return nil, "", nil
	}

	// First look in the same step.
	for i := range parent.Spec.Team.Steps {
		step := &parent.Spec.Team.Steps[i]
		if step.Name != stepName {
			continue
		}
		for j := range step.Tasks {
			task := &step.Tasks[j]
			if task.Name == depName {
				childName := buildTeamChildName(parent.Name, stepName, depName)
				return task, childName, nil
			}
		}
	}

	// Then look across all steps (cross-step dependencies).
	for i := range parent.Spec.Team.Steps {
		step := &parent.Spec.Team.Steps[i]
		for j := range step.Tasks {
			task := &step.Tasks[j]
			if task.Name == depName {
				childName := buildTeamChildName(parent.Name, step.Name, depName)
				return task, childName, nil
			}
		}
	}

	return nil, buildTeamChildName(parent.Name, stepName, depName), nil
}

// checkArtifactContract verifies the child produced the artifact declared by the contract.
func (s *KubeTeamService) checkArtifactContract(ctx context.Context, child *platformv1alpha1.AgentRun, contract string) error {
	normalized := normalizeArtifactSelector(contract)
	if normalized == "" {
		return nil
	}
	_, err := s.readChildArtifact(ctx, child, normalized)
	if err != nil {
		return fmt.Errorf("artifact %q not found on %s/%s", normalized, child.Namespace, child.Name)
	}
	return nil
}

// enforceRetryBudget checks whether the child has exhausted its task's maxRetries budget.
func enforceRetryBudget(parent *platformv1alpha1.AgentRun, child *platformv1alpha1.AgentRun) error {
	if parent == nil || child == nil || parent.Spec.Team == nil {
		return nil
	}
	taskName := child.Labels[teamTaskLabel]
	stepName := child.Labels[teamStepLabel]
	if taskName == "" || stepName == "" {
		return nil
	}
	_, task, err := findTeamTask(parent, stepName, taskName)
	if err != nil {
		return nil
	}
	if task.MaxRetries <= 0 {
		return nil
	}
	if child.Status.RetryCount >= task.MaxRetries {
		return fmt.Errorf("%w: task %q has %d retries, limit is %d", ErrRetryBudgetExhausted, taskName, child.Status.RetryCount, task.MaxRetries)
	}
	return nil
}
