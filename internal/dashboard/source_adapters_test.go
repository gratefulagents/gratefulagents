package dashboard

import (
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

func TestSourceAdaptersExposeAuthModeAndOAuthSecret(t *testing.T) {
	t.Parallel()

	project := &triggersv1alpha1.LinearProject{
		Spec: triggersv1alpha1.LinearProjectSpec{
			Defaults: triggersv1alpha1.AgentRunDefaults{
				AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
				Secrets:  triggersv1alpha1.AgentRunSecrets{OpenAIOAuthSecret: "openai-oauth"},
			},
		},
	}
	projectPB := k8sLinearProjectToProto(project)
	if projectPB.AuthMode != "oauth" || projectPB.OpenaiOauthSecret != "openai-oauth" {
		t.Fatalf("project auth mode/secret = %q/%q", projectPB.AuthMode, projectPB.OpenaiOauthSecret)
	}

	projectSource := &triggersv1alpha1.Project{
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName:     "Payments",
			KubernetesAdmin: true,
			Defaults: triggersv1alpha1.AgentRunDefaults{
				AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
				Secrets:  triggersv1alpha1.AgentRunSecrets{OpenAIOAuthSecret: "openai-oauth"},
			},
		},
	}
	projectSourcePB := k8sProjectToProto(projectSource)
	if projectSourcePB.AuthMode != "oauth" || projectSourcePB.OpenaiOauthSecret != "openai-oauth" {
		t.Fatalf("project source auth mode/secret = %q/%q", projectSourcePB.AuthMode, projectSourcePB.OpenaiOauthSecret)
	}
	if !projectSourcePB.KubernetesAdmin {
		t.Fatalf("project source KubernetesAdmin = false, want true")
	}

	cronSource := &triggersv1alpha1.Cron{
		Spec: triggersv1alpha1.CronSpec{
			Schedule: "0 * * * *",
			Prompt:   "Run maintenance",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
				Secrets:  triggersv1alpha1.AgentRunSecrets{OpenAIOAuthSecret: "openai-oauth"},
			},
		},
	}
	cronSourcePB := k8sCronToProto(cronSource)
	if cronSourcePB.AuthMode != "oauth" || cronSourcePB.OpenaiOauthSecret != "openai-oauth" {
		t.Fatalf("cron source auth mode/secret = %q/%q", cronSourcePB.AuthMode, cronSourcePB.OpenaiOauthSecret)
	}
}
