package dashboard

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func k8sGitHubRepositoryToProto(gh *triggersv1alpha1.GitHubRepository) *platform.GitHubRepository {
	pb := &platform.GitHubRepository{
		Namespace:       gh.Namespace,
		Name:            gh.Name,
		Owner:           gh.Spec.Owner,
		Repo:            gh.Spec.Repo,
		RepoUrl:         gh.Spec.Defaults.RepoURL,
		TriggerKeyword:  gh.Spec.TriggerKeyword,
		CreatedAtUnix:   gh.CreationTimestamp.Unix(),
		IssuesProcessed: gh.Status.IssuesProcessed,
		LastError:       gh.Status.LastError,
	}

	if gh.Status.LastPollTime != nil {
		pb.LastPollTimeUnix = gh.Status.LastPollTime.Unix()
	}

	for _, c := range gh.Status.Conditions {
		if c.Type == triggersv1alpha1.ConditionGitHubRepositoryReady {
			pb.ConditionReady = string(c.Status)
			break
		}
	}
	if pb.ConditionReady == "" {
		pb.ConditionReady = string(metav1.ConditionUnknown)
	}

	d := gh.Spec.Defaults
	pb.BaseBranch = d.BaseBranch
	pb.Model = d.Model
	pb.Provider = providerOrDefault(d.Provider)
	pb.AuthMode = string(triggersv1alpha1.NormalizeAuthMode(string(d.AuthMode)))
	pb.Image = d.Image
	pb.ClaudeApiKeySecret = d.Secrets.ClaudeApiKey
	pb.OpenaiOauthSecret = d.Secrets.OpenAIOAuthSecret
	pb.GithubTokenSecret = d.Secrets.GithubToken
	if d.Timeout.Duration != 0 {
		pb.Timeout = d.Timeout.Duration.String()
	}
	pb.CustomInstructions = d.CustomInstructions
	pb.AllowedModels = append(pb.AllowedModels, d.AllowedModels...)
	pb.Defaults = crdDefaultsToProto(d)
	pb.TriggerSettings = crdGitHubTriggerSettingsToProto(gh.Spec)
	if gh.Status.Maintainer != nil {
		pb.MaintainerStatus = &platform.GitHubRepositoryMaintainerStatus{
			RunName:           gh.Status.Maintainer.RunName,
			DispatchesToday:   gh.Status.Maintainer.DispatchesToday,
			LastReportState:   gh.Status.Maintainer.LastReportState,
			LastReportSummary: gh.Status.Maintainer.LastReportSummary,
		}
		if gh.Status.Maintainer.LastWakeTime != nil {
			pb.MaintainerStatus.LastWakeUnix = gh.Status.Maintainer.LastWakeTime.Unix()
		}
		if gh.Status.Maintainer.LastReportTime != nil {
			pb.MaintainerStatus.LastReportTimeUnix = gh.Status.Maintainer.LastReportTime.Unix()
		}
	}
	if gh.Spec.ReviewLoop != nil && gh.Spec.ReviewLoop.ReviewerDefaults != nil {
		pb.ReviewerDefaults = crdDefaultsToProto(*gh.Spec.ReviewLoop.ReviewerDefaults)
	}

	return pb
}

func crdGitHubTriggerSettingsToProto(spec triggersv1alpha1.GitHubRepositorySpec) *platform.GitHubRepositoryTriggerSettings {
	settings := &platform.GitHubRepositoryTriggerSettings{
		WebhookSecret:          spec.WebhookSecret,
		TriggerKeyword:         spec.TriggerKeyword,
		CancelRunsOnIssueClose: spec.CancelRunsOnIssueClose,
	}
	if spec.PollInterval.Duration != 0 {
		settings.PollInterval = spec.PollInterval.Duration.String()
	}
	if spec.Auth != nil {
		settings.AuthAllowedUsers = append(settings.AuthAllowedUsers, spec.Auth.AllowedUsers...)
		settings.AuthDenyUsers = append(settings.AuthDenyUsers, spec.Auth.DenyUsers...)
	}
	if spec.ReviewLoop != nil {
		settings.ReviewLoopDisabled = &spec.ReviewLoop.Disabled
		settings.ReviewLoopMaxRounds = spec.ReviewLoop.MaxRounds
		if spec.ReviewLoop.ReviewerModeRef != nil {
			settings.ReviewerModeRef = spec.ReviewLoop.ReviewerModeRef.Name
			settings.ReviewerModeVersion = spec.ReviewLoop.ReviewerModeRef.Version
			settings.ReviewerModeChannel = spec.ReviewLoop.ReviewerModeRef.Channel
		}
	}
	maintainerEnabled := spec.Maintainer != nil && !spec.Maintainer.Disabled
	settings.MaintainerEnabled = &maintainerEnabled
	if spec.Maintainer != nil {
		settings.MaintainerMaxConcurrentDispatches = spec.Maintainer.MaxConcurrentDispatches
		settings.MaintainerMaxDispatchesPerDay = spec.Maintainer.MaxDispatchesPerDay
		settings.MaintainerModel = spec.Maintainer.Model
		settings.MaintainerAllowPrMerge = spec.Maintainer.AllowPullRequestMerge
		if spec.Maintainer.StandupInterval != nil {
			settings.MaintainerStandupInterval = spec.Maintainer.StandupInterval.Duration.String()
		}
		if spec.Maintainer.ModeRef != nil {
			settings.MaintainerModeRef = spec.Maintainer.ModeRef.Name
		}
	}
	return settings
}

