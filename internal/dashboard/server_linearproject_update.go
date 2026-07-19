package dashboard

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// UpdateLinearProject replaces the run defaults (and optional policies) of an
// existing LinearProject trigger. The spec-level Linear wiring (API key
// secret, project/team IDs, poll interval, approved label, autoCreateTasks)
// is never changed by this RPC.
func (s *Server) UpdateLinearProject(ctx context.Context, req *platform.UpdateLinearProjectRequest) (*platform.LinearProject, error) {
	namespace := strings.TrimSpace(req.GetNamespace())
	name := strings.TrimSpace(req.GetName())
	if namespace == "" || name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace and name are required"))
	}
	if err := s.requireResourceAccess(ctx, linearProjectResourceType, name, namespace, AccessCollaborator, "update this project"); err != nil {
		return nil, err
	}

	existing := &triggersv1alpha1.LinearProject{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, existing); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get LinearProject %s/%s", namespace, name), err)
	}

	defaults, provider, authMode, err := protoDefaultsToCRD(req.GetDefaults())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// Trigger-created runs require an explicit model (validateTriggerRunDefaults
	// in the controller); reject early instead of persisting a broken trigger.
	if strings.TrimSpace(defaults.Model) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("defaults.model is required"))
	}
	if strings.TrimSpace(req.GetDefaults().GetGithubTokenSecret()) == "" {
		defaults.Secrets.GithubToken = existing.Spec.Defaults.Secrets.GithubToken
	}

	if req.GetUseSavedCredentials() {
		secrets := triggersv1alpha1.AgentRunSecrets{}
		if err := s.applyProjectSavedCredentials(ctx, namespace, provider, authMode, &secrets); err != nil {
			return nil, err
		}
		// Rewiring provider auth must not silently drop the trigger's GitHub
		// token ref when the caller has no saved GitHub token to replace it.
		if strings.TrimSpace(secrets.GithubToken) == "" {
			secrets.GithubToken = existing.Spec.Defaults.Secrets.GithubToken
		}
		defaults.Secrets = secrets
	} else if err := validateProviderAuthConfiguration(provider, authMode, defaults.Secrets.ClaudeApiKey, defaults.Secrets.OpenAIOAuthSecret, defaults.Secrets.ProviderKeys); err != nil { //nolint:staticcheck // legacy field retained for the explicit anthropic API-key path
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	policyCleanup, err := s.applyTriggerPolicies(ctx, namespace, name, req.GetPolicies(), &defaults)
	if err != nil {
		return nil, err
	}

	preserveAdminOnlyTriggerDefaults(&defaults, existing.Spec.Defaults)
	existing.Spec.Defaults = defaults
	if err := s.k8sClient.Update(ctx, existing); err != nil {
		for _, fn := range policyCleanup {
			fn()
		}
		return nil, mapK8sError("update LinearProject", err)
	}

	pb := s.linearProjectProto(ctx, existing, nil)
	pb.Owner, pb.MyPermission = s.resourceACL(ctx, linearProjectResourceType, existing.Name, existing.Namespace)
	return pb, nil
}
