package platform

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

func testMetaHarnessRun(labels map[string]string) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-1",
			Namespace: "ns-1",
			UID:       types.UID("uid-1"),
			Labels:    labels,
		},
	}
}

func envValue(t *testing.T, run *platformv1alpha1.AgentRun, name string) (string, bool) {
	t.Helper()
	for _, env := range runExecutionEnvVars(run) {
		if env.Name == name {
			return env.Value, true
		}
	}
	return "", false
}

func TestRunExecutionEnvVarsMetaHarnessDisabledByDefault(t *testing.T) {
	t.Setenv("ENABLE_METAHARNESS", "")
	run := testMetaHarnessRun(nil)
	if _, ok := envValue(t, run, "ENABLE_METAHARNESS"); ok {
		t.Fatal("ENABLE_METAHARNESS must not be set for unlabeled runs without the global flag")
	}
	if _, ok := envValue(t, run, "METAHARNESS_CANDIDATE"); ok {
		t.Fatal("METAHARNESS_CANDIDATE must not be set for unlabeled runs")
	}
	// Base identity env must always be present.
	if v, ok := envValue(t, run, "PLANTASK_NAME"); !ok || v != "run-1" {
		t.Fatalf("PLANTASK_NAME = %q (ok=%v), want run-1", v, ok)
	}
	if v, ok := envValue(t, run, "PLANTASK_UID"); !ok || v != "uid-1" {
		t.Fatalf("PLANTASK_UID = %q (ok=%v), want uid-1", v, ok)
	}
}

func TestRunExecutionEnvVarsMetaHarnessLabelEnablesAndPropagatesCandidate(t *testing.T) {
	t.Setenv("ENABLE_METAHARNESS", "")
	run := testMetaHarnessRun(map[string]string{metaHarnessLabel: "candidate-42"})
	if v, ok := envValue(t, run, "ENABLE_METAHARNESS"); !ok || v != "true" {
		t.Fatalf("ENABLE_METAHARNESS = %q (ok=%v), want true", v, ok)
	}
	if v, ok := envValue(t, run, "METAHARNESS_CANDIDATE"); !ok || v != "candidate-42" {
		t.Fatalf("METAHARNESS_CANDIDATE = %q (ok=%v), want candidate-42", v, ok)
	}
}

func TestRunExecutionEnvVarsMetaHarnessGlobalFlag(t *testing.T) {
	t.Setenv("ENABLE_METAHARNESS", "true")
	run := testMetaHarnessRun(nil)
	if v, ok := envValue(t, run, "ENABLE_METAHARNESS"); !ok || v != "true" {
		t.Fatalf("ENABLE_METAHARNESS = %q (ok=%v), want true", v, ok)
	}
	if _, ok := envValue(t, run, "METAHARNESS_CANDIDATE"); ok {
		t.Fatal("METAHARNESS_CANDIDATE must not be set without a per-run label")
	}
}

func TestRunExecutionEnvVarsDebugFlag(t *testing.T) {
	t.Setenv("ENABLE_METAHARNESS", "")
	run := testMetaHarnessRun(nil)
	run.Spec.Debug = true
	if v, ok := envValue(t, run, "AI_DEBUG"); !ok || v != "1" {
		t.Fatalf("AI_DEBUG = %q (ok=%v), want 1", v, ok)
	}
}
