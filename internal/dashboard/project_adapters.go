package dashboard

import (
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"google.golang.org/protobuf/types/known/timestamppb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func k8sProjectToProto(p *triggersv1alpha1.Project) *platform.Project {
	pb := &platform.Project{
		Namespace:       p.Namespace,
		Name:            p.Name,
		DisplayName:     p.Spec.DisplayName,
		RepoUrl:         p.Spec.Defaults.RepoURL,
		CreatedAtUnix:   p.CreationTimestamp.Unix(),
		KubernetesAdmin: p.Spec.KubernetesAdmin,
	}
	pb.ReviewLoopDisabled = p.Spec.ReviewLoop == nil || p.Spec.ReviewLoop.Disabled

	d := p.Spec.Defaults
	pb.AdditionalRepoUrls = append(pb.AdditionalRepoUrls, d.AdditionalRepos...)
	pb.BaseBranch = d.BaseBranch
	pb.Model = d.Model
	pb.Provider = providerOrDefault(d.Provider)
	pb.AuthMode = string(triggersv1alpha1.NormalizeAuthMode(string(d.AuthMode)))
	pb.ReasoningLevel = string(d.ReasoningLevel)
	pb.Image = d.Image
	pb.ClaudeApiKeySecret = d.Secrets.ClaudeApiKey
	pb.OpenaiOauthSecret = d.Secrets.OpenAIOAuthSecret
	pb.GithubTokenSecret = d.Secrets.GithubToken
	if d.Timeout.Duration != 0 {
		pb.Timeout = d.Timeout.Duration.String()
	}
	pb.CustomInstructions = d.CustomInstructions
	pb.AllowedModels = append(pb.AllowedModels, d.AllowedModels...)
	if d.RuntimeProfileRef != nil {
		pb.RuntimeProfileRef = d.RuntimeProfileRef.Name
	}
	if d.MCPPolicyRef != nil {
		pb.McpPolicyRef = d.MCPPolicyRef.Name
	}
	for _, ref := range d.MCPServerRefs {
		pb.McpServerRefs = append(pb.McpServerRefs, ref.Name)
	}
	for _, ref := range d.SkillRefs {
		pb.SkillRefs = append(pb.SkillRefs, ref.Name)
	}
	for _, key := range d.Secrets.ProviderKeys {
		pb.ProviderKeys = append(pb.ProviderKeys, &platform.ProviderKeyRef{
			Provider:   key.Provider,
			SecretName: key.SecretName,
			SecretKey:  key.SecretKey,
		})
	}

	statuses := make(map[string]triggersv1alpha1.ProjectTriggerStatus, len(p.Status.Triggers))
	for _, status := range p.Status.Triggers {
		statuses[status.Name] = status
	}
	for _, trigger := range p.Spec.Triggers {
		pb.Triggers = append(pb.Triggers, projectTriggerToProto(trigger, statuses[trigger.Name]))
	}

	return pb
}

