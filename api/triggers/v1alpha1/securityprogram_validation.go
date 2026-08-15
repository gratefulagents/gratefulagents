/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/util/validation"
)

var securityProgramParameterNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

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
		targetURL := strings.TrimSpace(target.TargetURL)
		if (repositoryURL == "") == (targetURL == "") {
			add(fieldPrefix, "must set exactly one of repositoryURL or targetURL")
		}
		parsed, err := url.ParseRequestURI(repositoryURL)
		if len(repositoryURL) > MaxSecurityProgramURLLength {
			add(fieldPrefix+".repositoryURL", "must be at most %d bytes", MaxSecurityProgramURLLength)
		} else if repositoryURL != "" && (err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil) {
			add(fieldPrefix+".repositoryURL", "must be an absolute HTTPS URL without user information")
		}
		if targetURL != "" {
			if len(targetURL) > MaxSecurityProgramURLLength {
				add(fieldPrefix+".targetURL", "must be at most %d bytes", MaxSecurityProgramURLLength)
			} else if _, err := NormalizeSecurityScanTargetURL(targetURL); err != nil {
				add(fieldPrefix+".targetURL", "must be an HTTP(S) URL or bare domain without user information")
			}
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
		if len(target.ParameterValues) > MaxSecurityProgramTargetParameters {
			add(fieldPrefix+".parameterValues", "must contain at most %d entries", MaxSecurityProgramTargetParameters)
		}
		parameterNames := make([]string, 0, len(target.ParameterValues))
		for name := range target.ParameterValues {
			parameterNames = append(parameterNames, name)
		}
		sort.Strings(parameterNames)
		for _, name := range parameterNames {
			field := fmt.Sprintf("%s.parameterValues[%q]", fieldPrefix, name)
			if len(name) > MaxSecurityProgramParameterName || !securityProgramParameterNamePattern.MatchString(name) {
				add(field, "name must be an identifier of at most %d characters", MaxSecurityProgramParameterName)
			}
			if utf8.RuneCountInString(target.ParameterValues[name]) > MaxSecurityProgramParameterValue {
				add(field, "value must be at most %d characters", MaxSecurityProgramParameterValue)
			}
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
	case "", string(PoCEnvironmentMainnetFork), string(PoCEnvironmentProjectTestSuite),
		string(PoCEnvironmentLocalDevnet), string(PoCEnvironmentEither):
	default:
		add("pocEnvironment", "must be one of mainnet-fork, project-test-suite, local-devnet, either")
	}

	errs = append(errs, validateSecurityProgramImpacts(spec.InScopeImpacts, severitySystem)...)
	errs = append(errs, validateSecurityProgramVerbatimLists(spec)...)
	errs = append(errs, validateSecurityProgramAssets(spec.Assets)...)
	errs = append(errs, validateSecurityProgramKnownIssues(spec.KnownIssues)...)
	errs = append(errs, validateSecurityProgramSubmissionBudget(spec.SubmissionBudget)...)
	return errs
}

// validateSecurityProgramImpacts checks the transcribed impact clauses. A
// clause is only usable verbatim, so anything blank, over-long, or duplicated
// within one asset category is rejected rather than normalized.
func validateSecurityProgramImpacts(impacts []SecurityProgramImpact, severitySystem string) []SecurityWorkflowFieldError {
	var errs []SecurityWorkflowFieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, SecurityWorkflowFieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	if len(impacts) > MaxSecurityProgramImpacts {
		add("inScopeImpacts", "must contain at most %d impacts", MaxSecurityProgramImpacts)
	}
	seen := make(map[string]int, len(impacts))
	for index, entry := range impacts {
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
			if previous, exists := seen[key]; exists {
				add(field+".impact", "must be unique per assetType; it duplicates inScopeImpacts[%d].impact", previous)
			} else {
				seen[key] = index
			}
		}
		level := strings.TrimSpace(entry.Level)
		if !validSecurityProgramImpactLevels[level] {
			add(field+".level", "must be one of critical, high, medium, low")
			continue
		}
		// Sherlock judges only High and Medium; a transcribed low or critical
		// impact under that system means the transcription is wrong, not that
		// the program pays for one.
		if severitySystem == string(SeveritySystemSherlock) && (level == "low" || level == "critical") {
			add(field+".level", "sherlock judges only high and medium severities")
		}
	}
	return errs
}

// validateSecurityProgramVerbatimLists checks the exclusion and prohibited
// testing transcriptions.
func validateSecurityProgramVerbatimLists(spec SecurityProgramSpec) []SecurityWorkflowFieldError {
	var errs []SecurityWorkflowFieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, SecurityWorkflowFieldError{Field: field, Message: fmt.Sprintf(format, args...)})
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
	return errs
}

// validateSecurityProgramAssets checks the deployed-asset bindings. An asset
// that identifies nothing cannot be checked against deployed state.
func validateSecurityProgramAssets(assets []SecurityProgramAsset) []SecurityWorkflowFieldError {
	var errs []SecurityWorkflowFieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, SecurityWorkflowFieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	if len(assets) > MaxSecurityProgramAssets {
		add("assets", "must contain at most %d assets", MaxSecurityProgramAssets)
	}
	for index, asset := range assets {
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
		if repositoryURL != "" && !isAbsoluteHTTPSURL(repositoryURL) {
			add(field+".repositoryURL", "must be an absolute HTTPS URL without user information")
		}
	}
	return errs
}

func validateSecurityProgramKnownIssues(issues []SecurityProgramKnownIssue) []SecurityWorkflowFieldError {
	var errs []SecurityWorkflowFieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, SecurityWorkflowFieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	if len(issues) > MaxSecurityProgramKnownIssues {
		add("knownIssues", "must contain at most %d entries", MaxSecurityProgramKnownIssues)
	}
	for index, issue := range issues {
		field := fmt.Sprintf("knownIssues[%d]", index)
		if strings.TrimSpace(issue.Source) == "" {
			add(field+".source", "is required")
		}
		if strings.TrimSpace(issue.Summary) == "" {
			add(field+".summary", "is required")
		}
		if reference := strings.TrimSpace(issue.Reference); reference != "" && !isAbsoluteHTTPSURL(reference) {
			add(field+".reference", "must be an absolute HTTPS URL without user information")
		}
	}
	return errs
}

func validateSecurityProgramSubmissionBudget(budget *SecurityProgramSubmissionBudget) []SecurityWorkflowFieldError {
	if budget == nil {
		return nil
	}
	var errs []SecurityWorkflowFieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, SecurityWorkflowFieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	if budget.MaxPerPeriod < 0 {
		add("submissionBudget.maxPerPeriod", "must not be negative")
	}
	if budget.PeriodDays < 0 {
		add("submissionBudget.periodDays", "must not be negative")
	}
	if budget.PeriodDays > 0 && budget.MaxPerPeriod == 0 {
		add("submissionBudget.maxPerPeriod", "is required when periodDays is set")
	}
	return errs
}

// isAbsoluteHTTPSURL reports whether value is an absolute HTTPS URL with a
// host and no user information.
func isAbsoluteHTTPSURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
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
