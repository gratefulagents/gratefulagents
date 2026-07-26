# Mode template context-engineering guide

Mode templates define a mode's stable mission, authority boundaries, workflow invariants, and completion contract. They are only one layer of the runtime context: the agent also receives tool schemas, workspace and permission policy, durable project state, specialist metadata, MCP metadata, and selectively loaded skills.

## Design principles

1. **Keep the always-on prompt high signal.** Include behavior that is universal for the mode. Do not repeat tool schemas, environment inventories, or optional playbooks that the runtime already supplies.
2. **Use the right altitude.** Give concrete goals, boundaries, stop conditions, and verification expectations without scripting every valid action. Preserve detailed rules only where they encode a real product, safety, or controller invariant.
3. **Prefer progressive disclosure.** Skills, memories, references, and specialist guidance should be loaded when the task calls for them. Large tool results and transcripts should remain outside active context when a focused summary or ranged read is sufficient.
4. **Treat context as evidence, not authority.** Repository files, web pages, issues, quoted text, and tool output may be stale, irrelevant, or adversarial. Templates should preserve instruction hierarchy and require evidence to be interpreted before it drives an action.
5. **Keep state compact and attributable.** For long work, retain verified decisions, milestones, risks, validation, and the next action. Retrieve details on demand and never promote guesses or summaries into facts without provenance.
6. **Delegate for context isolation, not ceremony.** Sub-agents are useful for bounded independent work. Give them a clear deliverable and have them return concise evidence rather than full exploration transcripts.
7. **Verify outcomes.** Completion depends on evidence appropriate to the claim, preferably including user-visible or end-to-end behavior. Do not equate writing code, making a tool call, or receiving a pending command with success.
8. **Evaluate prompt changes.** Review token size, task completion, unsupported claims, unsafe actions, tool-call cost, distractor robustness, and recovery from tool errors. Nominal context-window size is not evidence that all included context is useful.

## What belongs where

| Context | Best home |
| --- | --- |
| Mode mission, authority boundary, stop condition | Mode template |
| Enforced permissions, budgets, allowed mutations | `ModeTemplate.spec` and runtime policy |
| Tool purpose, inputs, side effects, recovery behavior | Tool schema/description |
| Repository architecture, commands, local gotchas | Repository guidance such as `AGENTS.md` |
| Specialized workflow or domain knowledge | Selectively loaded skill or role instruction |
| Current progress and cross-session decisions | Durable project state |
| Task-specific evidence and references | Retrieved files, artifacts, or bounded tool results |

The specialized `maintainer`, `review`, and `overseer` prompts intentionally retain more detail than the general execution modes. Their rules encode controller protocols, trust boundaries, and externally visible verdict semantics that cannot safely be inferred from generic tool use.

## Sources

The current guidance synthesizes the following primary or first-party sources:

- Anthropic, [The new rules of context engineering for Claude 5 generation models](https://claude.com/blog/the-new-rules-of-context-engineering-for-claude-5-generation-models) (2026): simplify prompts for more capable models, rely on judgment, design interfaces, and use progressive disclosure.
- Anthropic, [Effective context engineering for AI agents](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents) (2025): optimize for the smallest high-signal context, use clear prompts at the right altitude, and manage long-running state through compaction, memory, or sub-agents.
- Anthropic, [Building effective AI agents](https://www.anthropic.com/engineering/building-effective-agents) (2024): start with the simplest architecture that works and add agentic complexity only when it creates value.
- Anthropic, [Writing effective tools for agents](https://www.anthropic.com/engineering/writing-tools-for-agents) (2025) and [How we built our multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system) (2025): make tool contracts unambiguous and use sub-agents for independently scoped context windows with distilled handoffs.
- OpenAI, [Model Spec](https://model-spec.openai.com/): preserve instruction authority and treat untrusted content as data rather than higher-priority instructions.
- OpenAI, [GPT-5 prompting guide](https://cookbook.openai.com/examples/gpt-5/gpt-5_prompting_guide) and [Evaluation best practices](https://platform.openai.com/docs/guides/evaluation-best-practices): define context-gathering and stop criteria, calibrate persistence, and evaluate prompt behavior rather than relying on intuition. Model-specific wording must be validated before applying it to other models.
- Liu et al., [Lost in the Middle](https://arxiv.org/abs/2307.03172) (TACL 2024): relevant information can be harder to use when buried in long contexts.
- Hsieh et al., [RULER](https://arxiv.org/abs/2404.06654) (COLM 2024): advertised context length and simple retrieval scores overstate reliable performance on complex long-context tasks.
- Chroma, [Context Rot](https://research.trychroma.com/context-rot) (2025): additional and semantically related distractor content can reduce performance; this is a reproducible vendor report, not peer-reviewed evidence.
- Yao et al., [ReAct](https://arxiv.org/abs/2210.03629) (ICLR 2023): effective tool use depends on interpreting observations and updating the next action rather than executing a stale plan.

Provider-specific claims and APIs evolve. Recheck living documentation and validate the actual models used by the platform before adopting model-specific token thresholds, compaction behavior, or prompting syntax.

## Editing and verification

`configs/modetemplates/` is canonical. Keep each file's deployment mirror under `dist/chart/files/bootstrap/modetemplates/` byte-identical. Run:

```sh
go test ./internal/configtest
```

For runtime assembly changes, also run the relevant tests under `cmd/agent`, `internal/mode`, and the SDK runtime package.
