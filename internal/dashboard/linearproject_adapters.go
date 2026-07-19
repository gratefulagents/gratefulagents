package dashboard

import (
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// k8sLinearProjectToProto converts a K8s LinearProject to its proto representation.
func k8sLinearProjectToProto(lp *triggersv1alpha1.LinearProject) *platform.LinearProject {
	pb := &platform.LinearProject{
		Namespace:     lp.Namespace,
		Name:          lp.Name,
		ProjectId:     lp.Spec.ProjectID,
		ApprovedLabel: lp.Spec.ApprovedLabel,
		RepoUrl:       lp.Spec.Defaults.RepoURL,
		CreatedAtUnix: lp.CreationTimestamp.Unix(),
	}

	if lp.Spec.PollInterval.Duration != 0 {
		pb.PollInterval = lp.Spec.PollInterval.Duration.String()
	}

	if lp.Status.LastPollTime != nil {
		pb.LastPollTimeUnix = lp.Status.LastPollTime.Unix()
	}
	pb.IssuesProcessed = lp.Status.IssuesProcessed
	pb.LastError = lp.Status.LastError

	// Extract the Ready condition value.
	for _, c := range lp.Status.Conditions {
		if c.Type == triggersv1alpha1.ConditionLinearProjectReady {
			pb.ConditionReady = string(c.Status)
			break
		}
	}
	if pb.ConditionReady == "" {
		pb.ConditionReady = string(metav1.ConditionUnknown)
	}

	// Defaults for plan creation.
	d := lp.Spec.Defaults
	pb.BaseBranch = d.BaseBranch
	pb.Model = d.Model
	pb.Provider = providerOrDefault(d.Provider)
	pb.AuthMode = string(triggersv1alpha1.NormalizeAuthMode(string(d.AuthMode)))
	pb.Image = d.Image
	pb.ClaudeApiKeySecret = d.Secrets.ClaudeApiKey
	pb.OpenaiOauthSecret = d.Secrets.OpenAIOAuthSecret
	pb.GithubTokenSecret = d.Secrets.GithubToken
	for _, pk := range d.Secrets.ProviderKeys {
		pb.ProviderKeys = append(pb.ProviderKeys, &platform.ProviderKeyRef{
			Provider:   pk.Provider,
			SecretName: pk.SecretName,
			SecretKey:  pk.SecretKey,
		})
	}
	if d.Timeout.Duration != 0 {
		pb.Timeout = d.Timeout.Duration.String()
	}

	pb.AutoCreateTasks = lp.Spec.AutoCreateTasks
	pb.CustomInstructions = lp.Spec.Defaults.CustomInstructions
	pb.AllowedModels = append(pb.AllowedModels, d.AllowedModels...)
	pb.Defaults = crdDefaultsToProto(d)

	return pb
}
