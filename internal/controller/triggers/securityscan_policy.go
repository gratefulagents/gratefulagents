package triggers

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// securityScanReasonPolicyViolation is the Ready=False reason reported when a
// scan violates an enforced SecurityPolicyPack field. No run is created.
const securityScanReasonPolicyViolation = "PolicyViolation"

// securityPolicySeverityRank orders severities: critical=4 ... info=0, -1 for
// unknown/empty. A higher rank is a more severe threshold, i.e. it reports or
// fails on fewer findings.
func securityPolicySeverityRank(s string) int {
	for i, severity := range securityScanSeverities {
		if severity == s {
			return len(securityScanSeverities) - 1 - i
		}
	}
	return -1
}

// packDedupeSettings returns the pack's effective dedupe configuration using
// the same defaulting as scans (enabled, 820 permille).
func packDedupeSettings(dedupe *triggersv1alpha1.SecurityScanDedupe) (enabled bool, permille int32) {
	spec := triggersv1alpha1.SecurityScanSpec{Dedupe: dedupe}
	return spec.DedupeEnabled(), spec.DedupeSimilarityThresholdPermille()
}

// mergeSecurityScanBudgets fills budget fields the scan leaves unset from
// the pack's budgets (precedence: pack default < scan). A nil result means
// neither side sets any budget.
func mergeSecurityScanBudgets(scan, pack *triggersv1alpha1.SecurityScanBudgets) *triggersv1alpha1.SecurityScanBudgets {
	if pack == nil || pack.IsZero() {
		return scan.DeepCopy()
	}
	out := scan.DeepCopy()
	if out == nil {
		out = &triggersv1alpha1.SecurityScanBudgets{}
	}
	if out.MaxModelJobs == 0 {
		out.MaxModelJobs = pack.MaxModelJobs
	}
	if out.MaxCostUSD == "" {
		out.MaxCostUSD = pack.MaxCostUSD
	}
	if out.MaxTokens == 0 {
		out.MaxTokens = pack.MaxTokens
	}
	if out.MaxRuntime.Duration == 0 {
		out.MaxRuntime = pack.MaxRuntime
	}
	if out.MaxFindings == 0 {
		out.MaxFindings = pack.MaxFindings
	}
	if out.MaxValidationJobs == 0 {
		out.MaxValidationJobs = pack.MaxValidationJobs
	}
	return out
}

// securityBudgetCostUSD parses a budget cost string; empty or invalid means
// no ceiling (-1).
func securityBudgetCostUSD(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return -1
	}
	return v
}

// securityBudgetViolations checks the merged (effective) budgets against an
// enforced pack: for every limit the pack sets, the effective limit may not
// be higher. Because merging inherits unset scan fields from the pack, a
// violation can only come from the scan explicitly setting a higher value.
func securityBudgetViolations(effective, pack *triggersv1alpha1.SecurityScanBudgets, packName string) []string {
	if pack == nil || pack.IsZero() {
		return nil
	}
	var violations []string
	violate := func(field string, got, limit any) {
		violations = append(violations, fmt.Sprintf(
			"budgets.%s %v is raised above the pack's %v: policy pack %q enforces its budgets", field, got, limit, packName))
	}
	if effective == nil {
		effective = &triggersv1alpha1.SecurityScanBudgets{}
	}
	if pack.MaxModelJobs > 0 && effective.MaxModelJobs > pack.MaxModelJobs {
		violate("maxModelJobs", effective.MaxModelJobs, pack.MaxModelJobs)
	}
	if packCost := securityBudgetCostUSD(pack.MaxCostUSD); packCost >= 0 {
		if cost := securityBudgetCostUSD(effective.MaxCostUSD); cost < 0 || cost > packCost {
			violate("maxCostUSD", effective.MaxCostUSD, pack.MaxCostUSD)
		}
	}
	if pack.MaxTokens > 0 && effective.MaxTokens > pack.MaxTokens {
		violate("maxTokens", effective.MaxTokens, pack.MaxTokens)
	}
	if pack.MaxRuntime.Duration > 0 && effective.MaxRuntime.Duration > pack.MaxRuntime.Duration {
		violate("maxRuntime", effective.MaxRuntime.Duration, pack.MaxRuntime.Duration)
	}
	if pack.MaxFindings > 0 && effective.MaxFindings > pack.MaxFindings {
		violate("maxFindings", effective.MaxFindings, pack.MaxFindings)
	}
	if pack.MaxValidationJobs > 0 && effective.MaxValidationJobs > pack.MaxValidationJobs {
		violate("maxValidationJobs", effective.MaxValidationJobs, pack.MaxValidationJobs)
	}
	return violations
}

