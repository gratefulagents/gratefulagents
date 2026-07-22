//nolint:goconst // Repeated identifiers are intentional test fixtures.
package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type commitThenErrorProjectPatchClient struct{ client.Client }

func (c commitThenErrorProjectPatchClient) Patch(ctx context.Context, object client.Object, patch client.Patch, options ...client.PatchOption) error {
	if err := c.Client.Patch(ctx, object, patch, options...); err != nil {
		return err
	}
	if _, ok := object.(*triggersv1alpha1.Project); ok {
		return errors.New("simulated response loss after Project patch commit")
	}
	return nil
}

func TestGitHubProjectTriggerMaintainerProtoRoundTrip(t *testing.T) {
	enabled := true
	pb := &platform.ProjectTrigger{
		Name: "issues",
		Type: "github",
		Github: &platform.GitHubProjectTrigger{
			ConnectionRef: "github", Owner: "acme", Repo: "widgets", Issues: true,
			MaintainerEnabled: &enabled, MaintainerMaxConcurrentDispatches: 3, MaintainerMaxDispatchesPerDay: 12,
			MaintainerStandupInterval: "6h", MaintainerModeRef: "repository-maintainer", MaintainerModel: "gpt-5",
			MaintainerAllowPrMerge: true,
		},
	}

	trigger, err := projectTriggerFromProto(pb)
	if err != nil {
		t.Fatalf("projectTriggerFromProto: %v", err)
	}
	maintainer := trigger.GitHub.Maintainer
	if maintainer == nil || maintainer.ModeRef == nil || maintainer.ModeRef.Name != "repository-maintainer" || maintainer.Model != "gpt-5" ||
		maintainer.MaxConcurrentDispatches != 3 || maintainer.MaxDispatchesPerDay != 12 || maintainer.StandupInterval == nil ||
		maintainer.StandupInterval.Duration != 6*time.Hour || !maintainer.AllowPullRequestMerge {
		t.Fatalf("parsed maintainer = %#v", maintainer)
	}

	roundTrip := projectTriggerToProto("project", trigger, triggersv1alpha1.ProjectTriggerStatus{}).GetGithub()
	if roundTrip.MaintainerEnabled == nil || !roundTrip.GetMaintainerEnabled() || roundTrip.GetMaintainerMaxConcurrentDispatches() != 3 ||
		roundTrip.GetMaintainerMaxDispatchesPerDay() != 12 || roundTrip.GetMaintainerStandupInterval() != "6h0m0s" ||
		roundTrip.GetMaintainerModeRef() != "repository-maintainer" || roundTrip.GetMaintainerModel() != "gpt-5" || !roundTrip.GetMaintainerAllowPrMerge() {
		t.Fatalf("round trip maintainer = %#v", roundTrip)
	}
}

func TestGitHubProjectTriggerMaintainerRequiresExplicitOptIn(t *testing.T) {
	trigger, err := projectTriggerFromProto(&platform.ProjectTrigger{
		Name: "issues", Type: "github",
		Github: &platform.GitHubProjectTrigger{
			ConnectionRef: "github", Owner: "acme", Repo: "widgets",
			MaintainerModeRef: "repository-maintainer", MaintainerMaxConcurrentDispatches: 3,
		},
	})
	if err != nil {
		t.Fatalf("projectTriggerFromProto: %v", err)
	}
	if trigger.GitHub.Maintainer != nil {
		t.Fatalf("maintainer = %#v, want nil without explicit opt-in", trigger.GitHub.Maintainer)
	}
}

func TestGitHubProjectTriggerDisabledMaintainerPreservesConfiguration(t *testing.T) {
	disabled := false
	trigger, err := projectTriggerFromProto(&platform.ProjectTrigger{
		Name: "issues", Type: "github",
		Github: &platform.GitHubProjectTrigger{
			ConnectionRef: "github", Owner: "acme", Repo: "widgets", MaintainerEnabled: &disabled,
			MaintainerModeRef: "repository-maintainer", MaintainerMaxConcurrentDispatches: 3,
		},
	})
	if err != nil {
		t.Fatalf("projectTriggerFromProto: %v", err)
	}
	if trigger.GitHub.Maintainer == nil || !trigger.GitHub.Maintainer.Disabled || trigger.GitHub.Maintainer.ModeRef == nil ||
		trigger.GitHub.Maintainer.ModeRef.Name != "repository-maintainer" || trigger.GitHub.Maintainer.MaxConcurrentDispatches != 3 {
		t.Fatalf("disabled maintainer = %#v", trigger.GitHub.Maintainer)
	}
}

