/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package platform

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

func newSSHTunnelTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1 AddToScheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&platformv1alpha1.SSHTunnel{}).Build()
}

func testSSHTunnel() *platformv1alpha1.SSHTunnel {
	return &platformv1alpha1.SSHTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "llm", Namespace: "ns"},
		Spec: platformv1alpha1.SSHTunnelSpec{
			Host:       "inference.example.com",
			User:       "tunnel",
			RemotePort: 8000,
			SecretRef:  platformv1alpha1.NamedRef{Name: "llm-tunnel"},
		},
	}
}

func testSSHTunnelSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "llm-tunnel", Namespace: "ns"},
		Data: map[string][]byte{
			platformv1alpha1.SSHTunnelSecretPrivateKeyKey: []byte("key-material"),
			platformv1alpha1.SSHTunnelSecretKnownHostsKey: []byte("inference.example.com ssh-ed25519 AAAA"),
		},
	}
}

func reconcileSSHTunnel(t *testing.T, c client.Client, name string) *platformv1alpha1.SSHTunnel {
	t.Helper()
	r := &SSHTunnelReconciler{Client: c}
	key := client.ObjectKey{Namespace: "ns", Name: name}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	tunnel := &platformv1alpha1.SSHTunnel{}
	if err := c.Get(context.Background(), key, tunnel); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	return tunnel
}

func TestSSHTunnelReconcileReady(t *testing.T) {
	c := newSSHTunnelTestClient(t, testSSHTunnel(), testSSHTunnelSecret())
	tunnel := reconcileSSHTunnel(t, c, "llm")
	if tunnel.Status.Phase != "Ready" {
		t.Fatalf("phase = %q, want Ready (conditions: %+v)", tunnel.Status.Phase, tunnel.Status.Conditions)
	}
	if len(tunnel.Status.Conditions) != 1 || tunnel.Status.Conditions[0].Status != metav1.ConditionTrue {
		t.Fatalf("conditions = %+v", tunnel.Status.Conditions)
	}
}

func TestSSHTunnelReconcileMissingSecret(t *testing.T) {
	c := newSSHTunnelTestClient(t, testSSHTunnel())
	tunnel := reconcileSSHTunnel(t, c, "llm")
	if tunnel.Status.Phase != "Invalid" {
		t.Fatalf("phase = %q, want Invalid", tunnel.Status.Phase)
	}
	if len(tunnel.Status.Conditions) != 1 || tunnel.Status.Conditions[0].Status != metav1.ConditionFalse {
		t.Fatalf("conditions = %+v", tunnel.Status.Conditions)
	}
}

func TestSSHTunnelReconcileMissingSecretKey(t *testing.T) {
	secret := testSSHTunnelSecret()
	delete(secret.Data, platformv1alpha1.SSHTunnelSecretKnownHostsKey)
	c := newSSHTunnelTestClient(t, testSSHTunnel(), secret)
	tunnel := reconcileSSHTunnel(t, c, "llm")
	if tunnel.Status.Phase != "Invalid" {
		t.Fatalf("phase = %q, want Invalid", tunnel.Status.Phase)
	}
	if !strings.Contains(tunnel.Status.Conditions[0].Message, platformv1alpha1.SSHTunnelSecretKnownHostsKey) {
		t.Fatalf("message = %q, want mention of missing known_hosts", tunnel.Status.Conditions[0].Message)
	}
}

func TestResolveSSHTunnel(t *testing.T) {
	c := newSSHTunnelTestClient(t, testSSHTunnel())
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "ns"}}

	got, err := resolveSSHTunnel(context.Background(), c, run)
	if err != nil || got != nil {
		t.Fatalf("no ref: tunnel=%v err=%v, want nil/nil", got, err)
	}

	run.Spec.SSHTunnelRef = &platformv1alpha1.NamedRef{Name: "llm"}
	got, err = resolveSSHTunnel(context.Background(), c, run)
	if err != nil || got == nil || got.Name != "llm" {
		t.Fatalf("resolved tunnel = %v, err = %v", got, err)
	}

	run.Spec.SSHTunnelRef = &platformv1alpha1.NamedRef{Name: "missing"}
	if _, err := resolveSSHTunnel(context.Background(), c, run); err == nil {
		t.Fatal("dangling sshTunnelRef must fail, not silently skip the tunnel")
	}
}