// appendMissingSecurityRefs appends the pack's default refs that the scan
// does not already list, preserving order and avoiding duplicate resolution.
func appendMissingSecurityRefs(refs, defaults []triggersv1alpha1.SecurityResourceRef) []triggersv1alpha1.SecurityResourceRef {
	out := append([]triggersv1alpha1.SecurityResourceRef(nil), refs...)
	for _, def := range defaults {
		exists := slices.ContainsFunc(out, func(ref triggersv1alpha1.SecurityResourceRef) bool {
			return ref.Name == def.Name
		})
		if !exists {
			out = append(out, def)
		}
	}
	return out
}

// applySecurityPolicyPack folds the pack into the scan spec (precedence:
// platform defaults < policy pack < scan configuration) and checks the pack's
// enforced fields against the EFFECTIVE spec. spec must already have
// workflowRef resolved so requiredCategories sees the real task list. The
// returned violations are empty when the scan complies.
//
// Per-field "relax" semantics (checked only for fields listed in enforced):
//   - minSeverity: the scan's effective minSeverity may not be more severe
//     than the pack's, because a higher reporting floor reports less.
//   - failOnSeverity: the scan's effective failOnSeverity may not be empty or
//     a more severe threshold than the pack's, because both fail on less.
//   - dedupe: the scan may not disable dedupe while the pack enables it, nor
//     lower the similarity threshold below the pack's (a lower threshold
//     collapses less-similar findings as duplicates).
//   - requiredCategories: every pack category must appear among the effective
//     workflow tasks' categories.
//   - allowedRuntimeProfiles: defaults.runtimeProfileRef must name one of the
//     pack's allowed profiles; a scan without a profile does not comply.
func applySecurityPolicyPack(
	spec triggersv1alpha1.SecurityScanSpec, pack *triggersv1alpha1.SecurityPolicyPack,
) (triggersv1alpha1.SecurityScanSpec, []string) {
	out := *spec.DeepCopy()
	p := pack.Spec

	// Precedence: the pack fills fields the scan leaves unset.
	if out.MinSeverity == "" {
		out.MinSeverity = p.MinSeverity
	}
	if out.FailOnSeverity == "" {
		out.FailOnSeverity = p.FailOnSeverity
	}
	if out.Dedupe == nil && p.Dedupe != nil {
		out.Dedupe = p.Dedupe.DeepCopy()
	}
	out.Budgets = mergeSecurityScanBudgets(out.Budgets, p.Budgets)
	out.RankerRefs = appendMissingSecurityRefs(out.RankerRefs, p.DefaultRankerRefs)
	out.PostScriptRefs = appendMissingSecurityRefs(out.PostScriptRefs, p.DefaultPostScriptRefs)

	var violations []string
	violate := func(format string, args ...any) {
		violations = append(violations, fmt.Sprintf(format, args...))
	}
	for _, field := range p.Enforced {
		switch field {
		case triggersv1alpha1.SecurityPolicyFieldMinSeverity:
			if p.MinSeverity == "" {
				continue
			}
			if securityPolicySeverityRank(out.EffectiveMinSeverity()) > securityPolicySeverityRank(p.MinSeverity) {
				violate("minSeverity %q is raised above the pack's %q: the scan would report less than policy pack %q requires",
					out.EffectiveMinSeverity(), p.MinSeverity, pack.Name)
			}
		case triggersv1alpha1.SecurityPolicyFieldFailOnSeverity:
			if p.FailOnSeverity == "" {
				continue
			}
			if out.FailOnSeverity == "" || securityPolicySeverityRank(out.FailOnSeverity) > securityPolicySeverityRank(p.FailOnSeverity) {
				violate("failOnSeverity %q weakens the pack's %q: policy pack %q requires failing at or above that severity",
					out.FailOnSeverity, p.FailOnSeverity, pack.Name)
			}
		case triggersv1alpha1.SecurityPolicyFieldDedupe:
			packEnabled, packPermille := packDedupeSettings(p.Dedupe)
			if !packEnabled {
				continue
			}
			if !out.DedupeEnabled() {
				violate("dedupe is disabled: policy pack %q enforces dedupe", pack.Name)
			} else if out.DedupeSimilarityThresholdPermille() < packPermille {
				violate("dedupe similarityThresholdPermille %d is looser than the pack's %d: policy pack %q enforces the tighter threshold",
					out.DedupeSimilarityThresholdPermille(), packPermille, pack.Name)
			}
		case triggersv1alpha1.SecurityPolicyFieldRequiredCategories:
			covered := map[string]bool{}
			for _, task := range out.EffectiveWorkflow() {
				covered[task.Category] = true
			}
			var missing []string
			for _, category := range p.RequiredCategories {
				if !covered[category] {
					missing = append(missing, category)
				}
			}
			if len(missing) > 0 {
				violate("workflow does not cover required categories %s: policy pack %q requires them",
					strings.Join(missing, ", "), pack.Name)
			}
		case triggersv1alpha1.SecurityPolicyFieldAllowedRuntimeProfiles:
			profile := ""
			if out.Defaults.RuntimeProfileRef != nil {
				profile = out.Defaults.RuntimeProfileRef.Name
			}
			if !slices.Contains(p.AllowedRuntimeProfiles, profile) {
				violate("runtime profile %q is not in policy pack %q allowedRuntimeProfiles (%s)",
					profile, pack.Name, strings.Join(p.AllowedRuntimeProfiles, ", "))
			}
		case triggersv1alpha1.SecurityPolicyFieldBudgets:
			violations = append(violations, securityBudgetViolations(out.Budgets, p.Budgets, pack.Name)...)
		}
	}
	return out, violations
}

