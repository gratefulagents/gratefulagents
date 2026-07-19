package tools

import (
	"context"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newGitArtifactSink(t *testing.T) (agentRunGitArtifactSink, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "pr-run", Namespace: "default"}}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	return agentRunGitArtifactSink{k8sClient: c, taskName: "pr-run", namespace: "default"}, c
}

func TestRecordPullRequestURLAccumulatesDistinctURLs(t *testing.T) {
	t.Parallel()
	sink, c := newGitArtifactSink(t)
	ctx := context.Background()

	for _, url := range []string{
		"https://github.com/acme/app/pull/1",
		"https://github.com/acme/charts/pull/2",
		"https://github.com/acme/app/pull/1", // duplicate re-record (e.g. "PR already exists")
	} {
		if err := sink.RecordPullRequestURL(ctx, url); err != nil {
			t.Fatalf("RecordPullRequestURL(%q) error = %v", url, err)
		}
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(ctx, client.ObjectKey{Name: "pr-run", Namespace: "default"}, run); err != nil {
		t.Fatalf("Get(run): %v", err)
	}
	artifacts := run.Status.Artifacts
	if artifacts == nil {
		t.Fatal("expected artifacts to be recorded")
	}
	if artifacts.PullRequestURL != "https://github.com/acme/app/pull/1" {
		t.Fatalf("PullRequestURL = %q, want the most recently recorded URL", artifacts.PullRequestURL)
	}
	want := []string{
		"https://github.com/acme/app/pull/1",
		"https://github.com/acme/charts/pull/2",
	}
	if len(artifacts.PullRequestURLs) != len(want) {
		t.Fatalf("PullRequestURLs = %v, want %v", artifacts.PullRequestURLs, want)
	}
	for i := range want {
		if artifacts.PullRequestURLs[i] != want[i] {
			t.Fatalf("PullRequestURLs[%d] = %q, want %q", i, artifacts.PullRequestURLs[i], want[i])
		}
	}
}
