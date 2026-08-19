/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	// SSHTunnelSecretPrivateKeyKey is the required key in the referenced
	// Secret holding the PEM/OpenSSH private key used to authenticate.
	SSHTunnelSecretPrivateKeyKey = "privateKey"
	// SSHTunnelSecretKnownHostsKey is the required key in the referenced
	// Secret holding the pinned known_hosts entry (or entries) for the
	// tunnel host. Host keys are always verified strictly against this
	// file — there is no trust-on-first-use.
	SSHTunnelSecretKnownHostsKey = "known_hosts"
)

// SSHTunnelSpec describes an SSH local port-forward to a self-hosted,
// OpenAI-compatible inference endpoint that is reachable only over SSH.
//
// A run that references the tunnel (spec.sshTunnelRef) gets a hardened SSH
// sidecar that forwards 127.0.0.1:localPort inside the run pod to
// remoteHost:remotePort as seen from the SSH server, and its OPENAI_BASE_URL
// is pointed at http://127.0.0.1:localPort<baseURLPath>. The private key is
// mounted only into the sidecar container, never into the worker container.
type SSHTunnelSpec struct {
	// host is the SSH server address (DNS name or IP) that fronts the
	// inference endpoint.
	// +kubebuilder:validation:MinLength=1
	// +required
	Host string `json:"host"`
	// port is the SSH server port.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=22
	// +optional
	Port int32 `json:"port,omitempty"`
	// user is the SSH login user. Use a dedicated no-shell account whose
	// authorized_keys entry restricts the key to this single forward, e.g.
	// restrict,port-forwarding,permitopen="127.0.0.1:8000".
	// +kubebuilder:validation:MinLength=1
	// +required
	User string `json:"user"`
	// remoteHost is the forward destination as resolved on the SSH server.
	// Keep the inference server bound to loopback so SSH is the only way in.
	// +kubebuilder:default="127.0.0.1"
	// +optional
	RemoteHost string `json:"remoteHost,omitempty"`
	// remotePort is the inference endpoint port on remoteHost.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +required
	RemotePort int32 `json:"remotePort"`
	// localPort is the loopback port the sidecar listens on inside the run
	// pod. The worker reaches the endpoint at http://127.0.0.1:localPort.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=8434
	// +optional
	LocalPort int32 `json:"localPort,omitempty"`
	// baseURLPath is appended to http://127.0.0.1:localPort to form the
	// OPENAI_BASE_URL for runs using this tunnel.
	// +kubebuilder:default="/v1"
	// +optional
	BaseURLPath string `json:"baseURLPath,omitempty"`
	// secretRef names a Secret in the tunnel's namespace that must contain
	// the keys "privateKey" (SSH private key) and "known_hosts" (pinned host
	// key entries; StrictHostKeyChecking is always enforced against them).
	// +required
	SecretRef NamedRef `json:"secretRef"`
	// image overrides the SSH sidecar image for this tunnel. It must provide
	// /bin/sh and an OpenSSH client. Defaults to the operator-configured
	// tunnel image.
	// +optional
	Image string `json:"image,omitempty"`
	// description is a short human-readable summary shown when browsing
	// tunnels.
	// +optional
	Description string `json:"description,omitempty"`
}

// SSHTunnelStatus defines the observed state of SSHTunnel.
type SSHTunnelStatus struct {
	// +optional
	Phase string `json:"phase,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Host",type=string,JSONPath=`.spec.host`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SSHTunnel is the Schema for SSH tunnels to self-hosted inference endpoints.
type SSHTunnel struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec SSHTunnelSpec `json:"spec"`

	// +optional
	Status SSHTunnelStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SSHTunnelList contains a list of SSHTunnel.
type SSHTunnelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SSHTunnel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SSHTunnel{}, &SSHTunnelList{})
}
