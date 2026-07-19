package agentrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

var (
	ErrParentRunRequired       = errors.New("parent AgentRun is required")
	ErrExecutionModeNotTeam    = errors.New("parent AgentRun must use executionMode=team")
	ErrTeamSpecRequired        = errors.New("parent AgentRun team spec is required")
	ErrWorkflowModeUnsupported = errors.New("team mode requires chat or auto workflow mode")
	ErrParentOnlyDelegation    = errors.New("team mode requires parent-only delegation in v2")
	ErrParentScopeMismatch     = errors.New("requested parent does not match runtime parent binding")
	ErrRuntimeParentBinding    = errors.New("runtime parent binding is invalid")
	ErrChildCrossNamespace     = errors.New("child namespace must match parent namespace")
	ErrChildMessageRequired    = errors.New("child message is required")
	ErrChildArtifactNotFound   = errors.New("child artifact was not found")
	ErrDependencyNotReady      = errors.New("task dependency has not completed successfully")
	ErrArtifactContractNotMet  = errors.New("upstream task artifact contract is not satisfied")
	ErrRetryBudgetExhausted    = errors.New("task retry budget is exhausted")
	ErrMaxChildrenExceeded     = errors.New("delegation policy maxChildren limit reached")
	ErrMaxDepthExceeded        = errors.New("delegation policy maxDepth limit reached")
)

// TeamService is the shared orchestration contract used by ConnectRPC and MCP adapters.
type TeamService interface {
	CreateChildRun(context.Context, CreateChildRunRequest) (*ChildRunStatus, error)
	ListChildRuns(context.Context, ListChildRunsRequest) (*ListChildRunsResponse, error)
	GetChildRunStatus(context.Context, GetChildRunStatusRequest) (*ChildRunStatus, error)
	GetChildRunArtifact(context.Context, GetChildRunArtifactRequest) (*ChildRunArtifact, error)
	GetChildRunLogs(context.Context, GetChildRunStatusRequest) (*ChildRunLogs, error)
	SendMessageToChild(context.Context, SendMessageToChildRequest) (*ChildRunStatus, error)
	GetParentTeamStatus(context.Context, GetParentTeamStatusRequest) (*platformv1alpha1.AgentRunTeamSummary, error)
	WaitForRunChange(context.Context, WaitForRunChangeRequest) (*WaitForRunChangeResponse, error)
	CancelChildRun(context.Context, CancelChildRunRequest) (*ChildRunStatus, error)
	RetryChildRun(context.Context, RetryChildRunRequest) (*ChildRunStatus, error)
	GetApprovalStatus(context.Context, GetApprovalStatusRequest) (*ApprovalStatus, error)
	HasActiveChildren(context.Context, ParentRunRef) (bool, error)
}

type ParentRunRef struct {
	Namespace string
	Name      string
}

type ChildRunRef struct {
	Namespace string
	Name      string
}

type RuntimeParentBinding struct {
	Parent    ParentRunRef
	ParentUID string
}

type CreateChildRunRequest struct {
	Parent       ParentRunRef
	StepName     string
	TaskName     string
	Instructions string
	Mode         string // optional: "auto" (default), "chat", "plan", etc.
}

type ListChildRunsRequest struct {
	Parent   ParentRunRef
	StepName string
}

type ListChildRunsResponse struct {
	Children []ChildRunStatus
}

type GetChildRunStatusRequest struct {
	Parent ParentRunRef
	Child  ChildRunRef
}

type GetChildRunArtifactRequest struct {
	Parent   ParentRunRef
	Child    ChildRunRef
	Artifact string
}

type GetParentTeamStatusRequest struct {
	Parent ParentRunRef
}

type WaitForRunChangeRequest struct {
	Parent      ParentRunRef
	Scope       string
	TimeoutMS   int64
	UntilPhases []string
}

type WaitForRunChangeResponse struct {
	Changed bool
	RunName string
	Phase   string
}

type CancelChildRunRequest struct {
	Parent ParentRunRef
	Child  ChildRunRef
}

type RetryChildRunRequest struct {
	Parent ParentRunRef
	Child  ChildRunRef
}

type SendMessageToChildRequest struct {
	Parent  ParentRunRef
	Child   ChildRunRef
	Message string
}

type GetApprovalStatusRequest struct {
	Parent ParentRunRef
}

type ApprovalStatus struct {
	State string
}

type ChildRunStatus struct {
	Name          string
	Namespace     string
	Step          string
	Role          string
	Phase         string
	BlockReason   string
	CurrentTask   string
	CurrentWorker string
	Report        string
}

