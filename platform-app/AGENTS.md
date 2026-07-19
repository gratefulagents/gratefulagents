# AGENTS.md — working on platform-app as an agent

This directory holds the gratefulagents UI: one React source of truth
(`frontend/`) with two thin build targets (`web/` for the browser, `tauri/`
for the macOS/iOS shell). It is a self-contained pnpm workspace inside the
gratefulagents repo. See `README.md` here and `frontend/README.md` for
architecture.

## Self-dev mode: run, drive, and screenshot the UI

You can develop the UI end-to-end **inside your run sandbox** — no cluster, no
real backend, no Rust build. The `selfdev/` package boots the app against a
fake ConnectRPC backend with deterministic fixtures and screenshots it with
the system Chromium.

**Requirement:** your run must use the **default (multi-language) runtime
image** (it ships Node 22, pnpm, and Debian `chromium`). Language-specific
images (go, python, …) lack Node/Chromium. Check with `make selfdev-doctor`,
which verifies an actual headless Chromium launch rather than binary presence.
On Kubernetes runtimes whose OCI `/proc` masks force Bubblewrap to provide an
empty `/proc`, the run's RuntimeProfile must set
`spec.sandbox.enablePrivateProcfs: true`; this requires pod-user-namespace
support and a Pod Security policy that permits `procMount: Unmasked`.

### The loop

```bash
pnpm install --frozen-lockfile                  # once per sandbox
make selfdev-snap ROUTE=/runs/demo/run-ui-polish   # before your change
# … edit frontend/src …
make selfdev-snap ROUTE=/runs/demo/run-ui-polish   # after — compare
```

Each shot writes three files to `selfdev/out/` (gitignored):

- `<name>.png` — the screenshot (inspect with your vision capability)
- `<name>.aria.yml` — accessibility tree (cheap text diff; often enough
  to verify structure/copy without vision)
- `<name>.console.log` — console errors/warnings, page errors, failed
  requests (a blank page almost always explains itself here)

### Commands

```bash
make selfdev-doctor                    # verify chromium + fake backend boot
make selfdev-snap ROUTE=/... [THEME=dark|light|both] [SCENARIO=default|empty|error]
make selfdev-snap-all [SCENARIO=...]   # every fixture route — whole-UI smoke
make selfdev-serve                     # interactive UI + fake backend
make selfdev-test                      # harness unit tests
```

Full flag reference (`--tauri-sim`, `--viewport`, `--steps`, `--full-page`,
`--no-auth`, …): `selfdev/README.md`.

### Fixtures ("fake information")

Scenarios live in `selfdev/src/fixtures/` and are built from the generated
proto schemas (`frontend/src/rpc`), so they type-check against the real API:

- `default` — busy workspace: runs in every phase (chat, plan-review with
  pending actions, team, failed, queued), activity log with tool calls and a
  sub-agent, PR with checks + review thread, usage, diff, trace, projects,
  Linear/GitHub/Cron triggers, Slack agents + drafts, skills, credentials,
  soul, git identity, notifications, shares.
- `empty` — first boot, all empty states.
- `error` — failed/blocked runs, unhealthy triggers, disconnected Slack.

Route catalogs for `snap-all` live on each scenario (`routes`). Param routes
reference fixture resources, e.g. `/runs/demo/run-ui-polish`.

Fixture timestamps derive from a frozen clock (`selfdev/src/time.ts`) and the
harness installs the same instant as the page clock, so "3m ago" renders
identically on every run — screenshots are diffable.

### Tauri-only UI

`--tauri-sim` fakes `window.__TAURI_INTERNALS__` before the app loads, so
`isTauri` flips true in the browser and desktop-only surfaces render
(TitleBar, WorkspaceSwitcher, OAuth connect cards, local-credential import,
Connection settings). `--platform macos|ios` controls the reported OS:

```bash
pnpm --filter selfdev run screenshot --route /settings/credentials --tauri-sim --platform macos
```

The real Rust shell is **not** exercised (deliberate: WebKitGTK ≠ WKWebView,
and the Rust build is heavy). Rust-side changes (`tauri/src-tauri`) still need
a macOS build.

### Extending the harness

- UI starts calling a new RPC → implement it in
  `selfdev/src/server/fake-backend.ts` (unimplemented methods already return
  empty responses, never errors).
- New page/state worth screenshotting → add fixture data + a `routes` entry.
- New `@tauri-apps/*` call → add the command to
  `selfdev/src/browser/tauri-sim.ts` (unknown commands warn into the console
  log so you'll notice).

## Quality gates before a PR

```bash
make lint             # ESLint  (or: pnpm --filter @operator/frontend lint)
make test             # Vitest  (or: pnpm --filter @operator/frontend test)
make typecheck        # tsc -b via web target
make selfdev-test     # if you touched selfdev/
```

For UI changes, attach evidence: capture before/after screenshots of the
affected routes with `make selfdev-snap` and summarize what changed visually.

## Repo conventions

- UI source only in `frontend/src` — never fork components into `web/` or
  `tauri/`.
- `@tauri-apps/*` imports must stay dynamic and `isTauri`-guarded so the
  browser build keeps working (see `frontend/src/lib/platform.ts`).
- The RPC contract lives in `rpc/*.proto` at the repo root; regenerate the
  stubs with `make gen-rpc` (never hand-edit `frontend/src/rpc/**`).
