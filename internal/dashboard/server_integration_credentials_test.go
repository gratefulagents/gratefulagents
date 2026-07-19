package dashboard

import (
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// testIntegrationName is the free-form integration used across these tests.
const testIntegrationName = "grafana"

func TestValidateIntegrationName(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{testIntegrationName, testIntegrationName, false},
		{"  Grafana  ", "grafana", false},
		{"my-tool-2", "my-tool-2", false},
		{"", "", true},
		{"-bad", "", true},
		{"bad-", "", true},
		{"has space", "", true},
		{"github", "", true},    // reserved
		{"anthropic", "", true}, // reserved
	}
	for _, tc := range cases {
		got, err := validateIntegrationName(tc.in)
		if tc.wantErr != (err != nil) {
			t.Fatalf("validateIntegrationName(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
		}
		if !tc.wantErr && got != tc.want {
			t.Fatalf("validateIntegrationName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIntegrationCredentialLifecycle(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()

	// Write a grafana integration through the credentials RPC.
	resp, err := srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{
		Integrations: []*platform.IntegrationCredentialUpdate{{
			Name:    "Grafana",
			Entries: map[string]string{"url": "https://g.example.com", "token": "glsa_abc", "empty": ""},
		}},
	})
	if err != nil {
		t.Fatalf("UpdateMyCredentials() error = %v", err)
	}
	if len(resp.Integrations) != 1 || resp.Integrations[0].Name != testIntegrationName {
		t.Fatalf("integrations = %+v, want one named grafana", resp.Integrations)
	}
	if got := resp.Integrations[0].Keys; len(got) != 2 || got[0] != "token" || got[1] != "url" {
		t.Fatalf("keys = %v, want [token url] (empty value skipped)", got)
	}
	if resp.GithubTokenPresent {
		t.Fatal("integration must not masquerade as a built-in provider")
	}

	// Clear one key.
	resp, err = srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{
		Integrations: []*platform.IntegrationCredentialUpdate{{Name: testIntegrationName, ClearKeys: []string{"token"}}},
	})
	if err != nil {
		t.Fatalf("UpdateMyCredentials(clear) error = %v", err)
	}
	if got := resp.Integrations[0].Keys; len(got) != 1 || got[0] != "url" {
		t.Fatalf("keys after clear = %v, want [url]", got)
	}

	// Delete the whole integration.
	resp, err = srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{
		Integrations: []*platform.IntegrationCredentialUpdate{{Name: testIntegrationName, Delete: true}},
	})
	if err != nil {
		t.Fatalf("UpdateMyCredentials(delete) error = %v", err)
	}
	if len(resp.Integrations) != 0 {
		t.Fatalf("integrations after delete = %+v, want none", resp.Integrations)
	}

	// Reserved names are rejected.
	if _, err := srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{
		Integrations: []*platform.IntegrationCredentialUpdate{{Name: "openai", Entries: map[string]string{"k": "v"}}},
	}); err == nil {
		t.Fatal("expected reserved-name error")
	}
}

func TestIntegrationCredentialsExcludeBuiltinsFromList(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	namespace := seedSlackGitHubCredential(t, srv, ctx)

	states := srv.integrationCredentialStates(ctx, namespace)
	if len(states) != 0 {
		t.Fatalf("built-in github credential leaked into integrations: %+v", states)
	}
}

func TestUpdateSlackAgentRoundtripsMCPServerAndSkillRefs(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	seedSlackGitHubCredential(t, srv, ctx)
	seedSlackAPIKeyCredential(t, srv, ctx, "anthropic")

	req := &platform.UpdateSlackAgentRequest{
		Name:                "me",
		BotToken:            "xoxb-1",
		AppToken:            "xapp-1",
		Model:               "claude-sonnet-4-6",
		Provider:            "anthropic",
		UseSavedCredentials: true,
		McpServerRefs:       []string{testIntegrationName, " grafana ", "browser-playwright", ""},
		SkillRefs:           []string{"pdf", " pdf ", ""},
	}
	agent, err := srv.UpdateSlackAgent(ctx, req)
	if err != nil {
		t.Fatalf("UpdateSlackAgent() error = %v", err)
	}
	if got := agent.McpServerRefs; len(got) != 2 || got[0] != testIntegrationName || got[1] != "browser-playwright" {
		t.Fatalf("McpServerRefs = %v, want deduped [grafana browser-playwright]", got)
	}
	if got := agent.SkillRefs; len(got) != 1 || got[0] != "pdf" {
		t.Fatalf("SkillRefs = %v, want deduped [pdf]", got)
	}

	// A second save without refs clears them (form owns the whole list) — and a
	// save WITH refs must persist through re-reads (the old wipe bug).
	req.McpServerRefs = []string{testIntegrationName}
	req.SkillRefs = nil
	if _, err := srv.UpdateSlackAgent(ctx, req); err != nil {
		t.Fatalf("UpdateSlackAgent(second) error = %v", err)
	}
	list, err := srv.ListSlackAgents(ctx, &platform.ListSlackAgentsRequest{})
	if err != nil {
		t.Fatalf("ListSlackAgents() error = %v", err)
	}
	if len(list.Agents) != 1 || len(list.Agents[0].McpServerRefs) != 1 || list.Agents[0].McpServerRefs[0] != testIntegrationName {
		t.Fatalf("persisted refs = %+v, want [grafana]", list.Agents)
	}
	if len(list.Agents[0].SkillRefs) != 0 {
		t.Fatalf("skill refs must be cleared on save without them, got %+v", list.Agents[0].SkillRefs)
	}
}

func TestUpdateSlackAgentRoundtripsImage(t *testing.T) {
	srv := slackAgentTestServer(t)
	ctx := slackActorContext()
	namespace := seedSlackGitHubCredential(t, srv, ctx)
	seedSlackAPIKeyCredential(t, srv, ctx, "anthropic")

	req := &platform.UpdateSlackAgentRequest{
		Name:                "me",
		BotToken:            "xoxb-1",
		AppToken:            "xapp-1",
		Model:               "claude-sonnet-4-6",
		Provider:            "anthropic",
		UseSavedCredentials: true,
		Image:               "  docker.io/library/ruby:3.4  ",
	}
	agent, err := srv.UpdateSlackAgent(ctx, req)
	if err != nil {
		t.Fatalf("UpdateSlackAgent() error = %v", err)
	}
	if agent.Image != "docker.io/library/ruby:3.4" {
		t.Fatalf("Image = %q, want trimmed ruby image", agent.Image)
	}

	// Both the connector image and the child-run default must be set.
	cr := &triggersv1alpha1.SlackAgent{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "me"}, cr); err != nil {
		t.Fatalf("read SlackAgent CR: %v", err)
	}
	if cr.Spec.Image != "docker.io/library/ruby:3.4" {
		t.Fatalf("Spec.Image = %q", cr.Spec.Image)
	}
	if cr.Spec.Defaults.Image != "docker.io/library/ruby:3.4" {
		t.Fatalf("Spec.Defaults.Image = %q", cr.Spec.Defaults.Image)
	}

	// The form owns the field wholesale: an empty save resets to default.
	req.Image = ""
	if _, err := srv.UpdateSlackAgent(ctx, req); err != nil {
		t.Fatalf("UpdateSlackAgent(clear) error = %v", err)
	}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "me"}, cr); err != nil {
		t.Fatalf("re-read SlackAgent CR: %v", err)
	}
	if cr.Spec.Image != "" || cr.Spec.Defaults.Image != "" {
		t.Fatalf("image not cleared: spec=%q defaults=%q", cr.Spec.Image, cr.Spec.Defaults.Image)
	}
}