func TestGitHubProjectTriggerMaintainerValidation(t *testing.T) {
	enabled := true
	for _, tc := range []struct {
		name   string
		mutate func(*platform.GitHubProjectTrigger)
		want   string
	}{
		{name: "negative concurrent", mutate: func(config *platform.GitHubProjectTrigger) { config.MaintainerMaxConcurrentDispatches = -1 }, want: "max_concurrent"},
		{name: "negative daily", mutate: func(config *platform.GitHubProjectTrigger) { config.MaintainerMaxDispatchesPerDay = -1 }, want: "per_day"},
		{name: "invalid interval", mutate: func(config *platform.GitHubProjectTrigger) { config.MaintainerStandupInterval = "tomorrow" }, want: "standup_interval"},
		{name: "non-positive interval", mutate: func(config *platform.GitHubProjectTrigger) { config.MaintainerStandupInterval = "0s" }, want: "greater than zero"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := &platform.GitHubProjectTrigger{ConnectionRef: "github", Owner: "acme", Repo: "widgets", MaintainerEnabled: &enabled}
			tc.mutate(config)
			_, err := projectTriggerFromProto(&platform.ProjectTrigger{Name: "issues", Type: "github", Github: config})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestSlackProjectTriggerProtoRoundTrip(t *testing.T) {
	idle := int32(90)
	pb := &platform.ProjectTrigger{
		Name: "team-chat",
		Type: "slack",
		Slack: &platform.SlackProjectTrigger{
			ConnectionRef:      "workspace-app",
			Channel:            "C0123ABC",
			ChannelReplyMode:   "auto",
			Commanders:         []string{" U01OWNER ", "U02HELPER", "U02HELPER"},
			SessionIdleMinutes: &idle,
		},
	}

	trigger, err := projectTriggerFromProto(pb)
	if err != nil {
		t.Fatalf("projectTriggerFromProto: %v", err)
	}
	if trigger.Slack == nil {
		t.Fatal("slack config is nil")
	}
	if got, want := trigger.Slack.Commanders, []string{"U01OWNER", "U02HELPER"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("commanders = %#v, want %#v", got, want)
	}
	if trigger.Slack.Channel != "C0123ABC" || trigger.Slack.ChannelReplyMode != triggersv1alpha1.SlackChannelReplyAuto || trigger.Slack.SessionIdleMinutes == nil || *trigger.Slack.SessionIdleMinutes != 90 {
		t.Fatalf("parsed Slack config = %#v", trigger.Slack)
	}

	roundTrip := projectTriggerToProto("project", trigger, triggersv1alpha1.ProjectTriggerStatus{})
	if !reflect.DeepEqual(roundTrip.GetSlack(), &platform.SlackProjectTrigger{
		ConnectionRef:      "workspace-app",
		Channel:            "C0123ABC",
		ChannelReplyMode:   "auto",
		Commanders:         []string{"U01OWNER", "U02HELPER"},
		SessionIdleMinutes: &idle,
	}) {
		t.Fatalf("round trip Slack config = %#v", roundTrip.GetSlack())
	}
}

func TestSlackProjectTriggerSerializesEmptyChannel(t *testing.T) {
	trigger := triggersv1alpha1.ProjectTrigger{
		Name: "team-chat", Type: triggersv1alpha1.ProjectTriggerTypeSlack,
		Slack: &triggersv1alpha1.SlackProjectTriggerConfig{ConnectionRef: triggersv1alpha1.ConnectionRef{Name: "slack"}},
	}
	encoded, err := json.Marshal(trigger)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"channel":""`) {
		t.Fatalf("empty channel was omitted from JSON: %s", encoded)
	}
}

func TestSlackProjectTriggerValidation(t *testing.T) {
	idle := int32(90)
	base := func() *platform.ProjectTrigger {
		return &platform.ProjectTrigger{
			Name: "team-chat",
			Type: "slack",
			Slack: &platform.SlackProjectTrigger{
				ConnectionRef:      "workspace-app",
				Channel:            "C0123ABC",
				ChannelReplyMode:   "require-approval",
				Commanders:         []string{"U01OWNER"},
				SessionIdleMinutes: &idle,
			},
		}
	}

	tests := []struct {
		name   string
		mutate func(*platform.SlackProjectTrigger)
	}{
		{name: "channel name", mutate: func(slack *platform.SlackProjectTrigger) { slack.Channel = "#engineering" }},
		{name: "invalid commander", mutate: func(slack *platform.SlackProjectTrigger) { slack.Commanders = []string{"alice"} }},
		{name: "invalid reply mode", mutate: func(slack *platform.SlackProjectTrigger) { slack.ChannelReplyMode = "always" }},
		{name: "zero idle time", mutate: func(slack *platform.SlackProjectTrigger) { zero := int32(0); slack.SessionIdleMinutes = &zero }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pb := base()
			tt.mutate(pb.Slack)
			if _, err := projectTriggerFromProto(pb); err == nil {
				t.Fatal("projectTriggerFromProto succeeded")
			}
		})
	}
}

func TestResolveSlackTriggerChannelName(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	connection := &triggersv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "slack", Namespace: "team"},
		Spec:       triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeSlack, Slack: &triggersv1alpha1.SlackConnectionConfig{TokensSecret: "slack-tokens"}},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "slack-tokens", Namespace: "team"},
		Data:       map[string][]byte{triggersv1alpha1.SlackBotTokenKey: []byte("xoxb-fixture")},
	}
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(connection, secret).Build()
	server := NewServer(k8s, scheme, nil, nil, false)
	server.slackConversationLookup = func(_ context.Context, token, name string) (string, error) {
		if token != "xoxb-fixture" || name != "engineering" {
			t.Fatalf("lookup token/name = %q, %q", token, name)
		}
		return "C0123ABC", nil
	}
	pb := &platform.ProjectTrigger{
		Name: "team-chat", Type: "slack",
		Slack: &platform.SlackProjectTrigger{ConnectionRef: "slack", Channel: "#engineering"},
	}
	if err := server.resolveSlackTriggerChannel(context.Background(), "team", pb); err != nil {
		t.Fatalf("resolveSlackTriggerChannel: %v", err)
	}
	if pb.GetSlack().GetChannel() != "C0123ABC" {
		t.Fatalf("resolved channel = %q", pb.GetSlack().GetChannel())
	}
	trigger, err := projectTriggerFromProto(pb)
	if err != nil {
		t.Fatalf("projectTriggerFromProto after resolution: %v", err)
	}
	if trigger.Slack.Channel != "C0123ABC" {
		t.Fatalf("persisted channel = %q", trigger.Slack.Channel)
	}
}

func TestUnknownProjectPatchOutcomeRetainsSlackClaim(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	project := &triggersv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: "team"}}
	connection := &triggersv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "slack", Namespace: "team"},
		Spec:       triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeSlack, Slack: &triggersv1alpha1.SlackConnectionConfig{TokensSecret: "tokens"}},
	}
	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(project, connection).Build()
	server := NewServer(commitThenErrorProjectPatchClient{Client: baseClient}, scheme, nil, nil, false, WithAPIReader(baseClient))
	_, err := server.CreateProjectTrigger(context.Background(), &platform.CreateProjectTriggerRequest{
		Namespace: "team", Project: "project", Name: "first",
		Trigger: &platform.ProjectTrigger{Name: "first", Type: "slack", Slack: &platform.SlackProjectTrigger{ConnectionRef: "slack", Channel: "C111"}},
	})
	if err == nil {
		t.Fatal("CreateProjectTrigger succeeded despite simulated response loss")
	}
	storedProject := &triggersv1alpha1.Project{}
	if err := baseClient.Get(context.Background(), client.ObjectKeyFromObject(project), storedProject); err != nil {
		t.Fatal(err)
	}
	if len(storedProject.Spec.Triggers) != 1 {
		t.Fatalf("committed triggers = %d, want 1", len(storedProject.Spec.Triggers))
	}
	storedConnection := &triggersv1alpha1.Connection{}
	if err := baseClient.Get(context.Background(), client.ObjectKeyFromObject(connection), storedConnection); err != nil {
		t.Fatal(err)
	}
	claimValue := storedConnection.Annotations[slackTriggerClaimAnnotation]
	claim, pendingSince := parseSlackTriggerClaim(claimValue)
	if claim != slackTriggerClaim("project", "first") || pendingSince.IsZero() {
		t.Fatalf("retained claim = %q", claimValue)
	}
}

func TestSlackPendingClaimTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 123)
	claim := slackTriggerClaim("project", "trigger")
	value := fmt.Sprintf("%s%d|request|%s", slackPendingClaimPrefix, now.UnixNano(), claim)
	parsedClaim, pendingSince := parseSlackTriggerClaim(value)
	if parsedClaim != claim || !pendingSince.Equal(now) {
		t.Fatalf("parseSlackTriggerClaim(%q) = %q, %v", value, parsedClaim, pendingSince)
	}
	if !slackPendingClaimActive(pendingSince, now.Add(slackPendingClaimTTL)) {
		t.Fatal("claim should remain active through its TTL boundary")
	}
	if slackPendingClaimActive(pendingSince, now.Add(slackPendingClaimTTL+time.Nanosecond)) {
		t.Fatal("claim remained active after its TTL")
	}
	if slackPendingClaimActive(now.Add(time.Minute), now) {
		t.Fatal("future-dated claim should not be active")
	}
	if parsed, at := parseSlackTriggerClaim("pending|not-a-timestamp|request|project/trigger"); parsed != "" || !at.IsZero() {
		t.Fatalf("malformed claim parsed as %q, %v", parsed, at)
	}
}

func TestExpiredPendingSlackClaimCanBeReclaimed(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	enabled := true
	claim := slackTriggerClaim("project", "trigger")
	pending := fmt.Sprintf("%s%d|old-request|%s", slackPendingClaimPrefix, time.Now().Add(-slackPendingClaimTTL-time.Minute).UnixNano(), claim)
	trigger := triggersv1alpha1.ProjectTrigger{
		Name: "trigger", Type: triggersv1alpha1.ProjectTriggerTypeSlack, Enabled: &enabled,
		Slack: &triggersv1alpha1.SlackProjectTriggerConfig{ConnectionRef: triggersv1alpha1.ConnectionRef{Name: "slack"}, Channel: "C111"},
	}
	connection := &triggersv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "slack", Namespace: "team", Annotations: map[string]string{slackTriggerClaimAnnotation: pending}},
		Spec:       triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeSlack, Slack: &triggersv1alpha1.SlackConnectionConfig{TokensSecret: "tokens"}},
	}
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(connection).Build()
	server := NewServer(k8s, scheme, nil, nil, false)
	handle, err := server.claimSlackTriggerConnection(context.Background(), "team", "project", trigger)
	if err != nil {
		t.Fatalf("claimSlackTriggerConnection: %v", err)
	}
	if handle.pendingValue == "" || handle.pendingValue == pending {
		t.Fatalf("replacement pending claim = %q", handle.pendingValue)
	}
}

func TestPendingSlackClaimBlocksSameIdentityRetry(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	enabled := true
	claim := slackTriggerClaim("project", "trigger")
	pending := fmt.Sprintf("%s%d|request|%s", slackPendingClaimPrefix, time.Now().UnixNano(), claim)
	project := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: "team"},
		Spec: triggersv1alpha1.ProjectSpec{Triggers: []triggersv1alpha1.ProjectTrigger{{
			Name: "trigger", Type: triggersv1alpha1.ProjectTriggerTypeSlack, Enabled: &enabled,
			Slack: &triggersv1alpha1.SlackProjectTriggerConfig{ConnectionRef: triggersv1alpha1.ConnectionRef{Name: "slack"}, Channel: "C111"},
		}}},
	}
	connection := &triggersv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "slack", Namespace: "team", Annotations: map[string]string{slackTriggerClaimAnnotation: pending}},
		Spec:       triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeSlack, Slack: &triggersv1alpha1.SlackConnectionConfig{TokensSecret: "tokens"}},
	}
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(project, connection).Build()
	server := NewServer(k8s, scheme, nil, nil, false)
	_, err := server.claimSlackTriggerConnection(context.Background(), "team", "project", project.Spec.Triggers[0])
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("claimSlackTriggerConnection error = %v, want aborted", err)
	}
	stored := &triggersv1alpha1.Connection{}
	if err := k8s.Get(context.Background(), client.ObjectKeyFromObject(connection), stored); err != nil {
		t.Fatal(err)
	}
	if got := stored.Annotations[slackTriggerClaimAnnotation]; got != pending {
		t.Fatalf("pending claim = %q, want %q", got, pending)
	}
}

func TestConcurrentProjectTriggerCreatesPreserveBothChanges(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	project := &triggersv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: "team"}}
	firstConnection := &triggersv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "slack-first", Namespace: "team"},
		Spec:       triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeSlack, Slack: &triggersv1alpha1.SlackConnectionConfig{TokensSecret: "first-tokens"}},
	}
	secondConnection := &triggersv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "slack-second", Namespace: "team"},
		Spec:       triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeSlack, Slack: &triggersv1alpha1.SlackConnectionConfig{TokensSecret: "second-tokens"}},
	}
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(project, firstConnection, secondConnection).Build()
	server := NewServer(k8s, scheme, nil, nil, false)
	ctx := context.Background()

	start := make(chan struct{})
	results := make(chan error, 2)
	for i, connectionName := range []string{"slack-first", "slack-second"} {
		go func(index int, connection string) {
			<-start
			name := []string{"first", "second"}[index]
			_, err := server.CreateProjectTrigger(ctx, &platform.CreateProjectTriggerRequest{
				Namespace: "team", Project: "project", Name: name,
				Trigger: &platform.ProjectTrigger{Name: name, Type: "slack", Slack: &platform.SlackProjectTrigger{ConnectionRef: connection, Channel: []string{"C111", "C222"}[index]}},
			})
			results <- err
		}(i, connectionName)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent create failed: %v", err)
		}
	}
	stored := &triggersv1alpha1.Project{}
	if err := k8s.Get(ctx, client.ObjectKeyFromObject(project), stored); err != nil {
		t.Fatal(err)
	}
	if got := len(stored.Spec.Triggers); got != 2 {
		t.Fatalf("stored triggers = %d, want 2", got)
	}
}

func TestConcurrentSlackTriggersCannotShareConnection(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	project := &triggersv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: "team"}}
	connection := &triggersv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "slack", Namespace: "team"},
		Spec:       triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeSlack, Slack: &triggersv1alpha1.SlackConnectionConfig{TokensSecret: "slack-tokens"}},
	}
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(project, connection).Build()
	server := NewServer(k8s, scheme, nil, nil, false)
	ctx := context.Background()

	type result struct {
		name string
		err  error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for i, name := range []string{"first", "second"} {
		go func(index int, triggerName string) {
			<-start
			_, err := server.CreateProjectTrigger(ctx, &platform.CreateProjectTriggerRequest{
				Namespace: "team", Project: "project", Name: triggerName,
				Trigger: &platform.ProjectTrigger{Name: triggerName, Type: "slack", Slack: &platform.SlackProjectTrigger{ConnectionRef: "slack", Channel: []string{"C111", "C222"}[index]}},
			})
			results <- result{name: triggerName, err: err}
		}(i, name)
	}
	close(start)

	succeeded := 0
	winningName := ""
	for range 2 {
		result := <-results
		if result.err == nil {
			succeeded++
			winningName = result.name
			continue
		}
		if code := connect.CodeOf(result.err); code != connect.CodeAborted && code != connect.CodeFailedPrecondition {
			t.Fatalf("losing trigger returned %v", result.err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful creates = %d, want 1", succeeded)
	}
	stored := &triggersv1alpha1.Project{}
	if err := k8s.Get(ctx, client.ObjectKeyFromObject(project), stored); err != nil {
		t.Fatal(err)
	}
	if got := len(stored.Spec.Triggers); got != 1 {
		t.Fatalf("stored triggers = %d, want 1", got)
	}
	storedConnection := &triggersv1alpha1.Connection{}
	if err := k8s.Get(ctx, client.ObjectKeyFromObject(connection), storedConnection); err != nil {
		t.Fatal(err)
	}
	if got, want := storedConnection.Annotations[slackTriggerClaimAnnotation], slackTriggerClaim("project", winningName); got != want {
		t.Fatalf("Slack trigger claim = %q, want %q", got, want)
	}
}

func TestSlackProjectTriggerAllowsEmptyChannel(t *testing.T) {
	trigger, err := projectTriggerFromProto(&platform.ProjectTrigger{
		Name: "team-chat",
		Type: "slack",
		Slack: &platform.SlackProjectTrigger{
			ConnectionRef: "workspace-app",
			Channel:       "  ",
		},
	})
	if err != nil {
		t.Fatalf("projectTriggerFromProto: %v", err)
	}
	if trigger.Slack == nil || trigger.Slack.Channel != "" {
		t.Fatalf("trigger.Slack = %#v, want empty channel (unscoped)", trigger.Slack)
	}
}