func TestEnsureSSHTunnelSidecar(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "ns"},
		Spec: platformv1alpha1.AgentRunSpec{
			OpenAIBaseURL: "https://api.openai.com/v1",
		},
	}
	tunnel := testSSHTunnel()
	podSpec := buildCommonPodSpec(run, "sa", []string{"/bin/agent"}, nil, nil, nil)
	ensureSSHTunnelSidecar(&podSpec, tunnel)

	var sidecar *corev1.Container
	for i := range podSpec.InitContainers {
		if podSpec.InitContainers[i].Name == sshTunnelContainerName {
			sidecar = &podSpec.InitContainers[i]
		}
	}
	if sidecar == nil {
		t.Fatalf("ssh-tunnel sidecar not found in init containers: %+v", podSpec.InitContainers)
	}
	if sidecar.RestartPolicy == nil || *sidecar.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Fatal("sidecar must be a native sidecar (restartPolicy Always)")
	}
	script := strings.Join(sidecar.Command, " ")
	for _, want := range []string{
		"StrictHostKeyChecking=yes",
		"ExitOnForwardFailure=yes",
		"BatchMode=yes",
		"IdentitiesOnly=yes",
		"-L 127.0.0.1:8434:127.0.0.1:8000",
		"-p 22",
		"tunnel@inference.example.com",
		"UserKnownHostsFile=/etc/ssh-tunnel/known_hosts",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("ssh command missing %q: %s", want, script)
		}
	}

	// Hardened sidecar security context.
	sc := sidecar.SecurityContext
	if sc == nil || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot ||
		sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem ||
		sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("sidecar security context not hardened: %+v", sc)
	}

	// The worker's OPENAI_BASE_URL must point at the loopback forward.
	worker := podSpec.Containers[0]
	var baseURL string
	for _, env := range worker.Env {
		if env.Name == "OPENAI_BASE_URL" {
			baseURL = env.Value
		}
	}
	if baseURL != "http://127.0.0.1:8434/v1" {
		t.Fatalf("worker OPENAI_BASE_URL = %q, want tunnel loopback URL", baseURL)
	}

	// The key volume must be mounted only in the sidecar, never the worker.
	for _, mount := range worker.VolumeMounts {
		if mount.Name == sshTunnelSecretVolume {
			t.Fatal("tunnel secret volume must not be mounted into the worker container")
		}
	}
	foundSecretMount := false
	for _, mount := range sidecar.VolumeMounts {
		if mount.Name == sshTunnelSecretVolume && mount.ReadOnly {
			foundSecretMount = true
		}
	}
	if !foundSecretMount {
		t.Fatal("tunnel secret volume must be mounted read-only into the sidecar")
	}
}

func TestEnsureSSHTunnelSidecarNoTunnel(t *testing.T) {
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "ns"}}
	podSpec := buildCommonPodSpec(run, "sa", []string{"/bin/agent"}, nil, nil, nil)
	before := len(podSpec.InitContainers)
	ensureSSHTunnelSidecar(&podSpec, nil)
	if len(podSpec.InitContainers) != before {
		t.Fatal("nil tunnel must not add a sidecar")
	}
}

func TestSSHTunnelDefaultsAndOverrides(t *testing.T) {
	tunnel := testSSHTunnel()
	tunnel.Spec.Port = 2222
	tunnel.Spec.LocalPort = 9000
	tunnel.Spec.RemoteHost = "10.0.0.5"
	tunnel.Spec.BaseURLPath = "v1beta"
	script := strings.Join(sshTunnelCommand(tunnel), " ")
	if !strings.Contains(script, "-L 127.0.0.1:9000:10.0.0.5:8000") || !strings.Contains(script, "-p 2222") {
		t.Fatalf("ssh command did not honor overrides: %s", script)
	}
	if got := sshTunnelBaseURL(tunnel); got != "http://127.0.0.1:9000/v1beta" {
		t.Fatalf("base URL = %q", got)
	}
}
