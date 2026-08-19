---
title: SSH tunnels for self-hosted inference
seoTitle: Reach a Self-Hosted OpenAI Endpoint over SSH | GratefulAgents
description: Connect agent runs to a self-hosted, OpenAI-compatible inference server over a hardened per-run SSH tunnel with pinned host keys and a forward-only key.
agentPrompt: >-
  Read https://gratefulagents.dev/docs/settings/ssh-tunnels/ and help me connect my runs to a self-hosted OpenAI-compatible inference server through an SSHTunnel resource.
---

# SSH tunnels for self-hosted inference

If you host your own OpenAI-compatible inference server (vLLM, llama.cpp, Ollama, TGI, LM Studio, …) on a remote machine, an **SSHTunnel** resource lets agent runs reach it with SSH as the *only* exposed surface. The inference port itself never has to be reachable from the internet or even from the cluster network.

## How it works

When an `AgentRun` references an `SSHTunnel` (`spec.sshTunnelRef`), the platform injects a hardened SSH port-forward sidecar into the run pod and points the run's `OPENAI_BASE_URL` at the forward's loopback listener (for example `http://127.0.0.1:8434/v1`). Per run:

- The tunnel is **per pod**: traffic is encrypted end to end from the run pod to your server, and the forwarded port is bound to `127.0.0.1` inside the pod's network namespace only. Nothing is exposed on the cluster network.
- The SSH **private key is mounted only into the sidecar container** — the worker container that executes agent and repository code can never read it.
- Host keys are verified strictly against the `known_hosts` you provide (**no trust-on-first-use**), with `BatchMode`, `IdentitiesOnly`, and `ExitOnForwardFailure` enforced.
- The sidecar runs as a non-root user with all capabilities dropped, a read-only root filesystem, and the default seccomp profile. The worker only starts once the forward accepts connections.
- The tunnel works under the default `restricted` egress policy, which allows public-internet egress (your SSH server) while cluster-internal ranges stay blocked.

## 1. Harden the server side

Do this once on the machine that hosts your inference server. The goal: a dedicated account whose key can do exactly one thing — forward to the inference port.

1. Keep the inference server bound to loopback so SSH is the only way in:

   ```sh
   # e.g. vLLM
   vllm serve <model> --host 127.0.0.1 --port 8000
   ```

2. Create a dedicated no-shell user:

   ```sh
   sudo useradd --create-home --shell /usr/sbin/nologin llm-tunnel
   ```

3. Generate a dedicated ed25519 keypair (on your workstation, not the server):

   ```sh
   ssh-keygen -t ed25519 -f llm-tunnel-key -N '' -C 'gratefulagents-ssh-tunnel'
   ```

4. Install the public key with a **forward-only restriction** in `/home/llm-tunnel/.ssh/authorized_keys`:

   ```text
   restrict,port-forwarding,permitopen="127.0.0.1:8000" ssh-ed25519 AAAA... gratefulagents-ssh-tunnel
   ```

   `restrict` disables PTY allocation, X11, agent forwarding, and command execution; `permitopen` limits the key to the single inference port. Even a leaked key cannot log in or reach anything else.

5. Capture the server's host key for pinning:

   ```sh
   ssh-keyscan -t ed25519 inference.example.com > known_hosts
   ```

   Verify the fingerprint out of band (`ssh-keygen -lf known_hosts`) against the server's own `/etc/ssh/ssh_host_ed25519_key.pub` before trusting it.

## 2. Create the Secret

In the namespace where your runs execute, create a Secret with exactly these two keys:

```sh
kubectl create secret generic llm-tunnel \
  --from-file=privateKey=llm-tunnel-key \
  --from-file=known_hosts=known_hosts
```

## 3. Create the SSHTunnel resource

```yaml
apiVersion: platform.gratefulagents.dev/v1alpha1
kind: SSHTunnel
metadata:
  name: llm
spec:
  host: inference.example.com
  # port: 22            # SSH port (default 22)
  user: llm-tunnel
  # remoteHost: 127.0.0.1  # forward destination as seen from the SSH server
  remotePort: 8000      # the inference server's loopback port
  # localPort: 8434     # loopback listener inside the run pod (default 8434)
  # baseURLPath: /v1    # appended to http://127.0.0.1:<localPort> (default /v1)
  secretRef:
    name: llm-tunnel
  description: Self-hosted vLLM behind SSH
```

The controller validates the spec and the Secret's keys and reports `status.phase: Ready` (or `Invalid` with the reason) so you can catch misconfiguration before a run starts:

```sh
kubectl get sshtunnels
```

## 4. Reference the tunnel from a run

Set `spec.sshTunnelRef` on the `AgentRun` (alongside your model name and an API key credential if your server requires one):

```yaml
apiVersion: platform.gratefulagents.dev/v1alpha1
kind: AgentRun
spec:
  sshTunnelRef:
    name: llm
  model: openai/my-local-model
  # ...
```

When `sshTunnelRef` is set it takes precedence over `spec.openaiBaseURL`; the run's OpenAI-compatible traffic goes through the tunnel. A dangling reference fails the run pod instead of silently falling back to the public provider.

## Operational notes

- **Key rotation**: update the Secret and start a new run; running pods keep their established session.
- **Custom sidecar image**: `spec.image` overrides the operator-configured tunnel image (`agentImages.sshTunnel` in the Helm chart). The image must provide `/bin/sh`, an OpenSSH client, and `nc`.
- **Reconnects**: the sidecar exits on any forward failure or dead peer (keepalives every 15 s) and is restarted by the kubelet, re-establishing the tunnel.
- **Multiple servers**: create one `SSHTunnel` per endpoint; each run references exactly one.
