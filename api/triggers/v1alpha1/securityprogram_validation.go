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
	errs = append(errs, validateSecurityProgramScope(spec)...)
	return errs
}

// validateSecurityProgramScope validates the typed, machine-readable scope
// facts. Every field is optional so existing programs keep validating, but a
// field that is present must be usable without re-reading the prose snapshot.
func validateSecurityProgramScope(spec SecurityProgramSpec) []SecurityWorkflowFieldError {
	var errs []SecurityWorkflowFieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, SecurityWorkflowFieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	severitySystem := strings.TrimSpace(spec.SeveritySystem)
	if severitySystem != "" && !validSecurityProgramSeveritySystems[SecurityProgramSeveritySystem(severitySystem)] {
		add("severitySystem", "must be one of %s", strings.Join(securityProgramSeveritySystemNames(), ", "))
	}
	if primacy := strings.TrimSpace(spec.Primacy); primacy != "" &&
		primacy != string(PrimacyImpact) && primacy != string(PrimacyRules) {
		add("primacy", "must be either impact or rules")
	}
	switch strings.TrimSpace(spec.PoCEnvironment) {
	case "", string(PoCEnvironmentMainnetFork), string(PoCEnvironmentProjectTestSuite), string(PoCEnvironmentEither):
	default:
		add("pocEnvironment", "must be one of mainnet-fork, project-test-suite, either")
	}

	if len(spec.InScopeImpacts) > MaxSecurityProgramImpacts {
		add("inScopeImpacts", "must contain at most %d impacts", MaxSecurityProgramImpacts)
	}
	seenImpacts := make(map[string]int, len(spec.InScopeImpacts))
	for index, entry := range spec.InScopeImpacts {
		field := fmt.Sprintf("inScopeImpacts[%d]", index)
		impact := strings.TrimSpace(entry.Impact)
		switch {
		case impact == "":
			add(field+".impact", "is required")
		case utf8.RuneCountInString(entry.Impact) > MaxSecurityProgramImpactLength:
			add(field+".impact", "must be at most %d characters", MaxSecurityProgramImpactLength)
		default:
			// The same clause legitimately repeats across asset categories
			// (a smart-contract impact and a blockchain impact can share
			// wording), so uniqueness is per category.
			key := strings.TrimSpace(entry.AssetType) + "\x00" + impact
			if previous, exists := seenImpacts[key]; exists {
				add(field+".impact", "must be unique per assetType; it duplicates inScopeImpacts[%d].impact", previous)
			} else {
				seenImpacts[key] = index
			}
		}
		level := strings.TrimSpace(entry.Level)
		if !validSecurityProgramImpactLevels[level] {
			add(field+".level", "must be one of critical, high, medium, low")
			continue
		}
		// Sherlock judges only High and Medium; a transcribed low impact
		// under that system means the transcription is wrong, not that the
		// program pays for lows.
		if severitySystem == string(SeveritySystemSherlock) && (level == "low" || level == "critical") {
			add(field+".level", "sherlock judges only high and medium severities")
		}
	}

	if len(spec.OutOfScope) > MaxSecurityProgramExclusions {
		add("outOfScope", "must contain at most %d exclusions", MaxSecurityProgramExclusions)
	}
	for index, exclusion := range spec.OutOfScope {
		if strings.TrimSpace(exclusion) == "" {
			add(fmt.Sprintf("outOfScope[%d]", index), "must not be blank")
		}
	}
	if len(spec.ProhibitedTesting) > MaxSecurityProgramProhibitedTesting {
		add("prohibitedTesting", "must contain at most %d entries", MaxSecurityProgramProhibitedTesting)
	}
	for index, entry := range spec.ProhibitedTesting {
		if strings.TrimSpace(entry) == "" {
			add(fmt.Sprintf("prohibitedTesting[%d]", index), "must not be blank")
		}
	}

	if len(spec.Assets) > MaxSecurityProgramAssets {
		add("assets", "must contain at most %d assets", MaxSecurityProgramAssets)
	}
	for index, asset := range spec.Assets {
		field := fmt.Sprintf("assets[%d]", index)
		chainID := strings.TrimSpace(asset.ChainID)
		address := strings.TrimSpace(asset.Address)
		repositoryURL := strings.TrimSpace(asset.RepositoryURL)
		if chainID == "" && address == "" && repositoryURL == "" {
			add(field, "must identify an asset by chainID and address, or by repositoryURL")
		}
		if address != "" && chainID == "" {
			add(field+".chainID", "is required when address is set")
		}
		if repositoryURL != "" {
			parsed, err := url.ParseRequestURI(repositoryURL)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
				add(field+".repositoryURL", "must be an absolute HTTPS URL without user information")
			}
		}
	}

	if len(spec.KnownIssues) > MaxSecurityProgramKnownIssues {
		add("knownIssues", "must contain at most %d entries", MaxSecurityProgramKnownIssues)
	}
	for index, issue := range spec.KnownIssues {
		field := fmt.Sprintf("knownIssues[%d]", index)
		if strings.TrimSpace(issue.Source) == "" {
			add(field+".source", "is required")
		}
		if strings.TrimSpace(issue.Summary) == "" {
			add(field+".summary", "is required")
		}
		if reference := strings.TrimSpace(issue.Reference); reference != "" {
			parsed, err := url.ParseRequestURI(reference)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
				add(field+".reference", "must be an absolute HTTPS URL without user information")
			}
		}
	}

	if budget := spec.SubmissionBudget; budget != nil {
		if budget.MaxPerPeriod < 0 {
			add("submissionBudget.maxPerPeriod", "must not be negative")
		}
		if budget.PeriodDays < 0 {
			add("submissionBudget.periodDays", "must not be negative")
		}
		if budget.PeriodDays > 0 && budget.MaxPerPeriod == 0 {
			add("submissionBudget.maxPerPeriod", "is required when periodDays is set")
		}
	}
	return errs
}

var validSecurityProgramSeveritySystems = map[SecurityProgramSeveritySystem]bool{
	SeveritySystemImmunefiV23:        true,
	SeveritySystemCode4rena:          true,
	SeveritySystemSherlock:           true,
	SeveritySystemCantina:            true,
	SeveritySystemEthereumFoundation: true,
	SeveritySystemCustom:             true,
}

var validSecurityProgramImpactLevels = map[string]bool{
	"critical": true,
	"high":     true,
	"medium":   true,
	"low":      true,
}

func securityProgramSeveritySystemNames() []string {
	return []string{
		string(SeveritySystemImmunefiV23),
		string(SeveritySystemCode4rena),
		string(SeveritySystemSherlock),
		string(SeveritySystemCantina),
		string(SeveritySystemEthereumFoundation),
		string(SeveritySystemCustom),
	}
}
