/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/util/validation"
)

// ValidateSecurityProgramSpec validates the operator-authored program scope
// snapshot for dashboard writes, reconciliation, and scan dispatch.
func ValidateSecurityProgramSpec(spec SecurityProgramSpec) []SecurityWorkflowFieldError {
	var errs []SecurityWorkflowFieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, SecurityWorkflowFieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	provider := strings.TrimSpace(spec.Provider)
	if provider == "" {
		add("provider", "is required")
	} else if len(provider) > MaxSecurityProgramProviderLength {
		add("provider", "must be at most %d bytes", MaxSecurityProgramProviderLength)
	}
	displayName := strings.TrimSpace(spec.DisplayName)
	if displayName == "" {
		add("displayName", "is required")
	} else if len(displayName) > MaxSecurityProgramDisplayNameLength {
		add("displayName", "must be at most %d bytes", MaxSecurityProgramDisplayNameLength)
	}

	programURL := strings.TrimSpace(spec.ProgramURL)
	parsed, err := url.ParseRequestURI(programURL)
	if programURL == "" {
		add("programURL", "is required")
	} else if len(programURL) > MaxSecurityProgramURLLength {
		add("programURL", "must be at most %d bytes", MaxSecurityProgramURLLength)
	} else if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		add("programURL", "must be an absolute HTTPS URL without user information")
	}

	scopePolicy := strings.TrimSpace(spec.ScopePolicy)
	if scopePolicy == "" {
		add("scopePolicy", "is required")
	} else if utf8.RuneCountInString(spec.ScopePolicy) > MaxSecurityProgramScopePolicyLength {
		add("scopePolicy", "must be at most %d characters", MaxSecurityProgramScopePolicyLength)
	}
	if spec.VerifiedAt.IsZero() {
		add("verifiedAt", "is required")
	}
	if spec.ScanTarget != nil && len(spec.ScanTargets) != 0 {
		add("scanTargets", "cannot be set together with deprecated scanTarget")
	}
	if len(spec.ScanTargets) > MaxSecurityProgramScanTargets {
		add("scanTargets", "must contain at most %d targets", MaxSecurityProgramScanTargets)
	}

	targets := spec.ScanTargets
	prefix := func(index int) string { return fmt.Sprintf("scanTargets[%d]", index) }
	if len(targets) == 0 && spec.ScanTarget != nil {
		targets = []SecurityProgramScanTarget{*spec.ScanTarget}
		prefix = func(int) string { return "scanTarget" }
	}
	seenScanNames := make(map[string]int, len(targets))
	for index := range targets {
		target := &targets[index]
		fieldPrefix := prefix(index)
		repositoryURL := strings.TrimSpace(target.RepositoryURL)
		parsed, err := url.ParseRequestURI(repositoryURL)
		if repositoryURL == "" {
			add(fieldPrefix+".repositoryURL", "is required")
		} else if len(repositoryURL) > MaxSecurityProgramURLLength {
			add(fieldPrefix+".repositoryURL", "must be at most %d bytes", MaxSecurityProgramURLLength)
		} else if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			add(fieldPrefix+".repositoryURL", "must be an absolute HTTPS URL without user information")
		}
		baseBranch := strings.TrimSpace(target.BaseBranch)
		if len(baseBranch) > 255 {
			add(fieldPrefix+".baseBranch", "must be at most 255 bytes")
		}
		for _, ref := range []struct {
			field string
			name  string
		}{
			{field: "workflowRef", name: target.WorkflowRef},
			{field: "policyPackRef", name: target.PolicyPackRef},
			{field: "scanName", name: target.ScanName},
		} {
			if problems := validation.IsDNS1123Subdomain(ref.name); len(problems) != 0 {
				add(fieldPrefix+"."+ref.field, "must be a valid DNS-1123 subdomain")
			}
		}
		scanName := strings.TrimSpace(target.ScanName)
		if previous, exists := seenScanNames[scanName]; scanName != "" && exists {
			add(fieldPrefix+".scanName", "must be unique; it duplicates scanTargets[%d].scanName", previous)
		} else if scanName != "" {
			seenScanNames[scanName] = index
		}
		displayName := strings.TrimSpace(target.DisplayName)
		if displayName == "" {
			add(fieldPrefix+".displayName", "is required")
		} else if len(displayName) > MaxSecurityProgramDisplayNameLength {
			add(fieldPrefix+".displayName", "must be at most %d bytes", MaxSecurityProgramDisplayNameLength)
		}
		if target.Priority < 0 {
			add(fieldPrefix+".priority", "must not be negative")
		}
	}
	return errs
}
