package dashboard

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func defaultManagedResourceName(base, suffix string) string {
	base = sanitizeDNSLabel(base)
	suffix = sanitizeDNSLabel(suffix)
	if suffix == "" {
		return base
	}
	maxBase := maxDNSLabelLen - 1 - len(suffix)
	if maxBase < 1 {
		return suffix
	}
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
	}
	if base == "" {
		return suffix
	}
	return base + "-" + suffix
}

func projectRuntimeProfileName(projectName string) string {
	return defaultManagedResourceName(projectName, "runtime")
}

func projectMCPPolicyName(projectName string) string {
	return defaultManagedResourceName(projectName, "mcp-policy")
}

func namedRef(name string) *platformv1alpha1.NamedRef {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	return &platformv1alpha1.NamedRef{Name: name}
}

func normalizeConfiguredPermissionMode(mode string) platformv1alpha1.PermissionMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case string(platformv1alpha1.PermissionModeReadOnly):
		return platformv1alpha1.PermissionModeReadOnly
	case string(platformv1alpha1.PermissionModeDangerFullAccess):
		return platformv1alpha1.PermissionModeDangerFullAccess
	default:
		return platformv1alpha1.PermissionModeWorkspaceWrite
	}
}

func normalizeConfiguredEgressMode(mode string) platformv1alpha1.EgressMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case string(platformv1alpha1.EgressMode("restricted")):
		return platformv1alpha1.EgressMode("restricted")
	case string(platformv1alpha1.EgressMode("disabled")):
		return platformv1alpha1.EgressMode("disabled")
	default:
		return platformv1alpha1.EgressMode("unrestricted")
	}
}

func normalizeMCPDefaultAction(action string) platformv1alpha1.MCPDefaultAction {
	if strings.EqualFold(strings.TrimSpace(action), string(platformv1alpha1.MCPDefaultActionAllow)) {
		return platformv1alpha1.MCPDefaultActionAllow
	}
	return platformv1alpha1.MCPDefaultActionDeny
}

func normalizeMCPAllowedServers(servers []string) []platformv1alpha1.MCPAllowedServer {
	out := make([]platformv1alpha1.MCPAllowedServer, 0, len(servers))
	seen := map[string]struct{}{}
	for _, raw := range servers {
		for _, part := range strings.Split(raw, ",") {
			name := strings.TrimSpace(part)
			key := strings.ToLower(name)
			if name == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, platformv1alpha1.MCPAllowedServer{Name: name})
		}
	}
	return out
}

func (s *Server) applyConfiguredRuntimeProfile(
	ctx context.Context,
	namespace string,
	defaultName string,
	configure bool,
	refName string,
	permissionMode string,
	egressMode string,
) (*platformv1alpha1.NamedRef, bool, error) {
	name := strings.TrimSpace(refName)
	if !configure {
		return namedRef(name), false, nil
	}
	if name == "" {
		name = defaultName
	}
	if name == "" {
		return nil, false, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("runtime_profile_ref is required when configure_runtime_profile is true"))
	}

	security := &platformv1alpha1.RuntimeProfileSecurity{
		PermissionMode:  normalizeConfiguredPermissionMode(permissionMode),
		GitRemoteWrites: platformv1alpha1.GitRemoteWritesEnabled,
		EgressMode:      normalizeConfiguredEgressMode(egressMode),
	}

	profile := &platformv1alpha1.RuntimeProfile{}
	err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, profile)
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return nil, false, mapK8sError("read RuntimeProfile", err)
		}
		profile = &platformv1alpha1.RuntimeProfile{
			TypeMeta: metav1.TypeMeta{
				APIVersion: platformv1alpha1.GroupVersion.String(),
				Kind:       "RuntimeProfile",
			},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       platformv1alpha1.RuntimeProfileSpec{Security: security},
		}
		if err := s.k8sClient.Create(ctx, profile); err != nil {
			return nil, false, mapK8sError("create RuntimeProfile", err)
		}
		return &platformv1alpha1.NamedRef{Name: name}, true, nil
	}

	// This legacy project/trigger editor does not expose every RuntimeProfile
	// security field. Preserve the independently managed remote-write policy.
	if profile.Spec.Security != nil {
		security.GitRemoteWrites = profile.Spec.Security.GitRemoteWrites
	}
	profile.Spec.Security = security
	if err := s.k8sClient.Update(ctx, profile); err != nil {
		return nil, false, mapK8sError("update RuntimeProfile", err)
	}
	return &platformv1alpha1.NamedRef{Name: name}, false, nil
}

func (s *Server) applyConfiguredMCPPolicy(
	ctx context.Context,
	namespace string,
	defaultName string,
	configure bool,
	refName string,
	defaultAction string,
	allowedServers []string,
) (*platformv1alpha1.NamedRef, bool, error) {
	name := strings.TrimSpace(refName)
	if !configure {
		return namedRef(name), false, nil
	}
	if name == "" {
		name = defaultName
	}
	if name == "" {
		return nil, false, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("mcp_policy_ref is required when configure_mcp_policy is true"))
	}

	spec := platformv1alpha1.MCPPolicySpec{
		DefaultAction:  normalizeMCPDefaultAction(defaultAction),
		AllowedServers: normalizeMCPAllowedServers(allowedServers),
	}

	policy := &platformv1alpha1.MCPPolicy{}
	err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, policy)
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return nil, false, mapK8sError("read MCPPolicy", err)
		}
		policy = &platformv1alpha1.MCPPolicy{
			TypeMeta: metav1.TypeMeta{
				APIVersion: platformv1alpha1.GroupVersion.String(),
				Kind:       "MCPPolicy",
			},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       spec,
		}
		if err := s.k8sClient.Create(ctx, policy); err != nil {
			return nil, false, mapK8sError("create MCPPolicy", err)
		}
		return &platformv1alpha1.NamedRef{Name: name}, true, nil
	}

	policy.Spec.DefaultAction = spec.DefaultAction
	policy.Spec.AllowedServers = spec.AllowedServers
	if err := s.k8sClient.Update(ctx, policy); err != nil {
		return nil, false, mapK8sError("update MCPPolicy", err)
	}
	return &platformv1alpha1.NamedRef{Name: name}, false, nil
}
