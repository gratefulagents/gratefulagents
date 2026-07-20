# gratefulagents

<p>
  <img src="user-docs/static/img/logo.png" alt="GratefulAgents" width="128" />
</p>

**gratefulagents** is an open-source, self-hostable control plane for running AI agent workflows against repositories. It combines a Kubernetes controller, a dashboard, custom resources, and isolated agent-run environments for interactive work, plans, pull requests, reviews, and trigger-driven automation.

Website and user documentation: [gratefulagents.dev](https://gratefulagents.dev/)

## ❗ Early development — expect bugs

This project is in early development, and bugs are expected. If you find one, help the maintainers improve gratefulagents by [creating a GitHub issue](https://github.com/gratefulagents/gratefulagents/issues/new).

## Architecture

- The controller manager reconciles platform and trigger resources, exposes the dashboard, and can receive GitHub webhooks.
- Kubernetes CRDs represent resources such as `AgentRun`, `Project`, `GitHubRepository`, `LinearProject`, `SlackAgent`, and `Cron`.
- [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) provides the sandbox infrastructure used for agent-run workloads.
- The chart can deploy PostgreSQL with pgvector, MinIO, and Jaeger. The installers generate persistent credentials for the bundled PostgreSQL and MinIO services.

## Requirements and support matrix

| Path | Intended environment | What it installs | Image source | Notes |
| --- | --- | --- | --- | --- |
| [Kind](user-docs/docs/getting-started/self-hosting-kind.md) | macOS or Linux laptop with a Docker-compatible runtime | Single-node Kind cluster | Latest published `ghcr.io/gratefulagents/{controller,worker,injector}:<release-tag>` | Local evaluation path; dashboard binds to `127.0.0.1`. |
| [k3s](user-docs/docs/getting-started/self-hosting-k3s.md) | Fresh Debian/Ubuntu single-node server | k3s, chart, and dependencies | Latest published `ghcr.io/gratefulagents/{controller,worker,injector}:<release-tag>` | Self-hosting path; the installer changes the host and needs sudo. |
| Desktop app | Apple Silicon macOS; AMD64 or ARM64 Linux | Desktop client | GitHub release artifacts | Use only an artifact you can verify. The current convenience installer is not recommended for security-sensitive devices; review the [workspace guide](user-docs/docs/getting-started/web-desktop-workspaces.md). |

The project does not document high availability, managed-hosting, a production support SLA, or a supported k3s removal workflow at this release.

## Quick starts

### Run the latest published release locally

Use Kind when you want the newest released controller, worker, and injector images without building them locally:

```sh
git clone --branch main --depth 1 https://github.com/gratefulagents/gratefulagents.git
cd gratefulagents
make kind-install
```

The installer uses a dedicated kubeconfig and stores credentials and state below `~/.config/gratefulagents/kind` by default. It prints the local dashboard URL and the command to retrieve the generated `admin` password. See the [Kind guide](user-docs/docs/getting-started/self-hosting-kind.md) for requirements, overrides, state locations, and deletion.

### Run the latest published release on k3s

Use a fresh Debian or Ubuntu server for the supported self-hosting path:

```sh
git clone https://github.com/gratefulagents/gratefulagents.git
cd gratefulagents
make k3s-install
```

The k3s installer resolves the latest GitHub release tag, deploys the matching public GHCR images, and installs the chart with a private persistent values file. It does not build images or create a node-local registry. It is not equivalent to `helm install` with chart defaults: the defaults contain sample bundled-service credentials. Follow the [k3s guide](user-docs/docs/getting-started/self-hosting-k3s.md) for network exposure, upgrades, backups, and removal/data-retention warnings.

## Develop from source

Read [AGENTS.md](AGENTS.md) before changing backend, API, chart, or generated artifacts. For the frontend, read [platform-app/AGENTS.md](platform-app/AGENTS.md); it is a separate pnpm workspace. The root Makefile currently exposes supported self-hosting commands:

```sh
make help
```

### Source-development checks

Build the user-documentation site from its workspace:

```sh
cd user-docs
pnpm install --frozen-lockfile
pnpm build
```

For frontend changes, use the workspace's lint and strict type-check commands:

```sh
cd platform-app
make lint
make typecheck
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the backend-generation boundaries, UI screenshot evidence, test commands, and scoped validation guidance.

## Documentation and community

- [Website and user documentation](https://gratefulagents.dev/)
- [Self-host with Kind](user-docs/docs/getting-started/self-hosting-kind.md)
- [Self-host with k3s](user-docs/docs/getting-started/self-hosting-k3s.md)
- [Troubleshooting](user-docs/docs/troubleshooting/common-issues.md)
- [Contributing](CONTRIBUTING.md)
- [Security reporting](SECURITY.md)
- [License](LICENSE)

For non-security bugs and feature requests, use the [GitHub issue tracker](https://github.com/gratefulagents/gratefulagents/issues). Do not include credentials, tokens, private keys, webhook secrets, or private repository data in public reports.

## License

[GNU Affero General Public License v3.0](LICENSE).
