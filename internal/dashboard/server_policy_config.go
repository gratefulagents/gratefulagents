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
			Spec: platformv1alpha1.RuntimeProfileSpec{
				Sandbox: &platformv1alpha1.RuntimeProfileSandbox{
					// The default worker image enables Browser tools. Chromium needs a
					// real /proc/cpuinfo inside the command sandbox; without a private
					// procfs it incorrectly reports that the CPU lacks SSE3.
					EnablePrivateProcfs: true,
				},
				Security: security,
			},
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
	// Migrate profiles created before managed profiles enabled private procfs.
	// A non-nil Sandbox is left untouched so an explicitly configured opt-out
	// remains effective on clusters that do not support pod user namespaces.
	if profile.Spec.Sandbox == nil {
		profile.Spec.Sandbox = &platformv1alpha1.RuntimeProfileSandbox{EnablePrivateProcfs: true}
	}
	profile.Spec.Security = security
	if err := s.k8sClient.Update(ctx, profile); err != nil {
		return nil, false, mapK8sError("update RuntimeProfile", err)
	}
	return &platformv1alpha1.NamedRef{Name: name}, false, nil
}
