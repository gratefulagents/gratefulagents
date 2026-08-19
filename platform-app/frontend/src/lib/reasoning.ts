// Reasoning effort levels accepted by run- and project-level configuration
// (rpc reasoning_level fields). The empty string means "provider/model default".
// Mirrors the ModeReasoningLevel CRD enum and the SDK's reasoning ladder
// (Codex-CLI-style efforts on OpenAI, thinking budgets on Anthropic).
export const REASONING_LEVELS = ["", "none", "minimal", "low", "medium", "high", "xhigh", "max"] as const;
