//nolint:goconst // Repeated identifiers are intentional test fixtures.
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"connectrpc.com/connect"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
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

	roundTrip := projectTriggerToProto(trigger, triggersv1alpha1.ProjectTriggerStatus{})
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
