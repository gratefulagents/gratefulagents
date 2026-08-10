package triggers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func validateSecurityScanTaskSkillRefs(
	ctx context.Context,
	c client.Reader,
	namespace string,
	defaults []platformv1alpha1.NamedRef,
	workflow []triggersv1alpha1.SecurityScanTask,
) error {
	seen := make(map[string]struct{}, len(defaults))
	validate := func(ref platformv1alpha1.NamedRef, source string) error {
		name := strings.TrimSpace(ref.Name)
		if _, ok := seen[name]; ok {
			return nil
		}
		seen[name] = struct{}{}
		skill := &platformv1alpha1.Skill{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, skill); err != nil {
			if apierrors.IsNotFound(err) {
				return &securityScanRefError{
					reason:  securityScanReasonUnresolvedReference,
					message: fmt.Sprintf("Skill %q referenced by %s not found in namespace %q", name, source, namespace),
				}
			}
			return fmt.Errorf("resolving Skill %s/%s referenced by %s: %w", namespace, name, source, err)
		}
		if skill.Status.ObservedGeneration != skill.Generation ||
			!strings.EqualFold(strings.TrimSpace(skill.Status.Phase), "Ready") ||
			skill.Status.Resolved == nil ||
			strings.TrimSpace(skill.Status.Resolved.Instructions) == "" {
			return &securityScanRefError{
				reason:  securityScanReasonUnresolvedReference,
				message: fmt.Sprintf("Skill %q referenced by %s is not ready in namespace %q", name, source, namespace),
			}
		}
		return nil
	}
	for _, ref := range defaults {
		if err := validate(ref, "spec.defaults.skillRefs"); err != nil {
			return err
		}
	}
	for _, task := range workflow {
		for _, ref := range task.SkillRefs {
			if err := validate(ref, fmt.Sprintf("workflow task %q", task.Name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Ready=False reasons for reference problems.
const (
	securityScanReasonInvalidSpec         = "InvalidSpec"
	securityScanReasonUnresolvedReference = "UnresolvedReference"
	securityScanReasonCreateRunFailed     = "CreateRunFailed"
)

// securityScanRefError is a deterministic spec/reference failure that must
// surface as a specific Ready=False reason instead of the generic
// CreateRunFailed.
type securityScanRefError struct {
	reason  string
	message string
}

func (e *securityScanRefError) Error() string { return e.message }

// securityScanRunFailureReason maps a createScanRun error to the Ready
// condition reason reported on the scan.
func securityScanRunFailureReason(err error) string {
	var refErr *securityScanRefError
	if errors.As(err, &refErr) {
		return refErr.reason
	}
	return securityScanReasonCreateRunFailed
}

// securityScanInvalidSpecMessage returns a non-empty message when the scan
// spec is statically invalid: workflowRef and an inline workflow are mutually
// exclusive, event triggers and checks require a repository reference, and
// notification rules need a unique name plus at least one channel.
func securityScanInvalidSpecMessage(spec triggersv1alpha1.SecurityScanSpec) string {
	if spec.WorkflowRef != nil && len(spec.Workflow) > 0 {
		return "spec.workflowRef and spec.workflow are mutually exclusive: reference a SecurityWorkflow or define the workflow inline, not both"
	}
	if len(spec.Workflow) > 0 {
		if errs := triggersv1alpha1.ValidateSecurityWorkflowTasks(spec.Workflow); len(errs) != 0 {
			return "spec.workflow is invalid: " + errs[0].Error()
		}
	}
	if errs := triggersv1alpha1.ValidateSecurityScanBudgets("spec.budgets", spec.Budgets); len(errs) != 0 {
		return errs[0].Error()
	}
	if t := spec.Triggers; t != nil && (t.OnPullRequest || t.OnPush) {
		if t.RepositoryRef == nil || strings.TrimSpace(t.RepositoryRef.Name) == "" {
			return "spec.triggers.repositoryRef is required when onPullRequest or onPush is set: it names the GitHubRepository whose webhook deliveries trigger this scan"
		}
	}
	if c := spec.Checks; c != nil && c.Enabled {
		if spec.Triggers == nil || spec.Triggers.RepositoryRef == nil || strings.TrimSpace(spec.Triggers.RepositoryRef.Name) == "" {
			return "spec.checks.enabled requires spec.triggers.repositoryRef: the referenced GitHubRepository supplies the credentials that publish checks"
		}
	}
	seenRules := map[string]bool{}
	for i, rule := range spec.Notifications {
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			return fmt.Sprintf("spec.notifications[%d].name is required", i)
		}
		if seenRules[name] {
			return fmt.Sprintf("spec.notifications[%d].name %q is duplicated: rule names key the persisted notification dedupe markers and must be unique", i, name)
		}
		seenRules[name] = true
		if rule.Slack == nil && rule.GitHubIssues == nil && rule.Linear == nil {
			return fmt.Sprintf("spec.notifications[%d] (%q) configures no channel: set slack, githubIssues, and/or linear", i, name)
		}
	}
	return ""
}

// resolvedSecurityScanSpec is a SecurityScan spec with every reusable
// resource reference resolved into inline content, plus the provenance
// snapshot of what was resolved.
type resolvedSecurityScanSpec struct {
	// spec has workflowRef replaced by the referenced tasks, the policy
	// pack's defaults and enforced floors applied, and
	// rankerRefs/postScriptRefs (including pack defaults) appended after the
	// inline entries.
	spec triggersv1alpha1.SecurityScanSpec
	// refs records generation and content hash per resolved resource, in
	// spec order (policyPackRef, workflowRef, rankerRefs, postScriptRefs).
	refs []triggersv1alpha1.SecurityScanResolvedRef
	// workflowParams are the referenced SecurityWorkflow's declared
	// scan-time parameters; empty for inline workflows, whose {{params.*}}
	// references are free-form.
	workflowParams []triggersv1alpha1.SecurityWorkflowParameter
}

// resolveSecurityScanRefs resolves spec.policyPackRef, spec.workflowRef,
// spec.rankerRefs, and spec.postScriptRefs against the scan's namespace at
// run-creation time. The resolved content is inlined into the returned spec
// so the run prompt is built from a snapshot: later edits to the referenced
// resources never change runs that were already created. The policy pack is
// applied BEFORE prompt construction (precedence: platform defaults < policy
// pack < scan configuration) and its enforced fields are checked here, so
// model output can never affect enforcement; a violating scan returns a
// *securityScanRefError with reason PolicyViolation and no run is created. A
// missing reference returns a *securityScanRefError with reason
// UnresolvedReference.
func resolveSecurityScanRefs(
	ctx context.Context, c client.Reader, scan *triggersv1alpha1.SecurityScan,
) (*resolvedSecurityScanSpec, error) {
	if msg := securityScanInvalidSpecMessage(scan.Spec); msg != "" {
		return nil, &securityScanRefError{reason: securityScanReasonInvalidSpec, message: msg}
	}

	resolved := &resolvedSecurityScanSpec{spec: *scan.Spec.DeepCopy()}
	resolved.spec.WorkflowRef = nil
	resolved.spec.PolicyPackRef = nil
	resolved.spec.RankerRefs = nil
	resolved.spec.PostScriptRefs = nil

	var pack *triggersv1alpha1.SecurityPolicyPack
	if ref := scan.Spec.PolicyPackRef; ref != nil {
		pack = &triggersv1alpha1.SecurityPolicyPack{}
		if err := getSecurityScanRef(ctx, c, scan.Namespace, ref.Name, "SecurityPolicyPack", pack); err != nil {
			return nil, err
		}
		// Enforcement fails closed: an invalid pack rejects the scan instead
		// of silently enforcing nothing.
		if errs := triggersv1alpha1.ValidateSecurityPolicyPackSpec(pack.Spec); len(errs) != 0 {
			return nil, &securityScanRefError{
				reason:  securityScanReasonPolicyViolation,
				message: fmt.Sprintf("SecurityPolicyPack %q is invalid: %s", pack.Name, errs[0].Error()),
			}
		}
		resolved.refs = append(resolved.refs, resolvedSecurityRef("SecurityPolicyPack", pack.Name, pack.Generation, pack.Spec))
	}

	workflowRef := scan.Spec.WorkflowRef
	if workflowRef == nil && len(scan.Spec.Workflow) == 0 {
		workflowRef = &triggersv1alpha1.SecurityResourceRef{Name: triggersv1alpha1.DefaultSecurityWorkflowName}
	}
	if ref := workflowRef; ref != nil {
		workflow := &triggersv1alpha1.SecurityWorkflow{}
		if err := getSecurityScanRef(ctx, c, scan.Namespace, ref.Name, "SecurityWorkflow", workflow); err != nil {
			return nil, err
		}
		resolved.spec.Workflow = append([]triggersv1alpha1.SecurityScanTask(nil), workflow.Spec.Tasks...)
		if workflow.Spec.Parallelism > 0 {
			resolved.spec.Parallelism = workflow.Spec.Parallelism
		}
		resolved.workflowParams = append([]triggersv1alpha1.SecurityWorkflowParameter(nil), workflow.Spec.Parameters...)
		resolved.refs = append(resolved.refs, resolvedSecurityRef("SecurityWorkflow", workflow.Name, workflow.Generation, workflow.Spec))
	}

	rankerRefs := append([]triggersv1alpha1.SecurityResourceRef(nil), scan.Spec.RankerRefs...)
	postScriptRefs := append([]triggersv1alpha1.SecurityResourceRef(nil), scan.Spec.PostScriptRefs...)
	if pack != nil {
		// The pack is applied after workflow resolution so requiredCategories
		// checks the effective task list, and before ranker/post-script
		// resolution so pack defaults are resolved and snapshotted too.
		spec, violations := applySecurityPolicyPack(resolved.spec, pack)
		if len(violations) != 0 {
			return nil, &securityScanRefError{
				reason: securityScanReasonPolicyViolation,
				message: fmt.Sprintf("scan violates enforced SecurityPolicyPack %q: %s",
					pack.Name, strings.Join(violations, "; ")),
			}
		}
		rankerRefs = spec.RankerRefs
		postScriptRefs = spec.PostScriptRefs
		spec.RankerRefs = nil
		spec.PostScriptRefs = nil
		resolved.spec = spec
	}

	for _, ref := range rankerRefs {
		ranker := &triggersv1alpha1.SecurityRanker{}
		if err := getSecurityScanRef(ctx, c, scan.Namespace, ref.Name, "SecurityRanker", ranker); err != nil {
			return nil, err
		}
		resolved.spec.SeverityRankers = append(resolved.spec.SeverityRankers, triggersv1alpha1.SecurityScanRanker{
			Name:  ranker.Name,
			Rules: strings.Join(ranker.Spec.Rules, "\n"),
		})
		resolved.refs = append(resolved.refs, resolvedSecurityRef("SecurityRanker", ranker.Name, ranker.Generation, ranker.Spec))
	}

	for _, ref := range postScriptRefs {
		script := &triggersv1alpha1.SecurityPostScript{}
		if err := getSecurityScanRef(ctx, c, scan.Namespace, ref.Name, "SecurityPostScript", script); err != nil {
			return nil, err
		}
		resolved.spec.PostScripts = append(resolved.spec.PostScripts, triggersv1alpha1.SecurityScanPostScript{
			Name:   script.Name,
			Prompt: script.Spec.Prompt,
			RunOn:  script.Spec.RunOn,
		})
		resolved.refs = append(resolved.refs, resolvedSecurityRef("SecurityPostScript", script.Name, script.Generation, script.Spec))
	}

	return resolved, nil
}

func getSecurityScanRef(ctx context.Context, c client.Reader, namespace, name, kind string, obj client.Object) error {
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return &securityScanRefError{
				reason:  securityScanReasonUnresolvedReference,
				message: fmt.Sprintf("%s %q not found in namespace %q", kind, name, namespace),
			}
		}
		return fmt.Errorf("resolving %s %s/%s: %w", kind, namespace, name, err)
	}
	return nil
}

func resolvedSecurityRef(kind, name string, generation int64, spec any) triggersv1alpha1.SecurityScanResolvedRef {
	return triggersv1alpha1.SecurityScanResolvedRef{
		Kind:       kind,
		Name:       name,
		Generation: generation,
		Hash:       securitySpecHash(spec),
	}
}

// securitySpecHash is a sha256 hex digest of the JSON encoding of a resolved
// resource spec; it pins run provenance to exact content.
func securitySpecHash(spec any) string {
	data, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// securityScanResolvedRefsJSON encodes the resolved-refs snapshot for the run
// annotation. Empty when the scan uses no references.
func securityScanResolvedRefsJSON(refs []triggersv1alpha1.SecurityScanResolvedRef) string {
	if len(refs) == 0 {
		return ""
	}
	data, err := json.Marshal(refs)
	if err != nil {
		return ""
	}
	return string(data)
}
