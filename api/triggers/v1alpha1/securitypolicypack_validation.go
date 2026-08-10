/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// securityBudgetCostPattern matches the decimal USD strings AgentRunLimits
// accepts (e.g. "5", "2.50", or empty for no ceiling).
var securityBudgetCostPattern = regexp.MustCompile(`^([0-9]+(\.[0-9]+)?)?$`)

// ValidateSecurityScanBudgets validates a budgets block for both
// SecurityPolicyPack.spec.budgets and SecurityScan.spec.budgets: every count
// non-negative and maxCostUSD a plain decimal. Field paths are prefixed with
// prefix (e.g. "budgets").
func ValidateSecurityScanBudgets(prefix string, b *SecurityScanBudgets) []SecurityWorkflowFieldError {
	if b == nil {
		return nil
	}
	var errs []SecurityWorkflowFieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, SecurityWorkflowFieldError{Field: prefix + "." + field, Message: fmt.Sprintf(format, args...)})
	}
	if b.MaxModelJobs < 0 {
		add("maxModelJobs", "must not be negative (0 = unlimited)")
	}
	if !securityBudgetCostPattern.MatchString(b.MaxCostUSD) {
		add("maxCostUSD", "invalid cost %q (want a plain decimal like \"5\" or \"2.50\")", b.MaxCostUSD)
	}
	if b.MaxTokens < 0 {
		add("maxTokens", "must not be negative (0 = unlimited)")
	}
	if b.MaxRuntime.Duration < 0 {
		add("maxRuntime", "must not be negative (0 = unlimited)")
	}
	if b.MaxValidationJobs < 0 {
		add("maxValidationJobs", "must not be negative (0 = unlimited)")
	}
	return errs
}

// ValidateSecurityRetention validates a retention block: every day count in
// [0, SecurityRetentionMaxDays] (0 = keep forever). Field paths are prefixed
// with prefix (e.g. "retention").
func ValidateSecurityRetention(prefix string, r *SecurityPolicyPackRetention) []SecurityWorkflowFieldError {
	if r == nil {
		return nil
	}
	var errs []SecurityWorkflowFieldError
	check := func(field string, days int32) {
		if days < 0 || days > SecurityRetentionMaxDays {
			errs = append(errs, SecurityWorkflowFieldError{
				Field:   prefix + "." + field,
				Message: fmt.Sprintf("%d days out of range (want 0 for keep-forever, or 1-%d)", days, SecurityRetentionMaxDays),
			})
		}
	}
	check("scanDays", r.ScanDays)
	check("findingDays", r.FindingDays)
	check("reportDays", r.ReportDays)
	check("evidenceDays", r.EvidenceDays)
	check("pocDays", r.PoCDays)
	check("auditEventDays", r.AuditEventDays)
	return errs
}

// validSecurityPolicySeverity reports whether s is a known severity or empty.
func validSecurityPolicySeverity(s string) bool {
	switch s {
	case "", "critical", "high", "medium", "low", "info":
		return true
	}
	return false
}

// ValidateSecurityPolicyPackSpec validates a SecurityPolicyPack spec the same
// way for the dashboard and the pack reconciler: known severities, a valid
// dedupe threshold, enforced entries drawn from the enforceable field names,
// and suppression rules with a unique DNS-1123 label name, a reason, an
// owner, and at least one matcher field.
func ValidateSecurityPolicyPackSpec(spec SecurityPolicyPackSpec) []SecurityWorkflowFieldError {
	var errs []SecurityWorkflowFieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, SecurityWorkflowFieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if !validSecurityPolicySeverity(spec.MinSeverity) {
		add("minSeverity", "invalid severity %q (want critical, high, medium, low, or info)", spec.MinSeverity)
	}
	if !validSecurityPolicySeverity(spec.FailOnSeverity) {
		add("failOnSeverity", "invalid severity %q (want critical, high, medium, low, or info)", spec.FailOnSeverity)
	}
	if d := spec.Dedupe; d != nil && (d.SimilarityThresholdPermille < 0 || d.SimilarityThresholdPermille > 1000) {
		add("dedupe.similarityThresholdPermille", "threshold %d out of range (want 0-1000)", d.SimilarityThresholdPermille)
	}
	errs = append(errs, ValidateSecurityRetention("retention", spec.Retention)...)
	errs = append(errs, ValidateSecurityScanBudgets("budgets", spec.Budgets)...)

	seenEnforced := map[string]bool{}
	for i, field := range spec.Enforced {
		path := fmt.Sprintf("enforced[%d]", i)
		if !slices.Contains(SecurityPolicyPackEnforceableFields, field) {
			add(path, "unknown enforceable field %q (want one of: %s)", field, strings.Join(SecurityPolicyPackEnforceableFields, ", "))
			continue
		}
		if seenEnforced[field] {
			add(path, "field %q is listed twice", field)
		}
		seenEnforced[field] = true
		// Enforcing a field whose pack value is unset would either enforce
		// nothing or reject every scan; require the value.
		switch field {
		case SecurityPolicyFieldMinSeverity:
			if spec.MinSeverity == "" {
				add(path, "enforcing minSeverity requires minSeverity to be set")
			}
		case SecurityPolicyFieldFailOnSeverity:
			if spec.FailOnSeverity == "" {
				add(path, "enforcing failOnSeverity requires failOnSeverity to be set")
			}
		case SecurityPolicyFieldRequiredCategories:
			if len(spec.RequiredCategories) == 0 {
				add(path, "enforcing requiredCategories requires a non-empty requiredCategories list")
			}
		case SecurityPolicyFieldAllowedRuntimeProfiles:
			if len(spec.AllowedRuntimeProfiles) == 0 {
				add(path, "enforcing allowedRuntimeProfiles requires a non-empty allowedRuntimeProfiles list")
			}
		case SecurityPolicyFieldBudgets:
			if spec.Budgets == nil || spec.Budgets.IsZero() {
				add(path, "enforcing budgets requires at least one budget limit to be set")
			}
		}
	}

	for i, category := range spec.RequiredCategories {
		if strings.TrimSpace(category) == "" {
			add(fmt.Sprintf("requiredCategories[%d]", i), "category must not be blank")
		}
	}

	seenRules := map[string]bool{}
	for i, rule := range spec.Suppressions {
		path := fmt.Sprintf("suppressions[%d]", i)
		if problems := validation.IsDNS1123Label(rule.Name); len(problems) != 0 {
			add(path+".name", "invalid rule name %q (want a DNS-1123 label)", rule.Name)
		} else if seenRules[rule.Name] {
			add(path+".name", "duplicate rule name %q: rule names key the persisted suppression markers and must be unique", rule.Name)
		}
		seenRules[rule.Name] = true
		if strings.TrimSpace(rule.Reason) == "" {
			add(path+".reason", "rule %q needs a reason", rule.Name)
		}
		if strings.TrimSpace(rule.Owner) == "" {
			add(path+".owner", "rule %q needs an owner", rule.Name)
		}
		if rule.Matcher.IsZero() {
			add(path+".matcher", "rule %q needs at least one matcher field (category, cwe, pathGlob, or fingerprint)", rule.Name)
		}
	}

	return errs
}