func projectTriggerToProto(trigger triggersv1alpha1.ProjectTrigger, status triggersv1alpha1.ProjectTriggerStatus) *platform.ProjectTrigger {
	pb := &platform.ProjectTrigger{
		Name:               trigger.Name,
		Type:               string(trigger.Type),
		ObservedGeneration: status.ObservedGeneration,
		LastError:          status.LastError,
	}
	if trigger.Enabled != nil {
		enabled := *trigger.Enabled
		pb.Enabled = &enabled
	}
	if trigger.GitHub != nil {
		pb.Github = &platform.GitHubProjectTrigger{
			ConnectionRef:    trigger.GitHub.ConnectionRef.Name,
			Owner:            trigger.GitHub.Owner,
			Repo:             trigger.GitHub.Repo,
			Issues:           trigger.GitHub.Issues,
			Comments:         trigger.GitHub.Comments,
			TriggerKeyword:   trigger.GitHub.TriggerKeyword,
			AuthAllowedUsers: triggerAuthAllowedUsers(trigger.GitHub.Auth),
			AuthDenyUsers:    triggerAuthDenyUsers(trigger.GitHub.Auth),
		}
		if trigger.GitHub.PollInterval.Duration != 0 {
			pb.Github.PollInterval = trigger.GitHub.PollInterval.Duration.String()
		}
		maintainerEnabled := trigger.GitHub.Maintainer != nil && !trigger.GitHub.Maintainer.Disabled
		pb.Github.MaintainerEnabled = &maintainerEnabled
		if maintainer := trigger.GitHub.Maintainer; maintainer != nil {
			pb.Github.MaintainerMaxConcurrentDispatches = maintainer.MaxConcurrentDispatches
			pb.Github.MaintainerMaxDispatchesPerDay = maintainer.MaxDispatchesPerDay
			pb.Github.MaintainerModel = maintainer.Model
			pb.Github.MaintainerAllowPrMerge = maintainer.AllowPullRequestMerge
			if maintainer.StandupInterval != nil {
				pb.Github.MaintainerStandupInterval = maintainer.StandupInterval.Duration.String()
			}
			if maintainer.ModeRef != nil {
				pb.Github.MaintainerModeRef = maintainer.ModeRef.Name
			}
		}
	}
	if trigger.Slack != nil {
		pb.Slack = &platform.SlackProjectTrigger{
			ConnectionRef:      trigger.Slack.ConnectionRef.Name,
			Channel:            trigger.Slack.Channel,
			ChannelReplyMode:   string(trigger.Slack.ChannelReplyMode),
			Commanders:         append([]string(nil), trigger.Slack.Commanders...),
			SessionIdleMinutes: trigger.Slack.SessionIdleMinutes,
		}
	}
	if trigger.Cron != nil {
		pb.Cron = &platform.CronProjectTrigger{
			Schedule:          trigger.Cron.Schedule,
			TimeZone:          trigger.Cron.TimeZone,
			ConcurrencyPolicy: string(trigger.Cron.ConcurrencyPolicy),
			Prompt:            trigger.Cron.Prompt,
		}
	}
	if trigger.Linear != nil {
		pb.Linear = &platform.LinearProjectTrigger{
			ConnectionRef: trigger.Linear.ConnectionRef.Name,
			ProjectId:     trigger.Linear.ProjectID,
			TeamId:        trigger.Linear.TeamID,
			ApprovedLabel: trigger.Linear.ApprovedLabel,
			AutoCreate:    trigger.Linear.AutoCreate,
		}
		if trigger.Linear.PollInterval.Duration != 0 {
			pb.Linear.PollInterval = trigger.Linear.PollInterval.Duration.String()
		}
	}
	for _, condition := range status.Conditions {
		pb.Conditions = append(pb.Conditions, projectTriggerConditionToProto(condition))
	}
	if status.LastActivityTime != nil {
		pb.LastActivityTime = timestamppb.New(status.LastActivityTime.Time)
	}
	if status.NextActivityTime != nil {
		pb.NextActivityTime = timestamppb.New(status.NextActivityTime.Time)
	}
	return pb
}

func projectTriggerConditionToProto(condition metav1.Condition) *platform.ProjectTriggerCondition {
	return &platform.ProjectTriggerCondition{
		Type:               condition.Type,
		Status:             string(condition.Status),
		Reason:             condition.Reason,
		Message:            condition.Message,
		LastTransitionTime: timestamppb.New(condition.LastTransitionTime.Time),
	}
}

func triggerAuthAllowedUsers(auth *triggersv1alpha1.TriggerAuth) []string {
	if auth == nil {
		return nil
	}
	return append([]string(nil), auth.AllowedUsers...)
}

func triggerAuthDenyUsers(auth *triggersv1alpha1.TriggerAuth) []string {
	if auth == nil {
		return nil
	}
	return append([]string(nil), auth.DenyUsers...)
}
