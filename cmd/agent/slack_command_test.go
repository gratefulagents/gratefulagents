package main

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var dnsLabelRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func TestConversationRunName(t *testing.T) {
	now := time.Unix(1700000000, 0)
	a := conversationRunName("alice", "DBOTDM", "", now)
	b := conversationRunName("alice", "DBOTDM", "", now)
	if a != b {
		t.Fatalf("conversationRunName not deterministic for fixed time: %q != %q", a, b)
	}
	if len(a) > 63 || !dnsLabelRe.MatchString(a) {
		t.Fatalf("conversationRunName %q is not a valid DNS-1123 label", a)
	}

	// Different channels (conversations) must map to different runs.
	if c := conversationRunName("alice", "DALICE", "", now); c == a {
		t.Fatal("different channel produced the same run name")
	}
	// Different thread keys (channel threads) must differ.
	if c := conversationRunName("alice", "CTEAM", "1700000000.0002", now); c == a {
		t.Fatal("different thread key produced the same run name")
	}
	// Different agents must not collide.
	if c := conversationRunName("bob", "DBOTDM", "", now); c == a {
		t.Fatal("different agent produced the same run name")
	}
	// A later epoch (idle rollover) must produce a distinct run name.
	if c := conversationRunName("alice", "DBOTDM", "", now.Add(time.Second)); c == a {
		t.Fatal("different epoch produced the same run name")
	}
}

func TestIsTerminalPhase(t *testing.T) {
	terminal := []platformv1alpha1.AgentRunPhase{
		platformv1alpha1.AgentRunPhaseSucceeded,
		platformv1alpha1.AgentRunPhaseFailed,
		platformv1alpha1.AgentRunPhaseCancelled,
	}
	for _, p := range terminal {
		if !isTerminalPhase(p) {
			t.Errorf("isTerminalPhase(%s) = false, want true", p)
		}
	}
	nonTerminal := []platformv1alpha1.AgentRunPhase{
		platformv1alpha1.AgentRunPhasePending,
		platformv1alpha1.AgentRunPhaseProvisioning,
		platformv1alpha1.AgentRunPhaseRunning,
		platformv1alpha1.AgentRunPhaseQuestion,
		platformv1alpha1.AgentRunPhasePaused,
	}
	for _, p := range nonTerminal {
		if isTerminalPhase(p) {
			t.Errorf("isTerminalPhase(%s) = true, want false", p)
		}
	}
}

func TestSlackRunFailureMessageRedactsPublicDetail(t *testing.T) {
	run := &platformv1alpha1.AgentRun{}
	run.Status.LastError = "provider failed with secret-token-123"

	public := slackRunFailureMessage(run, true)
	if strings.Contains(public, "secret-token-123") {
		t.Fatalf("public failure leaked detail: %q", public)
	}
	private := slackRunFailureMessage(run, false)
	if !strings.Contains(private, "secret-token-123") {
		t.Fatalf("private owner failure omitted detail: %q", private)
	}
}

func TestTurnGateSharedPerRun(t *testing.T) {
	o := &slackOrchestrator{turnGates: map[string]*sync.Mutex{}}
	if o.turnGate("run-a") != o.turnGate("run-a") {
		t.Fatal("same run received different turn gates")
	}
	if o.turnGate("run-a") == o.turnGate("run-b") {
		t.Fatal("different runs shared one turn gate")
	}
}

func TestSlackCurrentDefaultsUsesSlackMode(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
	}
	agent := &triggersv1alpha1.SlackAgent{}
	agent.Name = "me"
	agent.Namespace = "ns"
	agent.Spec.Defaults.WorkflowMode = platformv1alpha1.WorkflowModeChat
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build()
	o := &slackOrchestrator{crdClient: c, namespace: "ns", agentName: "me"}

	got := o.currentDefaults(context.Background())
	if got.WorkflowMode != platformv1alpha1.WorkflowModeAuto {
		t.Fatalf("WorkflowMode = %q, want auto", got.WorkflowMode)
	}
	if got.ModeRef == nil || got.ModeRef.Name != slackModeName {
		t.Fatalf("ModeRef = %#v, want slack mode", got.ModeRef)
	}
}

func TestSlackCurrentDefaultsPreservesConfiguredMode(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
	}
	agent := &triggersv1alpha1.SlackAgent{}
	agent.Name = "me"
	agent.Namespace = "ns"
	agent.Spec.Defaults.ModeRef = &platformv1alpha1.ModeRef{
		Name: "custom", Version: "v2", Channel: "stable",
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build()
	o := &slackOrchestrator{crdClient: c, namespace: "ns", agentName: "me"}

	got := o.currentDefaults(context.Background())
	if got.ModeRef == nil || got.ModeRef.Name != "custom" || got.ModeRef.Version != "v2" || got.ModeRef.Channel != "stable" {
		t.Fatalf("ModeRef = %#v, want configured custom mode", got.ModeRef)
	}
}