// securitySuppressionRulesFromPack converts a pack's suppression rules into
// store rules. The persisted suppression id is "<pack name>/<rule name>".
func securitySuppressionRulesFromPack(pack *triggersv1alpha1.SecurityPolicyPack) []store.SecuritySuppressionRule {
	rules := make([]store.SecuritySuppressionRule, 0, len(pack.Spec.Suppressions))
	for _, rule := range pack.Spec.Suppressions {
		r := store.SecuritySuppressionRule{
			ID:     pack.Name + "/" + rule.Name,
			Reason: rule.Reason,
			Owner:  rule.Owner,
			Matcher: store.SecuritySuppressionMatcher{
				Category:    rule.Matcher.Category,
				CWE:         rule.Matcher.CWE,
				PathGlob:    rule.Matcher.PathGlob,
				Fingerprint: rule.Matcher.Fingerprint,
			},
		}
		if rule.ExpiresAt != nil {
			t := rule.ExpiresAt.Time
			r.ExpiresAt = &t
		}
		rules = append(rules, r)
	}
	return rules
}

// sweepSecuritySuppressions runs the suppression governance sweep for a scan
// from the status-refresh/finalize path: expired suppressions in the
// namespace are cleared (audited, never erased), suppressions whose rule was
// revoked — deleted from the pack, no longer matching the finding, or the
// pack/policyPackRef removed entirely — are cleared with a
// "suppression_revoked" audit event, then the scan's current policy pack
// suppression rules are applied to its persisted findings. All store calls
// are idempotent and cheap; errors are best-effort (logged, never failing
// the reconcile).
func (r *SecurityScanReconciler) sweepSecuritySuppressions(ctx context.Context, scan *triggersv1alpha1.SecurityScan) {
	if r.Findings == nil {
		return
	}
	log := logf.FromContext(ctx)
	if _, err := r.Findings.ExpireSecuritySuppressions(ctx, scan.Namespace); err != nil {
		log.Error(err, "failed to expire security suppressions", "scan", scan.Name)
	}
	var active []store.SecuritySuppressionRule
	if scan.Spec.PolicyPackRef != nil {
		pack := &triggersv1alpha1.SecurityPolicyPack{}
		err := r.Get(ctx, client.ObjectKey{Namespace: scan.Namespace, Name: scan.Spec.PolicyPackRef.Name}, pack)
		switch {
		case apierrors.IsNotFound(err):
			// A deleted pack leaves no active rules: every suppression it
			// granted is revoked below.
		case err != nil:
			// Unknown pack state must not revoke governed suppressions.
			log.Error(err, "failed to get SecurityPolicyPack for suppression sweep", "pack", scan.Spec.PolicyPackRef.Name)
			return
		case len(triggersv1alpha1.ValidateSecurityPolicyPackSpec(pack.Spec)) != 0:
			// An invalid pack's rule intent is unknowable: keep the current
			// suppressions until it is fixed (runs fail closed elsewhere).
			return
		default:
			now := r.now()
			for _, rule := range securitySuppressionRulesFromPack(pack) {
				if rule.ExpiresAt == nil || rule.ExpiresAt.After(now) {
					active = append(active, rule)
				}
			}
		}
	}
	// Revoke before applying so a finding released by one rule can be
	// re-suppressed by another rule in the same sweep, with both audited.
	if _, err := r.Findings.RevokeSecuritySuppressions(ctx, scan.Namespace, scan.Name, active); err != nil {
		log.Error(err, "failed to revoke security suppressions", "scan", scan.Name)
	}
	if len(active) == 0 {
		return
	}
	if _, err := r.Findings.ApplySecuritySuppressions(ctx, scan.Namespace, scan.Name, active); err != nil {
		log.Error(err, "failed to apply security suppressions", "scan", scan.Name, "pack", scan.Spec.PolicyPackRef.Name)
	}
}

