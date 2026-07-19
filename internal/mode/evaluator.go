package mode

import (
	"context"
	"fmt"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TransitionResult is the outcome of evaluating a mode transition.
type TransitionResult string

const (
	ResultApplied    TransitionResult = "applied"
	ResultDenied     TransitionResult = "denied"
	ResultNoop       TransitionResult = "noop"
	ResultRolledBack TransitionResult = "rolled_back"
)

// EvaluateResult contains the outcome of evaluating a mode switch.
type EvaluateResult struct {
	Result   TransitionResult
	Reason   string
	DenyCode string
	// Resolved target template (nil if denied/noop).
	Target *platformv1alpha1.ModeTemplateSpec
}

// EvaluateOpts provides context for gate and RBAC evaluation.
type EvaluateOpts struct {
	Run       *platformv1alpha1.AgentRun
	ActorRole Role
	Source    string
}

// Evaluate checks whether switching from the current mode to a target is valid.
// The caller must resolve the target template (e.g. via CRD lookup).
func Evaluate(
	current *platformv1alpha1.ModeTemplateSpec,
	target *platformv1alpha1.ModeTemplateSpec,
	opts ...EvaluateOpts,
) EvaluateResult {
	if target == nil {
		return EvaluateResult{
			Result:   ResultDenied,
			Reason:   fmt.Sprintf("[%s] target template is nil", DenyTemplateNotFound),
			DenyCode: DenyTemplateNotFound,
		}
	}

	// Noop: same mode.
	if current != nil && current.Name == target.Name {
		return EvaluateResult{
			Result: ResultNoop,
			Reason: "already in requested mode",
		}
	}

	// RBAC check (if opts provided). The target template is already resolved
	// here, so the installed-template minimum (member) applies.
	if len(opts) > 0 {
		o := opts[0]
		minRole := minRoleInstalledTemplate
		if o.ActorRole != RoleSystem && roleRank(o.ActorRole) < roleRank(minRole) {
			rbacResult := &GateResult{
				Gate:     "rbac",
				Passed:   false,
				Reason:   fmt.Sprintf("role %q insufficient for mode %q (requires %q)", o.ActorRole, target.Name, minRole),
				DenyCode: DenyRBACDenied,
			}
			return EvaluateResult{
				Result:   ResultDenied,
				Reason:   FormatDenialReason(rbacResult),
				DenyCode: rbacResult.DenyCode,
			}
		}
	}

	return EvaluateResult{
		Result: ResultApplied,
		Target: target,
	}
}

// ResolveTemplate looks up a ModeTemplate CRD by name+version.
// Always reads the live CRD to ensure template edits are picked up immediately.
func ResolveTemplate(ctx context.Context, c client.Reader, name, version string) (*platformv1alpha1.ModeTemplateSpec, error) {
	key := TemplateKey(name, version)
	var crd platformv1alpha1.ModeTemplate
	if err := c.Get(ctx, client.ObjectKey{Name: key}, &crd); err != nil {
		return nil, fmt.Errorf("[%s] mode template %q not found: %w", DenyTemplateNotFound, key, err)
	}
	return crd.Spec.DeepCopy(), nil
}
