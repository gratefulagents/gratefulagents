import { client } from "./client";
import { backendBaseUrl } from "./platform";
import { isWebUrl, parseHotkey, type DesktopOutcome, type DesktopRequest, type DesktopScope } from "./computer-use";

export interface DesktopRelayStatus {
  mode?: "selected_window" | "agent_choice";
  active: boolean;
  visionAvailable: boolean;
  pending?: DesktopRequest;
  reason?: string;
}

const identifier = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/;
// Proposed text must render exactly as it will be typed: control characters,
// invisible format characters (zero-width space, BOM, bidi overrides/isolates),
// line/paragraph separators, private-use and unassigned code points are
// rejected. Zero-width joiner/non-joiner (U+200C/U+200D) stay allowed for emoji
// sequences and scripts that need them; native validation is the final authority.
const hiddenText = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}\p{Co}\p{Cn}]/u;
const joiner = /[\u200c\u200d]/g;
export function hasHiddenText(text: string): boolean {
  return hiddenText.test(text.replace(joiner, ""));
}

// The claim RPC only marks a request as taken; resolve carries the outcome,
// including a PNG capture for observations, so it may take longer.
const relayTimeouts = { attach_agent: 4000, attach: 4000, poll: 4000, stop: 4000, claim: 6000, resolve: 20_000 } as const;

// The relay is not an instruction source. Reject malformed actions before
// presenting them; native validation remains the execution authority.
export function parseDesktopRelay(raw: string): DesktopRelayStatus {
  if (raw.length > 16_384) throw new Error("Invalid desktop relay response");
  const value = JSON.parse(raw) as DesktopRelayStatus;
  if (!value || typeof value.active !== "boolean" || typeof value.visionAvailable !== "boolean" ||
      (value.reason !== undefined && (typeof value.reason !== "string" || value.reason.length > 256))) {
    throw new Error("Invalid desktop relay response");
  }
  if (value.mode !== undefined && value.mode !== "selected_window" && value.mode !== "agent_choice") throw new Error("Unsupported computer-use mode; update and reconnect");
  if (value.pending !== undefined && value.pending !== null) {
    const request = value.pending;
    if (!request || typeof request !== "object" || !value.active || typeof request.requestId !== "string" || !identifier.test(request.requestId) ||
        (request.frameId !== undefined && (typeof request.frameId !== "string" || !identifier.test(request.frameId)))) {
      throw new Error("Invalid desktop request binding");
    }
    if (request.targetRevision !== undefined && (!Number.isSafeInteger(request.targetRevision) || request.targetRevision < 0)) throw new Error("Invalid target revision");
    const action = request.action;
    if (!action || typeof action !== "object") throw new Error("Invalid desktop action");
    const allowed: Record<string, string[]> = {
      list_windows: ["kind"], select_window: ["kind", "targetRef"],
      observe: ["kind", "question"], click: ["kind", "x", "y", "button", "count"], move: ["kind", "x", "y"],
      drag: ["kind", "x", "y", "toX", "toY"], scroll: ["kind", "deltaX", "deltaY", "x", "y"],
      type: ["kind", "text"], key: ["kind", "key"], activate: ["kind"], open_url: ["kind", "url"],
    };
    const pixel = (n: unknown) => typeof n === "number" && Number.isFinite(n) && n >= 0 && n <= 100_000;
    if (typeof action.kind !== "string" || !Object.hasOwn(allowed, action.kind)) throw new Error("Unsupported desktop action");
    if ((action.kind === "list_windows" || action.kind === "select_window") && value.mode !== "agent_choice") throw new Error("Window discovery requires explicit agent-choice authorization");
    const fields = allowed[action.kind];
    if (!fields || Object.keys(action).some((field) => !fields.includes(field))) throw new Error("Unsupported desktop action");
    switch (action.kind) {
      case "select_window":
        if (typeof action.targetRef !== "string" || !/^[a-f0-9]{64}$/.test(action.targetRef)) throw new Error("Invalid target reference");
        break;
      case "observe":
        if (action.question !== undefined && (typeof action.question !== "string" || action.question.length > 2048)) throw new Error("Invalid observation request");
        break;
      case "click":
        if (![action.x, action.y].every(pixel)) throw new Error("Invalid click coordinates");
        if (action.button !== undefined && !["left", "right", "middle"].includes(action.button)) throw new Error("Invalid click button");
        if (action.count !== undefined && ![1, 2, 3].includes(action.count)) throw new Error("Invalid click count");
        break;
      case "move":
        if (![action.x, action.y].every(pixel)) throw new Error("Invalid pointer coordinates");
        break;
      case "drag":
        if (![action.x, action.y, action.toX, action.toY].every(pixel) || (action.x === action.toX && action.y === action.toY)) throw new Error("Invalid drag coordinates");
        break;
      case "scroll":
        if (![action.deltaX, action.deltaY].every((n) => Number.isInteger(n) && Math.abs(n) <= 1000) || (!action.deltaX && !action.deltaY)) throw new Error("Invalid scroll request");
        if ((action.x === undefined) !== (action.y === undefined) || (action.x !== undefined && ![action.x, action.y].every(pixel))) throw new Error("Invalid scroll position");
        break;
      case "type":
        if (typeof action.text !== "string" || !action.text.length || action.text.length > 1000 || hasHiddenText(action.text)) throw new Error("Invalid proposed text");
        break;
      case "key":
        if (!parseHotkey(action.key)) throw new Error("Unsupported key combination");
        break;
      case "open_url":
        if (!isWebUrl(action.url)) throw new Error("Only http(s) URLs can be opened");
        break;
    }
    if (action.kind !== "observe" && action.kind !== "open_url" && action.kind !== "list_windows" && action.kind !== "select_window" && !request.frameId) throw new Error("An observation frame is required");
  }
  return value;
}

export async function exchangeDesktopRelay(
  sessionId: string, scope: DesktopScope,
  operation: "attach" | "poll" | "claim" | "resolve" | "stop",
  requestId = "", outcome?: DesktopOutcome,
): Promise<DesktopRelayStatus> {
  if (scope.backend !== backendBaseUrl()) throw new Error("Desktop backend changed; grant fresh consent");
  const wireOperation = operation === "attach" && scope.mode === "agent_choice" ? "attach_agent" : operation;
  const response = await client.exchangeComputerUse({
    namespace: scope.namespace, name: scope.run, sessionId, operation: wireOperation, requestId,
    outcomeJson: outcome ? JSON.stringify(outcome) : "",
  }, { timeoutMs: relayTimeouts[operation] }).catch((error: unknown) => {
    if (wireOperation === "attach_agent") throw new Error("Agent-choice connection failed. Verify run access, update desktop/backend/run to a compatible version, and reconnect with fresh consent.");
    throw error;
  });
  if (scope.backend !== backendBaseUrl()) throw new Error("Desktop backend changed; grant fresh consent");
  const relay = parseDesktopRelay(response.responseJson);
  if (scope.mode === "agent_choice" && operation !== "stop" && relay.mode !== "agent_choice") throw new Error("Agent-choice protocol unavailable; update desktop, backend and run, then reconnect");
  return relay;
}