// effectiveFailOnSeverity returns the scan's failOnSeverity with policy pack
// precedence applied: the scan's own value wins, otherwise the referenced
// pack's default. Best-effort: an unreadable or invalid pack falls back to
// the scan's value.
func (r *SecurityScanReconciler) effectiveFailOnSeverity(ctx context.Context, scan *triggersv1alpha1.SecurityScan) string {
	if scan.Spec.FailOnSeverity != "" || scan.Spec.PolicyPackRef == nil {
		return scan.Spec.FailOnSeverity
	}
	pack := &triggersv1alpha1.SecurityPolicyPack{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: scan.Namespace, Name: scan.Spec.PolicyPackRef.Name}, pack); err != nil {
		return ""
	}
	return pack.Spec.FailOnSeverity
}

// scanPolicyPack fetches the scan's referenced SecurityPolicyPack.
// Best-effort: it returns nil when the scan references no pack, the pack is
// missing/unreadable, or the pack spec is invalid (an invalid pack already
// rejects run creation elsewhere; monitoring paths just fall back to the
// scan's own configuration).
func (r *SecurityScanReconciler) scanPolicyPack(ctx context.Context, scan *triggersv1alpha1.SecurityScan) *triggersv1alpha1.SecurityPolicyPack {
	if scan.Spec.PolicyPackRef == nil {
		return nil
	}
	pack := &triggersv1alpha1.SecurityPolicyPack{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: scan.Namespace, Name: scan.Spec.PolicyPackRef.Name}, pack); err != nil {
		if !apierrors.IsNotFound(err) {
			logf.FromContext(ctx).Error(err, "failed to get SecurityPolicyPack", "pack", scan.Spec.PolicyPackRef.Name)
		}
		return nil
	}
	if errs := triggersv1alpha1.ValidateSecurityPolicyPackSpec(pack.Spec); len(errs) != 0 {
		return nil
	}
	return pack
}

// effectiveSecurityScanBudgets returns the scan's budgets merged with the
// referenced policy pack's defaults, for controller-side monitoring. All
// inputs come from CRD specs; model output can never influence the result.
func effectiveSecurityScanBudgets(scan *triggersv1alpha1.SecurityScan, pack *triggersv1alpha1.SecurityPolicyPack) *triggersv1alpha1.SecurityScanBudgets {
	if pack == nil {
		return mergeSecurityScanBudgets(scan.Spec.Budgets, nil)
	}
	return mergeSecurityScanBudgets(scan.Spec.Budgets, pack.Spec.Budgets)
}
