---
title: Publish securely with Cloudflare
agentPrompt: >-
  Read https://gratefulagents.dev/docs/getting-started/cloudflare-access/ and publish my gratefulagents workspace on a public HTTPS hostname behind Cloudflare Access. Ask me for the domain to use, then set up the tunnel and the access policy.
---

# Publish securely with Cloudflare

Use Cloudflare Tunnel to give a k3s deployment a public HTTPS hostname without opening the dashboard's port `8090` to the Internet. Then protect that hostname with Cloudflare Access.

This guide uses the default release name and namespace. If you changed `RELEASE_NAME` or `NAMESPACE` during installation, substitute your values in the service hostname and commands.

:::warning Public hostname does not mean public access
A Tunnel route makes the hostname reachable from the Internet. Until you add an Access application and policy, anyone who knows the hostname can reach the GratefulAgents sign-in page. Create the Access policy immediately, keep port `8090` closed at the host and provider firewalls, and continue to use GratefulAgents sign-in behind Access.
:::

## Before you begin

You need:

- a [k3s installation](./self-hosting-k3s.md);
- a domain added to the same Cloudflare account;
- permission to manage Cloudflare Tunnels, DNS, and Access; and
- outbound connectivity from the k3s server to Cloudflare. No inbound dashboard port is required.

Kind is intended for local evaluation and binds the dashboard to `127.0.0.1`. Use k3s for this Internet-facing path.

## Understand the two Cloudflare credentials

Cloudflare uses two unrelated credentials in this setup:

| Credential | Where it is used | What it does |
| --- | --- | --- |
| **Tunnel token** | k3s installer and the `cloudflared` pod | Connects this cluster to one remotely managed Tunnel. Never enter it in the GratefulAgents app. |
| **Access service token** | GratefulAgents iOS, Android, or desktop connection screen | Supplies a `CF-Access-Client-Id` and `CF-Access-Client-Secret` so the installed app can pass an Access policy. Never use it as the Tunnel token. |

Both are secrets. Use separate, short-lived Access service tokens for different people or devices when practical so one can be revoked without disconnecting everyone.

## 1. Create a remotely managed Tunnel

1. In the Cloudflare dashboard, go to **Networking → Tunnels**.
2. Select **Create a tunnel**, name it (for example, `gratefulagents-k3s`), and create it.
3. Cloudflare displays an installation command containing `--token`. Copy only the token value and keep it in a password manager. Do not run Cloudflare's host installation command; the GratefulAgents installer deploys `cloudflared` in Kubernetes.

Cloudflare shows the Tunnel as inactive until its connector starts.

## 2. Deploy the connector in k3s

Rerun the installer with the dashboard kept inside the cluster and the Tunnel token supplied for the first deployment:

```bash
DASHBOARD_SERVICE_TYPE=ClusterIP \
INSTALL_CLOUDFLARE_WARP=1 \
CLOUDFLARE_TUNNEL_TOKEN='replace-with-tunnel-token' \
./scripts/install-k3s.sh
```

The installer stores the token in the `cloudflare-tunnel-token` Kubernetes Secret and deploys `cloudflared` in the `cloudflare-warp` namespace. Later installer runs reuse the Secret, so omit `CLOUDFLARE_TUNNEL_TOKEN` unless you are replacing it.

Avoid saving the token in a shell profile or source-controlled file. It may also remain in your shell history when entered inline; clear that entry according to your shell's instructions or supply the variable from a trusted secret-management process.

Confirm that the connector is ready:

```bash
kubectl -n cloudflare-warp rollout status deployment/cloudflared
kubectl -n cloudflare-warp get pods
```

The Tunnel should now show **Healthy** in Cloudflare.

## 3. Add the public hostname

1. In **Networking → Tunnels**, select the Tunnel.
2. On **Routes**, choose **Add route → Published application**.
3. Choose a hostname, such as `agents.example.com`.
4. Set **Service URL** to the in-cluster dashboard service:

   ```text
   http://gratefulagents-controller-manager-dashboard.gratefulagents-system.svc.cluster.local:8090
   ```

5. Save the route.

Cloudflare creates or associates the DNS route and terminates public HTTPS. Do not change the service URL to the server's public IP, and do not open inbound `8090` merely to make the Tunnel work.

If you enabled Google sign-in, add the final `https://agents.example.com` origin and the callback URL expected by your deployment to the Google OAuth configuration.

## 4. Protect the hostname with Access

Create the protection before distributing the URL:

1. Go to **Zero Trust → Access controls → Applications**.
2. Add a **Self-hosted** application for the exact hostname, such as `agents.example.com`.
3. Add an **Allow** policy for the users, email domains, or identity-provider groups that may use the web app.
4. Set a short session duration appropriate for your organization and save the application.
5. Open the hostname in a private browser window and confirm Cloudflare requests identity before GratefulAgents loads.

An Access login controls who can reach the service; GratefulAgents sign-in still controls the user's account and role inside the service. Keep both layers enabled.

## 5. Create an Access service token for installed apps

The iOS, Android, and desktop apps can send Cloudflare Access service-token headers on every request:

1. Go to **Zero Trust → Access controls → Service credentials → Service Tokens**.
2. Select **Create Service Token**, give it a device- or user-specific name, and choose an expiration.
3. Generate the token and securely copy both values. Cloudflare displays the client secret only once.
4. Return to the Access application and add a policy with action **Service Auth**.
5. Configure the policy to include the service token you created, then save it.

Give the app user these three values through a secure channel:

- Operator URL: `https://agents.example.com`
- CF Access client ID
- CF Access client secret

The client ID and secret are entered in the installed app as described in [Install mobile and desktop apps](./web-desktop-workspaces.md). Do not put an Access service token in a browser URL, documentation screenshot, issue, or chat.

:::note Browser and installed-app policies can coexist
Use the **Allow** policy for interactive browser users and the **Service Auth** policy for installed apps. A service-token-only application will not provide the normal identity-provider login flow to browser users.
:::

## Verify and operate the setup

After connecting an installed app, verify:

- the app reaches `https://agents.example.com` and then shows GratefulAgents sign-in;
- omitting or changing the Access service token causes Cloudflare to reject the app connection;
- a browser without an allowed Access identity cannot reach GratefulAgents; and
- inbound port `8090` is closed in both the provider and host firewalls.

Monitor **Zero Trust → Logs → Access** for rejected requests. Rotate an Access service token before it expires, update each affected device, and delete the old token. If a device is lost, delete its service token; ending Access sessions alone does not revoke a still-valid client ID and secret.

For Cloudflare's current dashboard flow, see [Create a remotely managed Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/create-remote-tunnel/) and [Service tokens](https://developers.cloudflare.com/cloudflare-one/access-controls/service-credentials/service-tokens/).
