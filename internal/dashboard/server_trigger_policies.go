package dashboard

import (
	"context"
	"log"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// applyTriggerPolicies provisions or updates the RuntimeProfile and MCPPolicy
// a trigger's policies request configures, mirroring the Project create/update
// semantics, and rewires defaults' refs to the resulting objects. It returns
// cleanup funcs that undo its writes — deleting objects it created and
// restoring the prior spec of objects it updated in place — so callers can
// roll back when persisting the trigger itself fails. Cleanup funcs run on a
// context detached from the request's cancellation so rollback still succeeds
// when the request is cancelled mid-flight.
func (s *Server) applyTriggerPolicies(
	ctx context.Context,
	namespace, triggerName string,
	policies *platform.TriggerPolicies,
	defaults *triggersv1alpha1.AgentRunDefaults,
) ([]func(), error) {
	if policies == nil {
		return nil, nil
	}
	cleanupCtx := context.WithoutCancel(ctx)
	runCleanup := func(fns []func()) {
		for _, fn := range fns {
			fn()
		}
	}

	runtimeRefName := ""
	if defaults.RuntimeProfileRef != nil {
		runtimeRefName = defaults.RuntimeProfileRef.Name
	}
	var cleanup []func()
	// Snapshot the spec of a pre-existing profile before applyConfigured*
	// updates it in place, so a failed trigger write cannot leave another
	// trigger's policy object silently mutated.
	if policies.GetConfigureRuntimeProfile() {
		name := strings.TrimSpace(runtimeRefName)
		if name == "" {
			name = defaultManagedResourceName(triggerName, "runtime")
		}
		prior := &platformv1alpha1.RuntimeProfile{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, prior); err == nil {
			priorSpec := prior.Spec.DeepCopy()
			profileName := name
			cleanup = append(cleanup, func() {
				s.restoreRuntimeProfileSpec(cleanupCtx, namespace, profileName, priorSpec)
			})
		}
	}
	runtimeProfileRef, runtimeProfileCreated, err := s.applyConfiguredRuntimeProfile(
		ctx,
		namespace,
		defaultManagedResourceName(triggerName, "runtime"),
		policies.GetConfigureRuntimeProfile(),
		runtimeRefName,
		policies.GetPermissionMode(),
		policies.GetEgressMode(),
	)
	if err != nil {
		runCleanup(cleanup)
		return nil, err
	}
	if runtimeProfileCreated {
		cleanup = append(cleanup, func() { s.cleanupRuntimeProfile(cleanupCtx, namespace, runtimeProfileRef.Name) })
	}

	mcpRefName := ""
	if defaults.MCPPolicyRef != nil {
		mcpRefName = defaults.MCPPolicyRef.Name
	}
	if policies.GetConfigureMcpPolicy() {
		name := strings.TrimSpace(mcpRefName)
		if name == "" {
			name = defaultManagedResourceName(triggerName, "mcp-policy")
		}
		prior := &platformv1alpha1.MCPPolicy{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, prior); err == nil {
			priorSpec := prior.Spec.DeepCopy()
			policyName := name
			cleanup = append(cleanup, func() {
				s.restoreMCPPolicySpec(cleanupCtx, namespace, policyName, priorSpec)
			})
		}
	}
	mcpPolicyRef, mcpPolicyCreated, err := s.applyConfiguredMCPPolicy(
		ctx,
		namespace,
		defaultManagedResourceName(triggerName, "mcp-policy"),
		policies.GetConfigureMcpPolicy(),
		mcpRefName,
		policies.GetMcpPolicyDefaultAction(),
		policies.GetMcpPolicyAllowedServers(),
	)
	if err != nil {
		runCleanup(cleanup)
		return nil, err
	}
	if mcpPolicyCreated {
		cleanup = append(cleanup, func() { s.cleanupMCPPolicy(cleanupCtx, namespace, mcpPolicyRef.Name) })
	}

	defaults.RuntimeProfileRef = runtimeProfileRef
	defaults.MCPPolicyRef = mcpPolicyRef
	return cleanup, nil
}

// restoreRuntimeProfileSpec best-effort restores a RuntimeProfile's spec to a
// snapshot taken before a rolled-back trigger write updated it in place.
func (s *Server) restoreRuntimeProfileSpec(ctx context.Context, namespace, name string, spec *platformv1alpha1.RuntimeProfileSpec) {
	profile := &platformv1alpha1.RuntimeProfile{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, profile); err != nil {
		log.Printf("WARN: failed to read RuntimeProfile %s/%s for rollback: %v", namespace, name, err)
		return
	}
	profile.Spec = *spec
	if err := s.k8sClient.Update(ctx, profile); err != nil {
		log.Printf("WARN: failed to restore RuntimeProfile %s/%s after rollback: %v", namespace, name, err)
	}
}

// restoreMCPPolicySpec best-effort restores an MCPPolicy's spec to a snapshot
// taken before a rolled-back trigger write updated it in place.
func (s *Server) restoreMCPPolicySpec(ctx context.Context, namespace, name string, spec *platformv1alpha1.MCPPolicySpec) {
	policy := &platformv1alpha1.MCPPolicy{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, policy); err != nil {
		log.Printf("WARN: failed to read MCPPolicy %s/%s for rollback: %v", namespace, name, err)
		return
	}
	policy.Spec = *spec
	if err := s.k8sClient.Update(ctx, policy); err != nil {
		log.Printf("WARN: failed to restore MCPPolicy %s/%s after rollback: %v", namespace, name, err)
	}
}

// resolveTriggerPolicyModes resolves a trigger's referenced RuntimeProfile and
// MCPPolicy into the flattened read-model fields, the same way Project
// enrichment does.
func (s *Server) resolveTriggerPolicyModes(
	ctx context.Context,
	namespace string,
	d triggersv1alpha1.AgentRunDefaults,
) (permissionMode, egressMode, mcpDefaultAction string, mcpAllowedServers []string) {
	if d.RuntimeProfileRef != nil {
		permissionMode, egressMode = s.runtimeProfileModes(ctx, namespace, d.RuntimeProfileRef.Name)
	}
	if d.MCPPolicyRef != nil {
		mcpDefaultAction, mcpAllowedServers = s.mcpPolicyConfig(ctx, namespace, d.MCPPolicyRef.Name)
	}
	return permissionMode, egressMode, mcpDefaultAction, mcpAllowedServers
}
