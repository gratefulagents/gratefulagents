# Repository Guidelines

## Project Structure & Module Organization
`cmd/main.go` starts the controller manager and optional dashboard; `cmd/agent/` contains operator-specific agent-run orchestration code. The reusable agent runtime is the open-source module `github.com/gratefulagents/sdk` (consumed as a versioned dependency; source under `pkg/agentsdk/`). CRD types live in `api/platform` and `api/triggers`, with reconcilers under `internal/controller/`. Shared backend services are in `internal/auth/`, `internal/store/`, `internal/projectstate/`, and `internal/dashboard/`. Vendored ConnectRPC protos and generated Go stubs live in `rpc/`. Kubernetes manifests are under `config/`; runtime role instructions, mode templates, and skill packages are under `configs/`.

The frontend (shared React source, web build target, and Tauri desktop/iOS shell) lives in `platform-app/` — a self-contained pnpm workspace in this repo. The `rpc/*.proto` files at the repo root are the single source of truth for the API contract: Go stubs are generated next to them (`make gen-protoc`), TypeScript stubs into `platform-app/frontend/src/rpc` via buf (`make gen-rpc` regenerates both). The web UI is not embedded in the Go binary: the dashboard serves the directory named by `DASHBOARD_WEB_DIST` and shows a placeholder page otherwise. Image builds feed `platform-app/` to the Dockerfile's web-builder stage so images ship the UI at `/web_dist`. End-to-end coverage lives in `test/e2e/`.

## Build, Test, and Development Commands
Use the root `Makefile` for most workflows:

- `make build`: build the Go manager into `bin/manager`.
- `make run-dashboard`: run the controller locally with the dashboard on `localhost:8090`.
- `make test`: run non-e2e Go tests with envtest and write `cover.out`.
- `make test-e2e`: run the Ginkgo e2e suite against Kind.
- `make lint` or `make lint-fix`: run `golangci-lint`.
- `make manifests generate` and `make gen-protoc`: regenerate CRDs, deepcopy code, and ConnectRPC artifacts after schema changes.
- `make gen-rpc`: regenerate both the Go and TypeScript RPC stubs from `rpc/*.proto` (requires `buf`).

Frontend work (React source, web build, Tauri shells) happens in `platform-app/`; see its README and Makefile (`make lint`, `make test`, `make tauri-dev`, ...). API changes start with the `.proto` files in `rpc/` at the repo root: run `make gen-rpc` to regenerate the Go and TypeScript stubs, then implement the server side here and the UI in `platform-app/frontend/src/`.

## Coding Style & Naming Conventions
Let tooling own formatting: Go uses `gofmt` and `goimports`. Follow existing names: lowercase Go packages and `*_test.go` tests. Do not hand-edit generated files such as `api/**/zz_generated.deepcopy.go` or `rpc/**/*connect`.

## Testing Guidelines
Add or update tests with every behavior change. Go tests use the standard testing package plus envtest; e2e uses Ginkgo/Gomega in `test/e2e/`. No fixed coverage threshold is enforced, but `make test` produces coverage output and should stay green before review.

## Dashboard Feature Parity
Any new user-facing or operator-configurable functionality must include corresponding dashboard controls in `platform-app/` as part of the same change. A backend API, CRD field, annotation, or configuration option alone is not considered complete. Include the RPC wiring, clear UI labels and help text, validation, and frontend tests needed to configure the feature without using Kubernetes manifests or command-line tools. If a dashboard control is intentionally inappropriate, document the reason in the pull request.

## Commit & Pull Request Guidelines
Recent commits favor short imperative subjects, usually with conventional prefixes such as `fix:`, `test:`, or `chore:`. Prefer `<type>: <summary>`. PRs should describe user-visible impact, linked issues, rollout or config changes, and the commands you ran. Include screenshots for frontend UI changes, and call out regenerated artifacts when schemas or embedded assets changed.
