/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package platform

import (
	"context"
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

const (
	// SSH tunnel sidecar wiring (spec.sshTunnelRef). The sidecar forwards
	// 127.0.0.1:<localPort> inside the run pod to the inference endpoint
	// behind the tunnel's SSH server; the worker only ever sees the loopback
	// listener. The private key is mounted exclusively into the sidecar
	// container, so agent-executed code can never read it.
	sshTunnelContainerName = "ssh-tunnel"
	sshTunnelSecretVolume  = "ssh-tunnel-secret"
	sshTunnelHomeVolume    = "ssh-tunnel-home"
	sshTunnelSecretDir     = "/etc/ssh-tunnel"
	sshTunnelHomeDir       = "/home/tunnel"
	defaultSSHTunnelImage  = "ghcr.io/gratefulagents/tunnel:latest"
	sshTunnelRunAsUID      = 65532
	sshTunnelRunAsGID      = 65532
)

// resolveSSHTunnel fetches the SSHTunnel referenced by the run, or nil when
// the run does not use a tunnel. A dangling reference is an error: a run that
// asked for a tunnel must never start pointed at the public provider instead.
func resolveSSHTunnel(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun) (*platformv1alpha1.SSHTunnel, error) {
	if run == nil || run.Spec.SSHTunnelRef == nil || strings.TrimSpace(run.Spec.SSHTunnelRef.Name) == "" {
		return nil, nil
	}
	name := strings.TrimSpace(run.Spec.SSHTunnelRef.Name)
	tunnel := &platformv1alpha1.SSHTunnel{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, tunnel); err != nil {
		return nil, fmt.Errorf("resolving sshTunnelRef %q: %w", name, err)
	}
	return tunnel, nil
}

func sshTunnelLocalPort(tunnel *platformv1alpha1.SSHTunnel) int32 {
	if tunnel.Spec.LocalPort > 0 {
		return tunnel.Spec.LocalPort
	}
	return 8434
}

func sshTunnelBaseURL(tunnel *platformv1alpha1.SSHTunnel) string {
	path := strings.TrimSpace(tunnel.Spec.BaseURLPath)
	if path == "" {
		path = "/v1"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return fmt.Sprintf("http://127.0.0.1:%d%s", sshTunnelLocalPort(tunnel), path)
}

// sshTunnelCommand renders the hardened ssh invocation. The shell prelude
// copies the mounted key into the sidecar-private HOME with 0600 permissions
// (OpenSSH rejects group/world-readable keys, and Secret volume modes cannot
// express per-uid ownership), then execs ssh with:
//   - BatchMode / ExitOnForwardFailure: fail fast instead of hanging or
//     silently running without the forward; the pod restart policy retries.
//   - StrictHostKeyChecking=yes against only the pinned known_hosts from the
//     Secret — no trust-on-first-use, no global known hosts.
//   - IdentitiesOnly with the single mounted key; no agent, no defaults.
//   - keepalives so a dead peer tears the forward down promptly.
//   - a loopback-only -L bind, so the forward is reachable from the run pod's
//     network namespace only.
func sshTunnelCommand(tunnel *platformv1alpha1.SSHTunnel) []string {
	port := tunnel.Spec.Port
	if port <= 0 {
		port = 22
	}
	remoteHost := strings.TrimSpace(tunnel.Spec.RemoteHost)
	if remoteHost == "" {
		remoteHost = "127.0.0.1"
	}
	script := strings.Join([]string{
		"set -eu",
		"umask 077",
		`mkdir -p "$HOME/.ssh"`,
		fmt.Sprintf(`cp %s/%s "$HOME/.ssh/key"`, sshTunnelSecretDir, platformv1alpha1.SSHTunnelSecretPrivateKeyKey),
		`chmod 0600 "$HOME/.ssh/key"`,
		"exec ssh -N" +
			" -o BatchMode=yes" +
			" -o ExitOnForwardFailure=yes" +
			" -o StrictHostKeyChecking=yes" +
			fmt.Sprintf(" -o UserKnownHostsFile=%s/%s", sshTunnelSecretDir, platformv1alpha1.SSHTunnelSecretKnownHostsKey) +
			" -o GlobalKnownHostsFile=/dev/null" +
			" -o IdentitiesOnly=yes" +
			` -i "$HOME/.ssh/key"` +
			" -o ServerAliveInterval=15" +
			" -o ServerAliveCountMax=3" +
			" -o ConnectTimeout=10" +
			fmt.Sprintf(" -L 127.0.0.1:%d:%s:%d", sshTunnelLocalPort(tunnel), remoteHost, tunnel.Spec.RemotePort) +
			fmt.Sprintf(" -p %d", port) +
			fmt.Sprintf(" -- %s@%s", tunnel.Spec.User, tunnel.Spec.Host),
	}, "\n")
	return []string{"/bin/sh", "-c", script}
}

// ensureSSHTunnelSidecar wires the resolved SSHTunnel into the run pod: a
// non-root, no-capability native sidecar (init container with restartPolicy
// Always, so the pod still completes when the worker exits) holding the SSH
// forward, plus an OPENAI_BASE_URL override on the worker pointing at the
// forward's loopback listener.
//
// Call after buildCommonPodSpec so the override wins over spec.openaiBaseURL.
func ensureSSHTunnelSidecar(podSpec *corev1.PodSpec, tunnel *platformv1alpha1.SSHTunnel) {
	if podSpec == nil || len(podSpec.Containers) == 0 || tunnel == nil {
		return
	}
	// Secret files default to 0644 root-owned; 0444 keeps them readable by
	// the sidecar's non-root uid. Only the sidecar mounts this volume, and it
	// re-copies the key to 0600 before ssh sees it (see sshTunnelCommand).
	secretMode := int32(0o444)
	podSpec.Volumes = append(podSpec.Volumes,
		corev1.Volume{
			Name: sshTunnelSecretVolume,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName:  tunnel.Spec.SecretRef.Name,
				DefaultMode: &secretMode,
			}},
		},
		corev1.Volume{
			Name:         sshTunnelHomeVolume,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
	)
	restartAlways := corev1.ContainerRestartPolicyAlways
	localPort := sshTunnelLocalPort(tunnel)
	podSpec.InitContainers = append(podSpec.InitContainers, corev1.Container{
		Name:            sshTunnelContainerName,
		Image:           firstNonEmpty(tunnel.Spec.Image, os.Getenv("SSH_TUNNEL_IMAGE"), defaultSSHTunnelImage),
		ImagePullPolicy: corev1.PullIfNotPresent,
		RestartPolicy:   &restartAlways,
		Command:         sshTunnelCommand(tunnel),
		Env:             []corev1.EnvVar{{Name: "HOME", Value: sshTunnelHomeDir}},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:                int64Ptr(sshTunnelRunAsUID),
			RunAsGroup:               int64Ptr(sshTunnelRunAsGID),
			RunAsNonRoot:             boolPtr(true),
			AllowPrivilegeEscalation: boolPtr(false),
			ReadOnlyRootFilesystem:   boolPtr(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: sshTunnelSecretVolume, MountPath: sshTunnelSecretDir, ReadOnly: true},
			{Name: sshTunnelHomeVolume, MountPath: sshTunnelHomeDir},
		},
		// Gate the worker's start on the forward accepting connections so the
		// first model request does not race SSH session establishment. The
		// exec probe dials from inside the pod's network namespace, which is
		// the only place the loopback-bound forward is visible.
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
				Command: []string{"/bin/sh", "-c", fmt.Sprintf("nc -z 127.0.0.1 %d", localPort)},
			}},
			PeriodSeconds:    2,
			FailureThreshold: 60,
		},
		Ports: []corev1.ContainerPort{{Name: "openai", ContainerPort: localPort, Protocol: corev1.ProtocolTCP}},
	})

	worker := &podSpec.Containers[0]
	upsertContainerEnv(worker, corev1.EnvVar{Name: "OPENAI_BASE_URL", Value: sshTunnelBaseURL(tunnel)})
}
