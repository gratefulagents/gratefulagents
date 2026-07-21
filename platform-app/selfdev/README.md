# Platform app self-development harness

Run and inspect the platform UI inside an AgentRun without a Kubernetes cluster, real backend, or Rust/Tauri build. The harness starts a deterministic fake ConnectRPC backend, starts the web target with Vite, and drives the system Chromium through Playwright.

## Prerequisites

Use the default multi-language AgentRun runtime image. It includes Node 22, pnpm, and Debian Chromium. Then run:

```bash
cd platform-app
make selfdev-doctor
```

The doctor performs a real headless Chromium launch and probes the fake backend. Binary presence alone is not considered healthy.

If Chromium cannot launch because `/proc` is unavailable in the command sandbox, configure the run's `RuntimeProfile` with:

```yaml
spec:
  sandbox:
    enablePrivateProcfs: true
```

That setting requires pod user-namespace support and a Pod Security policy that permits `procMount: Unmasked`.

## Typical AgentRun loop

```bash
cd platform-app
pnpm install --frozen-lockfile       # once per sandbox
make selfdev-snap ROUTE=/runs/demo/run-ui-polish
# edit frontend/src/...
make selfdev-snap ROUTE=/runs/demo/run-ui-polish
make selfdev-test
make typecheck
```

A capture writes three files under `selfdev/out/`:

- `*.png`: viewport or full-page screenshot
- `*.aria.yml`: accessibility tree for fast structural and copy review
- `*.console.log`: console errors/warnings, page errors, failed requests, and HTTP responses with status 400 or higher

Screenshot commands exit non-zero when they capture a console or network finding, so AgentRuns and CI cannot silently accept a broken page. Use `--allow-findings` only when intentionally capturing a known error and after inspecting the log.

## Commands

The Make targets cover the common workflow:

```bash
make selfdev-doctor
make selfdev-serve SCENARIO=default
make selfdev-snap ROUTE=/projects THEME=dark SCENARIO=default
make selfdev-snap-all SCENARIO=default
make selfdev-test
```

Use the package CLI for additional flags:

```bash
pnpm --filter selfdev run screenshot --route /settings/credentials --theme both
pnpm --filter selfdev run snap-all --scenario empty --tauri-sim --platform macos
pnpm --filter selfdev run serve --scenario error --ui-port 5199
pnpm --filter selfdev run fake-server --scenario default --port 8090
```

### Shared flags

| Flag | Values / default | Purpose |
| --- | --- | --- |
| `--scenario` | `default`, `empty`, `error`; default `default` | Select deterministic fixture data. |
| `--theme` | `dark`, `light`, `both`; screenshot default `dark`, snap-all default `both` | Select the color scheme. |
| `--viewport` | `WIDTHxHEIGHT`; default `1440x900` | Set the browser viewport. |
| `--tauri-sim` | off by default | Install a browser-side Tauri API simulation before app startup. |
| `--platform` | `macos`, `ios`, `linux`, `windows`; default `macos` | Set the OS reported by Tauri simulation. |
| `--full-page` | off by default | Capture the full scrollable document. |
| `--steps` | JSON file path | Perform interactions before capture. |
| `--wait-for` | CSS selector; default `#main-content` | Wait for a visible readiness element. |
| `--settle` | milliseconds; default `700` | Wait after readiness before capture. |
| `--no-auth` | off by default | Skip sign-in, for capturing login or connection screens. |
| `--out` | directory; default `selfdev/out` | Change the output directory. |
| `--ui-port` | default `5199` | Change the Vite port. |
| `--backend-port` / `--port` | screenshot default ephemeral; fake-server default `8090` | Change the fake backend port. |
| `--allow-findings` | off by default | Keep exit status zero despite recorded findings. |

`screenshot` requires `--route /path` (or the route as its first positional argument). `snap-all` reads the selected scenario's route catalog.

### Interaction steps

`--steps` accepts a JSON array. Supported actions are documented by the type and parser in `src/browser/steps.ts`; invalid steps fail before Chromium starts. A typical file looks like:

```json
[
  { "action": "click", "selector": "role=button[name='New project']" },
  { "action": "fill", "selector": "#project-name", "value": "demo" },
  { "action": "wait", "ms": 250 }
]
```

## Scenarios

- `default`: populated workspace covering active, review, successful, failed, queued, and team runs plus projects, triggers, Slack, resources, settings, diffs, traces, and reviews.
- `empty`: first-use and empty-list states.
- `error`: failed or blocked runs and unhealthy integrations.

Fixtures live in `src/fixtures/` and are constructed from generated protobuf schemas. Keep each parameterized screenshot route backed by a matching fixture resource; unit tests enforce this.

## Tauri simulation

`--tauri-sim` covers desktop-only frontend behavior such as the title bar, workspace switcher, connection controls, OAuth cards, and local credential import. It does not run the Rust shell or validate native capabilities. Rust-side changes still require the appropriate native build on their target OS.

Unknown simulated Tauri commands are written as console warnings and therefore fail screenshots by default. Add intentional commands to `src/browser/tauri-sim.ts`.

## Troubleshooting

- **No Chromium found:** use the default runtime image or set `SELFDEV_CHROMIUM=/absolute/path/to/chromium`.
- **Chromium launch fails with `/proc` errors:** enable private procfs as described under Prerequisites.
- **Vite port is in use:** pass `--ui-port` with an unused port.
- **Blank content:** inspect the matching `*.console.log` and `*.aria.yml`; unmatched React routes appear as console warnings.
- **A new RPC returns empty data:** implement it in `src/server/fake-backend.ts` and add typed fixture data.
- **A Tauri-only action warns:** extend `src/browser/tauri-sim.ts`.
