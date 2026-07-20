/*
Copyright 2026.

SPDX-License-Identifier: GPL-3.0-only
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PermissionMode defines the runtime mutability boundary.
// +kubebuilder:validation:Enum=read-only;workspace-write;danger-full-access
type PermissionMode string

const (
	PermissionModeReadOnly         PermissionMode = "read-only"
	PermissionModeWorkspaceWrite   PermissionMode = "workspace-write"
	PermissionModeDangerFullAccess PermissionMode = "danger-full-access"
)

// permissionModeRank orders modes from most (0) to least restrictive.
// Unknown non-empty values rank least restrictive.
func permissionModeRank(m PermissionMode) int {
	switch m {
	case PermissionModeReadOnly:
		return 0
	case PermissionModeWorkspaceWrite:
		return 1
	default:
		return 2
	}
}

// MostRestrictivePermissionMode combines two permission modes, returning the
// more restrictive one. Empty values express no opinion and never tighten or
// loosen the other side.
func MostRestrictivePermissionMode(a, b PermissionMode) PermissionMode {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if permissionModeRank(a) <= permissionModeRank(b) {
		return a
	}
	return b
}

// EgressMode defines outbound network posture.
// +kubebuilder:validation:Enum=unrestricted;restricted;disabled
type EgressMode string

// RuntimeProfileSandbox holds sandbox provisioning defaults.
type RuntimeProfileSandbox struct {
	// +optional
	SandboxTemplateRef *NamedRef `json:"sandboxTemplateRef,omitempty"`
	// +optional
	RuntimeClassName string `json:"runtimeClassName,omitempty"`
	// +optional
	WarmPoolRef *NamedRef `json:"warmPoolRef,omitempty"`
	// PersistWorkspace enables a PersistentVolumeClaim for /workspace so that
	// files survive pod restarts (e.g. pause/resume). Default: false (EmptyDir).
	// +optional
	PersistWorkspace bool `json:"persistWorkspace,omitempty"`
	// WorkspaceSize sets the PVC capacity when PersistWorkspace is true.
	// Default: "10Gi".
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?(Ki|Mi|Gi|Ti|Pi|Ei|m|k|M|G|T|P|E)?$`
	WorkspaceSize string `json:"workspaceSize,omitempty"`
	// EnablePrivateProcfs runs the worker pod in a Kubernetes user namespace and
	// unmasks the worker container's procfs so bubblewrap can mount a fresh,
	// PID-namespaced /proc for model-controlled commands. This is required by
	// tools such as Chromium and Go that depend on /proc/self or /proc/cpuinfo.
	// It requires node, container-runtime, and volume support for Kubernetes pod
	// user namespaces and a Pod Security policy that permits Unmasked procMount.
	// The worker's existing procfs is never exposed inside the command sandbox.
	// +optional
	EnablePrivateProcfs bool `json:"enablePrivateProcfs,omitempty"`
	// CommandSandbox configures the same-pod subprocess sandbox used by model-controlled commands.
	// Toolchains are mounted read-only; the workspace remains the writable project boundary.
	// +optional
	CommandSandbox *RuntimeProfileCommandSandbox `json:"commandSandbox,omitempty"`
}

// RuntimeProfileCommandSandbox configures language/toolchain visibility inside model-controlled subprocesses.
type RuntimeProfileCommandSandbox struct {
	// Path replaces the default subprocess PATH. Use absolute paths only; writable and sensitive paths are ignored.
	// When set, PathPrepend and PathAppend are ignored by the subprocess runtime.
	// +listType=set
	// +optional
	Path []string `json:"path,omitempty"`
	// PathPrepend adds absolute paths before the default subprocess PATH. Writable and sensitive paths are ignored.
	// +listType=set
	// +optional
	PathPrepend []string `json:"pathPrepend,omitempty"`
	// PathAppend adds absolute paths after the default subprocess PATH.
	// Sensitive paths are ignored; project-local node_modules/.bin and the dedicated scratch Go bin path are allowed here so project tools resolve after system tools.
	// +listType=set
	// +optional
	PathAppend []string `json:"pathAppend,omitempty"`
	// ExtraReadOnlyPaths mounts additional host paths read-only inside the subprocess sandbox.
	// Paths inside /workspace, /tmp, /proc, /dev, /run, /var/run, /var/lib, /home, /root, and / are ignored by the subprocess runtime.
	// +listType=set
	// +optional
	ExtraReadOnlyPaths []string `json:"extraReadOnlyPaths,omitempty"`
	// ExtraWritablePaths grants absolute admin-owned scratch or cache paths writable inside the subprocess sandbox.
	// Workspace paths are not allowed, and sensitive roots are ignored by the subprocess runtime.
	// +listType=set
	// +optional
	ExtraWritablePaths []string `json:"extraWritablePaths,omitempty"`
	// Env adds non-secret environment variables to sandboxed subprocesses.
	// Values can reference other safe sandbox variables such as $PATH; secret-like keys are ignored by the controller.
	// +optional
	Env map[string]string `json:"env,omitempty"`
}

// RuntimeProfileSecurity holds executor governance defaults.
type RuntimeProfileSecurity struct {
	// +optional
	PermissionMode PermissionMode `json:"permissionMode,omitempty"`
	// +optional
	EgressMode EgressMode `json:"egressMode,omitempty"`
	// +optional
	DefaultTimeout metav1.Duration `json:"defaultTimeout,omitempty"`
}

// RuntimeProfileAdmission holds lightweight concurrency defaults for v1.
type RuntimeProfileAdmission struct {
	// +optional
	MaxConcurrentRuns int32 `json:"maxConcurrentRuns,omitempty"`
	// +optional
	PerNamespaceMaxConcurrentRuns int32 `json:"perNamespaceMaxConcurrentRuns,omitempty"`
	// +optional
	StaleRunTimeout metav1.Duration `json:"staleRunTimeout,omitempty"`
}

// RuntimeProfileSpec defines the desired state of RuntimeProfile.
type RuntimeProfileSpec struct {
	// +optional
	Sandbox *RuntimeProfileSandbox `json:"sandbox,omitempty"`
	// +optional
	Security *RuntimeProfileSecurity `json:"security,omitempty"`
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
	// +optional
	Admission *RuntimeProfileAdmission `json:"admission,omitempty"`
}

// RuntimeProfileStatus defines the observed state of RuntimeProfile.
type RuntimeProfileStatus struct {
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	ResolvedDefaultsHash string `json:"resolvedDefaultsHash,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Permission",type=string,JSONPath=`.spec.security.permissionMode`
// +kubebuilder:printcolumn:name="Egress",type=string,JSONPath=`.spec.security.egressMode`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// RuntimeProfile is the Schema for the runtimeprofiles API.
type RuntimeProfile struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec RuntimeProfileSpec `json:"spec"`

	// +optional
	Status RuntimeProfileStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// RuntimeProfileList contains a list of RuntimeProfile.
type RuntimeProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []RuntimeProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RuntimeProfile{}, &RuntimeProfileList{})
}
