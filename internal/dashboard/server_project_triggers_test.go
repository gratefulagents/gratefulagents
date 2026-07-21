package dashboard

import (
	"reflect"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

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