func TestSlackCreateRunMountsSavedGitHubTokenSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
	}

	agent := &triggersv1alpha1.SlackAgent{}
	agent.Name = "me"
	agent.Namespace = "ns"
	agent.Spec.Defaults = triggersv1alpha1.AgentRunDefaults{
		Model:    "claude-opus-4.8",
		Provider: triggersv1alpha1.ProviderCopilot,
		AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
		Secrets: triggersv1alpha1.AgentRunSecrets{
			OpenAIOAuthSecret: "usercred-copilot",
		},
	}
	githubSecret := &corev1.Secret{}
	githubSecret.Name = slackSavedGitHubSecretName
	githubSecret.Namespace = "ns"
	githubSecret.Data = map[string][]byte{slackSavedGitHubTokenKey: []byte("gh-token")}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent, githubSecret).Build()
	o := &slackOrchestrator{crdClient: c, namespace: "ns", agentName: "me", tokensSecret: "me-slack"}

	if err := o.createRun(context.Background(), "slack-test-run", internalslack.Decision{
		ChannelID: "D123",
		ThreadTS:  "1782815689.940749",
		Text:      "hello",
	}, "hello"); err != nil {
		t.Fatalf("createRun() error = %v", err)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "slack-test-run"}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	if run.Spec.Secrets == nil {
		t.Fatal("AgentRun secrets nil")
	}
	if got := run.Spec.Secrets.GitHubTokenSecret; got != slackSavedGitHubSecretName {
		t.Fatalf("GitHubTokenSecret = %q, want %q", got, slackSavedGitHubSecretName)
	}
	if got := run.Spec.Secrets.SlackTokensSecret; got != "me-slack" {
		t.Fatalf("SlackTokensSecret = %q, want me-slack", got)
	}
}

func TestSlackCreateRunRequiresSavedGitHubToken(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
	}

	agent := &triggersv1alpha1.SlackAgent{}
	agent.Name = "me"
	agent.Namespace = "ns"
	agent.Spec.Defaults = triggersv1alpha1.AgentRunDefaults{
		Model:    "claude-opus-4.8",
		Provider: triggersv1alpha1.ProviderCopilot,
		AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
		Secrets: triggersv1alpha1.AgentRunSecrets{
			OpenAIOAuthSecret: "usercred-copilot",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build()
	o := &slackOrchestrator{crdClient: c, namespace: "ns", agentName: "me"}

	err := o.createRun(context.Background(), "slack-test-run", internalslack.Decision{
		ChannelID: "D123",
		ThreadTS:  "1782815689.940749",
		Text:      "hello",
	}, "hello")
	if err == nil {
		t.Fatal("createRun() error = nil, want missing GitHub token error")
	}
	if got := err.Error(); got != "no saved GitHub token; add it in Settings" {
		t.Fatalf("createRun() error = %q, want Settings guidance", got)
	}
}

func TestSlackCreateRunPrefersAgentGitHubTokenSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
	}

	agent := &triggersv1alpha1.SlackAgent{}
	agent.Name = "me"
	agent.Namespace = "ns"
	agent.Spec.Defaults = triggersv1alpha1.AgentRunDefaults{
		Model:    "claude-opus-4.8",
		Provider: triggersv1alpha1.ProviderCopilot,
		AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
		Secrets: triggersv1alpha1.AgentRunSecrets{
			OpenAIOAuthSecret: "usercred-copilot",
			GithubToken:       "me-slack-github",
		},
	}
	agentSecret := &corev1.Secret{}
	agentSecret.Name = "me-slack-github"
	agentSecret.Namespace = "ns"
	agentSecret.Data = map[string][]byte{slackSavedGitHubTokenKey: []byte("agent-gh-tok")}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent, agentSecret).Build()
	o := &slackOrchestrator{crdClient: c, namespace: "ns", agentName: "me", tokensSecret: "me-slack"}

	if err := o.createRun(context.Background(), "slack-test-run", internalslack.Decision{
		ChannelID: "D123",
		ThreadTS:  "1782815689.940749",
		Text:      "hello",
	}, "hello"); err != nil {
		t.Fatalf("createRun() error = %v", err)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "slack-test-run"}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	if run.Spec.Secrets == nil {
		t.Fatal("AgentRun secrets nil")
	}
	if got := run.Spec.Secrets.GitHubTokenSecret; got != "me-slack-github" {
		t.Fatalf("GitHubTokenSecret = %q, want me-slack-github", got)
	}
}

