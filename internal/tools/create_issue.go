package tools

import (
	sdkgit "github.com/gratefulagents/sdk/pkg/agentsdk/tools/git"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RegisterCreateIssueTool registers the SDK create_github_issue tool with an
// operator artifact sink.
func RegisterCreateIssueTool(registry *Registry, k8sClient client.Client, taskName, namespace string) {
	if k8sClient == nil {
		return
	}
	registry.Register(sdkgit.NewCreateIssueTool(nil, agentRunGitArtifactSink{
		k8sClient: k8sClient,
		taskName:  taskName,
		namespace: namespace,
	}))
}