func protoGitHubTriggerSettingsToCRD(pb *platform.GitHubRepositoryTriggerSettings) (metav1.Duration, string, string, bool, *triggersv1alpha1.TriggerAuth, *triggersv1alpha1.ReviewLoopSpec, *triggersv1alpha1.MaintainerSpec, error) {
	if pb == nil {
		return metav1.Duration{}, "", "", false, nil, nil, nil, nil
	}
	var pollInterval metav1.Duration
	if value := trim(pb.GetPollInterval()); value != "" {
		if err := pollInterval.UnmarshalJSON([]byte(strconv.Quote(value))); err != nil {
			return metav1.Duration{}, "", "", false, nil, nil, nil, fmt.Errorf("invalid poll_interval %q: %w", value, err)
		}
		if pollInterval.Duration <= 0 {
			return metav1.Duration{}, "", "", false, nil, nil, nil, fmt.Errorf("poll_interval must be greater than zero")
		}
	}
	authAllowedUsers := nonEmptyTrimmedStrings(pb.GetAuthAllowedUsers())
	authDenyUsers := nonEmptyTrimmedStrings(pb.GetAuthDenyUsers())
	var auth *triggersv1alpha1.TriggerAuth
	if len(authAllowedUsers) > 0 || len(authDenyUsers) > 0 {
		auth = &triggersv1alpha1.TriggerAuth{
			AllowedUsers: authAllowedUsers,
			DenyUsers:    authDenyUsers,
		}
	}

	reviewerMode := platformv1alpha1.ModeRef{
		Name:    trim(pb.GetReviewerModeRef()),
		Version: trim(pb.GetReviewerModeVersion()),
		Channel: trim(pb.GetReviewerModeChannel()),
	}
	if reviewerMode.Name == "" && (reviewerMode.Version != "" || reviewerMode.Channel != "") {
		return metav1.Duration{}, "", "", false, nil, nil, nil, fmt.Errorf("reviewer_mode_ref is required when reviewer mode version or channel is set")
	}
	if pb.GetReviewLoopMaxRounds() < 0 {
		return metav1.Duration{}, "", "", false, nil, nil, nil, fmt.Errorf("review_loop_max_rounds must be zero or greater")
	}
	var reviewerModeRef *platformv1alpha1.ModeRef
	if reviewerMode.Name != "" {
		reviewerModeRef = &reviewerMode
	}
	var reviewLoop *triggersv1alpha1.ReviewLoopSpec
	if pb.ReviewLoopDisabled != nil || pb.GetReviewLoopMaxRounds() > 0 || reviewerModeRef != nil {
		disabled := true
		if pb.ReviewLoopDisabled != nil {
			disabled = pb.GetReviewLoopDisabled()
		}
		reviewLoop = &triggersv1alpha1.ReviewLoopSpec{
			Disabled:        disabled,
			MaxRounds:       pb.GetReviewLoopMaxRounds(),
			ReviewerModeRef: reviewerModeRef,
		}
	}

	var maintainer *triggersv1alpha1.MaintainerSpec
	if pb.MaintainerEnabled != nil && pb.GetMaintainerEnabled() {
		if pb.GetMaintainerMaxConcurrentDispatches() < 0 {
			return metav1.Duration{}, "", "", false, nil, nil, nil, fmt.Errorf("maintainer_max_concurrent_dispatches must be zero or greater")
		}
		if pb.GetMaintainerMaxDispatchesPerDay() < 0 {
			return metav1.Duration{}, "", "", false, nil, nil, nil, fmt.Errorf("maintainer_max_dispatches_per_day must be zero or greater")
		}
		maintainer = &triggersv1alpha1.MaintainerSpec{
			Model:                   trim(pb.GetMaintainerModel()),
			MaxConcurrentDispatches: pb.GetMaintainerMaxConcurrentDispatches(),
			MaxDispatchesPerDay:     pb.GetMaintainerMaxDispatchesPerDay(),
			AllowPullRequestMerge:   pb.GetMaintainerAllowPrMerge(),
		}
		if modeRef := trim(pb.GetMaintainerModeRef()); modeRef != "" {
			maintainer.ModeRef = &platformv1alpha1.ModeRef{Name: modeRef}
		}
		if value := trim(pb.GetMaintainerStandupInterval()); value != "" {
			duration, err := time.ParseDuration(value)
			if err != nil {
				return metav1.Duration{}, "", "", false, nil, nil, nil, fmt.Errorf("invalid maintainer_standup_interval %q: %w", value, err)
			}
			if duration <= 0 {
				return metav1.Duration{}, "", "", false, nil, nil, nil, fmt.Errorf("maintainer_standup_interval must be greater than zero")
			}
			maintainer.StandupInterval = &metav1.Duration{Duration: duration}
		}
	}
	return pollInterval,
		trim(pb.GetWebhookSecret()),
		trim(pb.GetTriggerKeyword()),
		pb.GetCancelRunsOnIssueClose(),
		auth,
		reviewLoop,
		maintainer,
		nil
}

func nonEmptyTrimmedStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := trim(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func trim(value string) string {
	return strings.TrimSpace(value)
}
