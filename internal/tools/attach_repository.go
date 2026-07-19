package tools

import sdkgit "github.com/gratefulagents/sdk/pkg/agentsdk/tools/git"

// RegisterAttachRepositoryTool registers the SDK attach_repository tool with
// operator defaults for branch creation.
func RegisterAttachRepositoryTool(registry *Registry, baseBranch, branchName string) {
	if registry == nil {
		return
	}
	registry.Register(sdkgit.NewAttachRepositoryTool(nil,
		sdkgit.WithAttachRepositoryDefaultBaseBranch(baseBranch),
		sdkgit.WithAttachRepositoryDefaultBranchName(branchName),
	))
}
