package dashboard

import (
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func k8sCronToProto(cr *triggersv1alpha1.Cron) *platform.Cron {
	pb := &platform.Cron{
		Namespace:         cr.Namespace,
		Name:              cr.Name,
		Schedule:          cr.Spec.Schedule,
		TimeZone:          cr.Spec.TimeZone,
		Suspend:           cr.Spec.Suspend,
		ConcurrencyPolicy: string(cr.Spec.ConcurrencyPolicy),
		Prompt:            cr.Spec.Prompt,
		RepoUrl:           cr.Spec.Defaults.RepoURL,
		RunsCreated:       cr.Status.RunsCreated,
		LastRunName:       cr.Status.LastRunName,
		LastError:         cr.Status.LastError,
		CreatedAtUnix:     cr.CreationTimestamp.Unix(),
	}

	if cr.Status.LastScheduleTime != nil {
		pb.LastScheduleTimeUnix = cr.Status.LastScheduleTime.Unix()
	}
	if cr.Status.NextScheduleTime != nil {
		pb.NextScheduleTimeUnix = cr.Status.NextScheduleTime.Unix()
	}

	for _, c := range cr.Status.Conditions {
		if c.Type == triggersv1alpha1.ConditionCronReady {
			pb.ConditionReady = string(c.Status)
			break
		}
	}
	if pb.ConditionReady == "" {
		pb.ConditionReady = string(metav1.ConditionUnknown)
	}

	d := cr.Spec.Defaults
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
	for _, key := range d.Secrets.ProviderKeys {
		pb.ProviderKeys = append(pb.ProviderKeys, &platform.ProviderKeyRef{
			Provider:   key.Provider,
			SecretName: key.SecretName,
			SecretKey:  key.SecretKey,
		})
	}
	pb.Defaults = crdDefaultsToProto(d)

	return pb
}

// crdDefaultsToProto maps the shared CRD trigger defaults to the canonical
// proto AgentRunDefaults message (Cron field 41).
func crdDefaultsToProto(d triggersv1alpha1.AgentRunDefaults) *platform.AgentRunDefaults {
	pb := &platform.AgentRunDefaults{
		RepoUrl:            d.RepoURL,
		AdditionalRepoUrls: append([]string(nil), d.AdditionalRepos...),
		BaseBranch:         d.BaseBranch,
		Image:              d.Image,
		Model:              d.Model,
		AllowedModels:      append([]string(nil), d.AllowedModels...),
		Provider:           providerOrDefault(d.Provider),
		AuthMode:           string(triggersv1alpha1.NormalizeAuthMode(string(d.AuthMode))),
		ReasoningLevel:     string(d.ReasoningLevel),
		OpenaiBaseUrl:      d.OpenAIBaseURL,
		OpenaiApi:          d.OpenAIAPI,
		CustomInstructions: d.CustomInstructions,
		ClaudeApiKeySecret: d.Secrets.ClaudeApiKey,
		OpenaiOauthSecret:  d.Secrets.OpenAIOAuthSecret,
		GithubTokenSecret:  d.Secrets.GithubToken,
		WorkflowMode:       string(d.WorkflowMode),
		ExecutionMode:      string(d.ExecutionMode),
	}
	if d.Timeout.Duration != 0 {
		pb.Timeout = d.Timeout.Duration.String()
	}
	for _, key := range d.Secrets.ProviderKeys {
		pb.ProviderKeys = append(pb.ProviderKeys, &platform.ProviderKeyRef{
			Provider:   key.Provider,
			SecretName: key.SecretName,
			SecretKey:  key.SecretKey,
		})
	}
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
	if d.ModeRef != nil {
		pb.ModeRef = d.ModeRef.Name
	}
	return pb
}
