# Sub-agent roles

A `RoleInstruction` is a sub-agent the parent agent can delegate to. Roles are defined by capability, the way Claude Code and Codex define their sub-agents: what the sub-agent may touch (`toolAccess`), and optionally how hard it thinks (`reasoningLevel`). They are not personas. The parent's task message carries the task-specific instructions.

## Shipped roles

| Role | Tool access | Use |
| --- | --- | --- |
| `general` | full | Implementation, fixes, test runs, any self-contained multi-step task |
| `explore` | read-only, low reasoning | Specific codebase questions, keeping search output out of the parent's context |
| `reviewer` | read-only | An independent second look at a plan, diff, or diagnosis |

The runtime always offers `general`, even when no `general` RoleInstruction exists. Its built-in prompt lives in `internal/agentroles`, and `internal/configtest` keeps `general.yaml` identical to it. Creating a RoleInstruction named `general` replaces the built-in.

## How a sub-agent's prompt is built

1. The shared base prompt (`agentroles.SharedInstructions`): scope discipline, faithful reporting, workspace etiquette, and the report format. Every role gets it, including user-created roles.
2. The role's `instructions`.
3. Workspace and turn-budget context added by the SDK.
4. The task message written by the parent.

Keep role instructions short. Anything task-specific belongs in the parent's message, and rules every sub-agent needs belong in the shared base prompt.

## Models

Shipped roles do not pin a model, so sub-agents use the parent run's model. Pin `modelsByProvider` only for a deliberate cost or latency trade-off. A pinned model does not follow the parent when it moves to a newer model.

## Editing

`configs/roleinstructions/` is canonical. Run `make helm-sync-bootstrap` to mirror it into `dist/chart/files/bootstrap/roleinstructions/`, then `go test ./internal/configtest ./cmd/agent`.

To retire a shipped role, add its name to `agentroles.RetiredBootstrapNames`. Bootstrap defaults are Helm hooks, which Helm never deletes once they leave the chart. On startup the manager deletes chart-seeded copies of retired roles. It skips any role without the `platform.gratefulagents.dev/bootstrap-default` annotation, so roles users created themselves are untouched.
