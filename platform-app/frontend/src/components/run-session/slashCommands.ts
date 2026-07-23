import type { AgentRun, ModeTemplate } from "@/rpc/platform/service_pb";

// SlashCommandKind selects which RPC executes the command. All commands are
// mode switches — plan is a regular ModeTemplate with dedicated /plan and
// /chat triggers.
export type SlashCommandAction = { kind: "mode"; target: string };

export interface SlashCommand {
  id: string;
  /** Canonical text including the leading slash, e.g. "/plan" or "/mode deep". */
  trigger: string;
  title: string;
  description: string;
  keywords?: string[];
  action: SlashCommandAction;
}

// Mode templates already exposed through dedicated commands (or retained only
// for compatibility) are skipped in the generic mode list.
const DEDICATED_MODES = new Set(["chat", "autopilot", "plan"]);

// buildSlashCommands derives the available commands from the live run state so
// the palette only offers transitions that make sense right now.
export function buildSlashCommands(
  run: Pick<AgentRun, "modeName">,
  availableModes: ModeTemplate[],
): SlashCommand[] {
  const commands: SlashCommand[] = [];
  const inPlan = run.modeName === "plan";

  if (inPlan) {
    commands.push({
      id: "exit-plan",
      trigger: "/chat",
      title: "Exit plan mode",
      description: "Leave plan mode and switch to autonomous execution.",
      keywords: ["build", "exit-plan", "approve", "code"],
      action: { kind: "mode", target: "autopilot" },
    });
  } else {
    commands.push({
      id: "plan",
      trigger: "/plan",
      title: "Plan mode",
      description: "Plan first, then implement here after approval.",
      keywords: ["planning", "design", "approve"],
      action: { kind: "mode", target: "plan" },
    });
  }

  for (const mode of availableModes) {
    const key = mode.k8sName || mode.name;
    if (!key || key === run.modeName || DEDICATED_MODES.has(key)) {
      continue;
    }
    commands.push({
      id: `mode:${key}`,
      trigger: `/mode ${key}`,
      title: key,
      description: mode.description || mode.category || "Switch mode.",
      keywords: ["mode", mode.category].filter((k): k is string => Boolean(k)),
      action: { kind: "mode", target: key },
    });
  }

  return commands;
}

// filterSlashCommands narrows the registry to entries matching the composer
// input. It returns an empty list when the input is not a slash command.
export function filterSlashCommands(commands: SlashCommand[], input: string): SlashCommand[] {
  const query = input.trimStart().toLowerCase();
  if (!query.startsWith("/")) {
    return [];
  }
  const body = query.slice(1).trim();
  if (body === "") {
    return commands;
  }
  const tokens = body.split(/\s+/).filter(Boolean);
  return commands.filter((command) => {
    const trigger = command.trigger.toLowerCase();
    if (trigger.startsWith(query)) {
      return true;
    }
    const haystack = [command.trigger, command.title, ...(command.keywords ?? [])]
      .join(" ")
      .toLowerCase();
    return tokens.every((token) => haystack.includes(token));
  });
}