func TestSlackCreateRunFailsWhenAgentGitHubSecretMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
	}

	agent := &triggersv1alpha1.SlackAgent{}
	agent.Name = "me"
	agent.Namespace = "ns"
	agent.Spec.Defaults = triggersv1alpha1.AgentRunDefaults{
		Model:    "claude-opus-4.8",
		Provider: triggersv1alpha1.ProviderCopilot,
		AuthMode: platformv1alpha1.AgentRunAuthModeOAuth,
		Secrets: triggersv1alpha1.AgentRunSecrets{
			OpenAIOAuthSecret: "usercred-copilot",
			GithubToken:       "me-slack-github",
		},
	}
	// The saved token exists, but the agent explicitly references its own
	// secret — a dangling reference must fail loudly, not silently fall back.
	savedSecret := &corev1.Secret{}
	savedSecret.Name = slackSavedGitHubSecretName
	savedSecret.Namespace = "ns"
	savedSecret.Data = map[string][]byte{slackSavedGitHubTokenKey: []byte("saved-gh-tok")}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent, savedSecret).Build()
	o := &slackOrchestrator{crdClient: c, namespace: "ns", agentName: "me"}

	err := o.createRun(context.Background(), "slack-test-run", internalslack.Decision{
		ChannelID: "D123",
		ThreadTS:  "1782815689.940749",
		Text:      "hello",
	}, "hello")
	if err == nil {
		t.Fatal("createRun() succeeded with a dangling agent GitHub secret reference")
	}
	if !strings.Contains(err.Error(), "me-slack-github") {
		t.Fatalf("error = %v, want mention of the missing agent secret", err)
	}
	run := &platformv1alpha1.AgentRun{}
	if getErr := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "slack-test-run"}, run); getErr == nil {
		t.Fatal("AgentRun was created despite the missing GitHub token secret")
	}
}

func TestIsPublicSurface(t *testing.T) {
	// Only a 1:1 DM with the bot is private; everything else (channels,
	// private channels/groups, group DMs, unknown) is visible to others.
	private := []string{"im"}
	public := []string{"channel", "group", "mpim", ""}
	for _, ct := range private {
		if isPublicSurface(ct) {
			t.Errorf("isPublicSurface(%q) = true, want false", ct)
		}
	}
	for _, ct := range public {
		if !isPublicSurface(ct) {
			t.Errorf("isPublicSurface(%q) = false, want true", ct)
		}
	}
}

func TestReplyNeedsApproval(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
	}

	newOrch := func(mode triggersv1alpha1.SlackChannelReplyMode, withAgent bool) *slackOrchestrator {
		builder := fake.NewClientBuilder().WithScheme(scheme)
		if withAgent {
			agent := &triggersv1alpha1.SlackAgent{}
			agent.Name = "me"
			agent.Namespace = "ns"
			agent.Spec.ChannelReplyMode = mode
			builder = builder.WithObjects(agent)
		}
		return &slackOrchestrator{crdClient: builder.Build(), namespace: "ns", agentName: "me"}
	}

	cases := []struct {
		name        string
		channelType string
		mode        triggersv1alpha1.SlackChannelReplyMode
		withAgent   bool
		want        bool
	}{
		{"dm is always direct", "im", triggersv1alpha1.SlackChannelReplyRequireApproval, true, false},
		{"channel with default mode gated", "channel", "", true, true},
		{"channel with require-approval gated", "channel", triggersv1alpha1.SlackChannelReplyRequireApproval, true, true},
		{"channel with auto direct", "channel", triggersv1alpha1.SlackChannelReplyAuto, true, false},
		{"group dm with auto direct", "mpim", triggersv1alpha1.SlackChannelReplyAuto, true, false},
		{"group with require-approval gated", "group", triggersv1alpha1.SlackChannelReplyRequireApproval, true, true},
		{"read failure fails safe to gated", "channel", "", false, true},
		{"read failure never gates a dm", "im", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newOrch(tc.mode, tc.withAgent)
			got := o.replyNeedsApproval(context.Background(), internalslack.Decision{ChannelType: tc.channelType})
			if got != tc.want {
				t.Fatalf("replyNeedsApproval(%q, mode=%q) = %v, want %v", tc.channelType, tc.mode, got, tc.want)
			}
		})
	}
}

func TestChannelReplyReviseInstruction(t *testing.T) {
	got := channelReplyReviseInstruction("Deploys are frozen until Monday.", "mention the incident number")
	for _, want := range []string{
		"mention the incident number",
		"Deploys are frozen until Monday.",
		"ONLY the revised message",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("instruction missing %q:\n%s", want, got)
		}
	}
}
