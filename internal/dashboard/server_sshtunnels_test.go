/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package dashboard

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// TestListSSHTunnelsScopedToPersonalNamespace pins the read surface for the
// kubectl-authored SSHTunnel resources: the caller sees only tunnels in their
// personal namespace, sorted by name, with status but never secret material.
func TestListSSHTunnelsScopedToPersonalNamespace(t *testing.T) {
	scheme := testProjectScheme(t)
	aliceNS := deriveUserNamespaceName("Alice Smith", "alice-id")
	ready := &platformv1alpha1.SSHTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "vllm", Namespace: aliceNS},
		Spec: platformv1alpha1.SSHTunnelSpec{
			Host:        "gpu.example.com",
			Port:        2222,
			User:        "tunnel",
			RemoteHost:  "127.0.0.1",
			RemotePort:  8000,
			Description: "Self-hosted vLLM",
			SecretRef:   platformv1alpha1.NamedRef{Name: "vllm-ssh"},
		},
		Status: platformv1alpha1.SSHTunnelStatus{
			Phase: "Ready",
			Conditions: []metav1.Condition{{
				Type:    "Ready",
				Status:  metav1.ConditionTrue,
				Reason:  "Reconciled",
				Message: "Validated SSH tunnel to tunnel@gpu.example.com",
			}},
		},
	}
	invalid := &platformv1alpha1.SSHTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "broken", Namespace: aliceNS},
		Spec: platformv1alpha1.SSHTunnelSpec{
			Host:       "other.example.com",
			User:       "tunnel",
			RemotePort: 8000,
			SecretRef:  platformv1alpha1.NamedRef{Name: "missing"},
		},
		Status: platformv1alpha1.SSHTunnelStatus{
			Phase: "Invalid",
			Conditions: []metav1.Condition{{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "Reconciled",
				Message: `secret "missing" not found`,
			}},
		},
	}
	otherNamespace := &platformv1alpha1.SSHTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: "another-tenant"},
		Spec: platformv1alpha1.SSHTunnelSpec{
			Host:       "foreign.example.com",
			User:       "tunnel",
			RemotePort: 8000,
			SecretRef:  platformv1alpha1.NamedRef{Name: "foreign-ssh"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ready, invalid, otherNamespace).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.ListSSHTunnels(resourceActorContext("alice-id", "member", "Alice Smith"), &platform.ListSSHTunnelsRequest{})
	if err != nil {
		t.Fatalf("ListSSHTunnels() error = %v", err)
	}
	if resp.Namespace != aliceNS {
		t.Fatalf("namespace = %q, want %q", resp.Namespace, aliceNS)
	}
	if len(resp.Tunnels) != 2 || resp.Tunnels[0].Name != "broken" || resp.Tunnels[1].Name != "vllm" {
		t.Fatalf("tunnels = %#v, want [broken vllm] from the caller's namespace only", resp.Tunnels)
	}
	got := resp.Tunnels[1]
	if got.Host != "gpu.example.com" || got.Port != 2222 || got.User != "tunnel" ||
		got.RemoteHost != "127.0.0.1" || got.RemotePort != 8000 || got.Description != "Self-hosted vLLM" {
		t.Fatalf("tunnel summary = %#v, want spec fields mapped", got)
	}
	if got.Phase != "Ready" || got.Message != "Validated SSH tunnel to tunnel@gpu.example.com" {
		t.Fatalf("tunnel health = %q/%q, want Ready with condition message", got.Phase, got.Message)
	}
	if resp.Tunnels[0].Phase != "Invalid" || resp.Tunnels[0].Message != `secret "missing" not found` {
		t.Fatalf("invalid tunnel health = %q/%q", resp.Tunnels[0].Phase, resp.Tunnels[0].Message)
	}

	other, err := srv.ListSSHTunnels(resourceActorContext("bob-id", "member", "Bob Jones"), &platform.ListSSHTunnelsRequest{})
	if err != nil {
		t.Fatalf("ListSSHTunnels() other user error = %v", err)
	}
	if len(other.Tunnels) != 0 {
		t.Fatalf("other user saw %d tunnels, want 0", len(other.Tunnels))
	}
}
