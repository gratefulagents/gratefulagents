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
	return errs
}
