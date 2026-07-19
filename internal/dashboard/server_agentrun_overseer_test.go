package dashboard

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func TestAgentRunOverseerSpecFromProtoDefaultsAndExplicitZero(t *testing.T) {
	zero, one := int32(0), int32(1)
	spec, err := agentRunOverseerSpecFromProto(&platform.AgentRunOverseerConfig{ModeRefName: " overseer ", ModeRefVersion: " v2 ", Model: " model ", Authority: " enforce ", IntervalMinutes: &one, MaxInterventions: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if spec.ModeRef == nil || spec.ModeRef.Name != "overseer" || spec.ModeRef.Version != "v2" || spec.Model != "model" || spec.Authority != platformv1alpha1.AgentRunOverseerAuthorityEnforce || spec.IntervalMinutes != 1 || spec.MaxInterventions != 0 {
		t.Fatalf("spec = %#v", spec)
	}
	defaults, err := agentRunOverseerSpecFromProto(&platform.AgentRunOverseerConfig{})
	if err != nil || defaults.Authority != platformv1alpha1.AgentRunOverseerAuthorityAdvise || defaults.IntervalMinutes != 10 || defaults.MaxInterventions != 5 {
		t.Fatalf("defaults = %#v, err=%v", defaults, err)
	}
}

func TestAgentRunOverseerSpecFromProtoRejectsInvalidConfig(t *testing.T) {
	zero, negative := int32(0), int32(-1)
	tooLong := platformv1alpha1.AgentRunOverseerMaxIntervalMinutes + 1
	tooMany := platformv1alpha1.AgentRunOverseerMaxInterventions + 1
	for name, config := range map[string]*platform.AgentRunOverseerConfig{
		"partial mode": {ModeRefVersion: "v1"}, "authority": {Authority: "admin"},
		"interval minimum": {IntervalMinutes: &zero}, "interval maximum": {IntervalMinutes: &tooLong},
		"cap minimum": {MaxInterventions: &negative}, "cap maximum": {MaxInterventions: &tooMany},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := agentRunOverseerSpecFromProto(config); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestAgentRunOverseerLifecyclePreservesImmutableFieldsAndMarker(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "default"}, Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	interval, cap := int32(4), int32(7)
	_, err := srv.AttachAgentRunOverseer(context.Background(), &platform.AttachAgentRunOverseerRequest{Namespace: "default", Name: "run", Overseer: &platform.AgentRunOverseerConfig{ModeRefName: "custom", Model: "openai/gpt", IntervalMinutes: &interval, MaxInterventions: &cap}})
	if err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	_, err = srv.UpdateAgentRunOverseer(context.Background(), &platform.UpdateAgentRunOverseerRequest{Namespace: "default", Name: "run", MaxInterventions: &zero})
	if err != nil {
		t.Fatal(err)
	}
	var got platformv1alpha1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(run), &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Overseer == nil || got.Spec.Overseer.ModeRef.Name != "custom" || got.Spec.Overseer.Model != "openai/gpt" || got.Spec.Overseer.MaxInterventions != 0 {
		t.Fatalf("overseer = %#v", got.Spec.Overseer)
	}
	if _, err := srv.DetachAgentRunOverseer(context.Background(), &platform.DetachAgentRunOverseerRequest{Namespace: "default", Name: "run"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(run), &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Overseer != nil || got.Annotations[platformv1alpha1.OverseerDetachingAnnotation] == "" {
		t.Fatalf("detached run = %#v", got)
	}
	_, err = srv.AttachAgentRunOverseer(context.Background(), &platform.AttachAgentRunOverseerRequest{Namespace: "default", Name: "run", Overseer: &platform.AgentRunOverseerConfig{}})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("reattach code=%v err=%v", connect.CodeOf(err), err)
	}
}
