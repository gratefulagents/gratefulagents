package triggers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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
// exclusive.
func securityScanInvalidSpecMessage(spec triggersv1alpha1.SecurityScanSpec) string {
	if spec.WorkflowRef != nil && len(spec.Workflow) > 0 {
		return "spec.workflowRef and spec.workflow are mutually exclusive: reference a SecurityWorkflow or define the workflow inline, not both"
	}
	return ""
}

// resolvedSecurityScanSpec is a SecurityScan spec with every reusable
// resource reference resolved into inline content, plus the provenance
// snapshot of what was resolved.
type resolvedSecurityScanSpec struct {
	// spec has workflowRef replaced by the referenced tasks and
	// rankerRefs/postScriptRefs appended after the inline entries.
	spec triggersv1alpha1.SecurityScanSpec
	// refs records generation and content hash per resolved resource, in
	// spec order (workflowRef, rankerRefs, postScriptRefs).
	refs []triggersv1alpha1.SecurityScanResolvedRef
}

// resolveSecurityScanRefs resolves spec.workflowRef, spec.rankerRefs, and
// spec.postScriptRefs against the scan's namespace at run-creation time. The
// resolved content is inlined into the returned spec so the run prompt is
// built from a snapshot: later edits to the referenced resources never change
// runs that were already created. A missing reference returns a
// *securityScanRefError with reason UnresolvedReference.
func resolveSecurityScanRefs(
	ctx context.Context, c client.Reader, scan *triggersv1alpha1.SecurityScan,
) (*resolvedSecurityScanSpec, error) {
	if msg := securityScanInvalidSpecMessage(scan.Spec); msg != "" {
		return nil, &securityScanRefError{reason: securityScanReasonInvalidSpec, message: msg}
	}

	resolved := &resolvedSecurityScanSpec{spec: *scan.Spec.DeepCopy()}
	resolved.spec.WorkflowRef = nil
	resolved.spec.RankerRefs = nil
	resolved.spec.PostScriptRefs = nil

	if ref := scan.Spec.WorkflowRef; ref != nil {
		workflow := &triggersv1alpha1.SecurityWorkflow{}
		if err := getSecurityScanRef(ctx, c, scan.Namespace, ref.Name, "SecurityWorkflow", workflow); err != nil {
			return nil, err
		}
		resolved.spec.Workflow = append([]triggersv1alpha1.SecurityScanTask(nil), workflow.Spec.Tasks...)
		if workflow.Spec.Parallelism > 0 {
			resolved.spec.Parallelism = workflow.Spec.Parallelism
		}
		resolved.refs = append(resolved.refs, resolvedSecurityRef("SecurityWorkflow", workflow.Name, workflow.Generation, workflow.Spec))
	}

	for _, ref := range scan.Spec.RankerRefs {
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

	for _, ref := range scan.Spec.PostScriptRefs {
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
