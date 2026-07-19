package tools

import (
	"context"
	"log"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type agentRunGitArtifactSink struct {
	k8sClient client.Client
	taskName  string
	namespace string
}

func (s agentRunGitArtifactSink) RecordPullRequestURL(ctx context.Context, url string) error {
	return s.patchArtifacts(ctx, func(artifacts *platformv1alpha1.AgentRunArtifacts) {
		// Keep the legacy single-URL field pointing at the most recent PR and
		// accumulate every distinct PR the run creates (one per repo is common
		// now that create_pull_request accepts repo_path).
		artifacts.PullRequestURL = url
		for _, existing := range artifacts.PullRequestURLs {
			if existing == url {
				return
			}
		}
		artifacts.PullRequestURLs = append(artifacts.PullRequestURLs, url)
	})
}

func (s agentRunGitArtifactSink) RecordIssueURL(ctx context.Context, url string) error {
	return s.patchArtifacts(ctx, func(artifacts *platformv1alpha1.AgentRunArtifacts) {
		artifacts.IssueURL = url
	})
}

func (s agentRunGitArtifactSink) patchArtifacts(ctx context.Context, mutate func(*platformv1alpha1.AgentRunArtifacts)) error {
	if s.k8sClient == nil {
		return nil
	}
	key := types.NamespacedName{Name: s.taskName, Namespace: s.namespace}
	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, key, run); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Printf("WARN: failed to get AgentRun for GitHub artifact patch: %v", err)
			return err
		}
		return nil
	}

	patch := run.DeepCopy()
	if patch.Status.Artifacts == nil {
		patch.Status.Artifacts = &platformv1alpha1.AgentRunArtifacts{}
	}
	mutate(patch.Status.Artifacts)
	if err := s.k8sClient.Status().Patch(ctx, patch, client.MergeFrom(run)); err != nil {
		log.Printf("WARN: failed to patch GitHub artifact on AgentRun: %v", err)
		return err
	}
	return nil
}
