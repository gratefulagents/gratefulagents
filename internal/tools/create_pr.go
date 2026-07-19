package tools

import (
	sdkgit "github.com/gratefulagents/sdk/pkg/agentsdk/tools/git"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RegisterCreatePRTool registers the SDK create_pull_request tool with an
// operator artifact sink.
func RegisterCreatePRTool(registry *Registry, k8sClient client.Client, taskName, namespace string) {
	if k8sClient == nil {
		return
	}
	registry.Register(sdkgit.NewCreatePullRequestTool(newBranchGuardedGitRunner(newCoAuthoringGitRunner(nil)), agentRunGitArtifactSink{
		k8sClient: k8sClient,
		taskName:  taskName,
		namespace: namespace,
	}))
}