type ChildRunArtifact struct {
	Artifact string `json:"artifact,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Name     string `json:"name,omitempty"`
	Key      string `json:"key,omitempty"`
	URL      string `json:"url,omitempty"`
	Content  string `json:"content,omitempty"`
}

type ChildRunLogs struct {
	Status                 ChildRunStatus
	UserInputType          string
	PendingQuestion        string
	LastError              string
	ActivityLogURL         string
	RecentActivity         []platformv1alpha1.AgentRunActivity
	ConversationTail       []platformv1alpha1.AgentRunChatMessage
	CanUnblockWithRetry    bool
	SuggestedUnblockAction string
}

// ValidateTeamParent enforces the v2 architecture defaults for parent-visible team orchestration.
func ValidateTeamParent(run *platformv1alpha1.AgentRun) error {
	if run == nil {
		return ErrParentRunRequired
	}
	allowAdHocParent := allowsAdHocTeamParent(run)
	// Any mode is compatible with team execution — the snapshot determines behavior.
	if run.Spec.ExecutionMode != platformv1alpha1.ExecutionModeTeam {
		if !allowAdHocParent || run.Spec.Team != nil {
			return ErrExecutionModeNotTeam
		}
		return nil
	}
	if run.Spec.Team == nil {
		if allowAdHocParent {
			return nil
		}
		return ErrTeamSpecRequired
	}
	if run.Spec.Team.DelegationPolicy != nil && !run.Spec.Team.DelegationPolicy.ParentOnly {
		return ErrParentOnlyDelegation
	}
	return nil
}

func allowsAdHocTeamParent(run *platformv1alpha1.AgentRun) bool {
	if run == nil {
		return false
	}
	// All workflow modes support ad-hoc team parents.
	return true
}

func RuntimeParentBindingFromEnv() (RuntimeParentBinding, bool, error) {
	parentNamespace := strings.TrimSpace(os.Getenv("AGENTRUN_PARENT_NAMESPACE"))
	parentName := strings.TrimSpace(os.Getenv("AGENTRUN_PARENT_NAME"))
	parentUID := strings.TrimSpace(os.Getenv("AGENTRUN_PARENT_UID"))
	currentNamespace := strings.TrimSpace(os.Getenv("AGENTRUN_CURRENT_NAMESPACE"))
	currentName := strings.TrimSpace(os.Getenv("AGENTRUN_CURRENT_NAME"))
	currentUID := strings.TrimSpace(os.Getenv("AGENTRUN_CURRENT_UID"))

	scopeConfigured := parentNamespace != "" || parentName != "" || parentUID != "" || currentNamespace != "" || currentName != "" || currentUID != ""
	if !scopeConfigured {
		parentNamespace = firstNonEmptyEnv("RUN_NAMESPACE", "POD_NAMESPACE")
		parentName = firstNonEmptyEnv("RUN_NAME", "PLANTASK_NAME", "CODINGTASK_NAME")
		parentUID = firstNonEmptyEnv("RUN_UID", "PLANTASK_UID", "CODINGTASK_UID")
		scopeConfigured = parentNamespace != "" || parentName != "" || parentUID != ""
	}
	if !scopeConfigured {
		return RuntimeParentBinding{}, false, nil
	}

	if parentNamespace == "" {
		parentNamespace = currentNamespace
	}
	if parentName == "" {
		parentName = currentName
	}
	if parentUID == "" {
		parentUID = currentUID
	}

	if parentNamespace == "" || parentName == "" {
		return RuntimeParentBinding{}, false, fmt.Errorf("%w: parent namespace/name are required when runtime binding is present", ErrRuntimeParentBinding)
	}

	return RuntimeParentBinding{
		Parent: ParentRunRef{
			Namespace: parentNamespace,
			Name:      parentName,
		},
		ParentUID: parentUID,
	}, true, nil
}

func ValidateRuntimeParentBinding(requested ParentRunRef) error {
	binding, ok, err := RuntimeParentBindingFromEnv()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	reqNamespace := strings.TrimSpace(requested.Namespace)
	reqName := strings.TrimSpace(requested.Name)
	if reqNamespace == "" || reqName == "" {
		return fmt.Errorf("%w: requested parent reference is incomplete", ErrParentScopeMismatch)
	}
	if reqNamespace != binding.Parent.Namespace || reqName != binding.Parent.Name {
		return fmt.Errorf("%w: requested=%s/%s runtime=%s/%s", ErrParentScopeMismatch, reqNamespace, reqName, binding.Parent.Namespace, binding.Parent.Name)
	}
	return nil
}

func ValidateRuntimeParentUID(parent *platformv1alpha1.AgentRun) error {
	if parent == nil {
		return ErrParentRunRequired
	}
	binding, ok, err := RuntimeParentBindingFromEnv()
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(binding.ParentUID) == "" {
		return nil
	}
	if string(parent.UID) != binding.ParentUID {
		return fmt.Errorf("%w: requested uid=%s runtime uid=%s", ErrParentScopeMismatch, string(parent.UID), binding.ParentUID)
	}
	return nil
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
