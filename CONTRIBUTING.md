# Contributing to gratefulagents

Thanks for contributing. Start by reading [AGENTS.md](AGENTS.md). It defines the repository layout, generated-code boundaries, API workflow, and pull-request conventions. Frontend work has additional requirements in [platform-app/AGENTS.md](platform-app/AGENTS.md).

## Choose the right area

| Area | Source of truth | Notes |
| --- | --- | --- |
| Controller, APIs, CRDs, chart, and runtime configuration | Repository root | API changes begin in root `rpc/**/*.proto`; do not hand-edit generated Go or ConnectRPC files. |
| Shared frontend, browser build, and Tauri shells | `platform-app/frontend/` | Keep shared UI source in `frontend/src`; `web/` and `tauri/` are thin targets. |
| User documentation | `user-docs/` | Docusaurus workspace. |

Follow Go formatting through `gofmt`/`goimports`. Use lowercase Go package names and `*_test.go` for Go tests. Do not edit generated files such as `api/**/zz_generated.deepcopy.go`, `rpc/**/*connect`, or `platform-app/frontend/src/rpc/**` by hand.

## Set up and inspect available commands

The root Makefile currently exposes the supported self-hosting workflow. Inspect it rather than assuming development targets are available:

```sh
make help
```

For documentation changes, install dependencies and build the Docusaurus site:

```sh
cd user-docs
pnpm install --frozen-lockfile
pnpm build
```

For frontend work, use the `platform-app` workspace. Its Makefile is the current command reference:

```sh
cd platform-app
make install
make lint
make typecheck
make test
```

For a UI change, `platform-app/AGENTS.md` requires before-and-after evidence from the self-development harness. The documented commands are:

```sh
cd platform-app
make selfdev-doctor
make selfdev-snap ROUTE=/runs/demo/run-ui-polish
```

Use the route and scenario that exercise the changed UI. Review the generated screenshot, accessibility tree, and console log under `platform-app/selfdev/out/` before including evidence in the pull request.

`AGENTS.md` describes backend validation and generation workflows (`make build`, `make test`, `make lint`, `make manifests generate`, `make gen-protoc`, and `make gen-rpc`). Run `make help` to inspect all available root targets. When an API changes, regenerate both Go and TypeScript RPC stubs with the project-supported generation workflow; never patch generated output.

## Make a focused change

1. Open an issue first for a substantial change, or explain the problem clearly in the pull request.
2. Keep the change scoped. Include matching dashboard controls for user-facing or operator-configurable backend features, unless the pull request explains why a control is inappropriate.
3. Update user-facing documentation and tests with behavior changes.
4. Run the relevant available validation for the files you changed. Do not claim commands that you did not run.
5. Use a short imperative commit subject. Recent history commonly uses conventional prefixes such as `fix:`, `feat:`, `test:`, or `chore:`.

## Releases

Releases are automated from Conventional Commit messages on `main`. Both
`fix:` and `feat:` commits create a patch release, while a commit with a
`BREAKING CHANGE:` footer (or a `!` type modifier) creates a major release.
Other commit types do not publish a release by default.

The release workflow calculates the next version, creates the Git tag and a
draft GitHub Release, stamps the version into the controller and native apps,
and uploads the images and release artifacts. The draft is published only
after every artifact and the updater manifest are available. A failed release
run removes its draft and tag so a later run can reuse the version. No release
pull request or checked-in version file is used.

## Open a pull request

Describe:

- the user-visible impact;
- linked issue(s);
- configuration, rollout, or migration considerations;
- validation commands actually run and their results; and
- regenerated artifacts, if schemas or embedded assets changed.

Attach before-and-after screenshots for frontend changes. Keep secrets, tokens, private keys, webhook payloads, customer data, and private repository URLs out of issues, commits, screenshots, and pull-request descriptions. Report vulnerabilities through the process in [SECURITY.md](SECURITY.md), not a public issue.
