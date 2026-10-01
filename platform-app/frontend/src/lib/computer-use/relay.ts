import { client } from "@/lib/client";
import type { ActionName, ComputerAction, Screenshot } from "./native";

export type RelayOperation = "connect" | "next" | "result" | "disconnect";

export interface RunRef {
  namespace: string;
  name: string;
}

export interface RelayRequest {
  id: string;
  action: ComputerAction;
}

export interface RelayResponse {
  protocol: 2;
  active: boolean;
  available: boolean;
  reason: string;
  request?: RelayRequest;
}

export interface RelayResult {
  requestId: string;
  ok: boolean;
  error?: string;
  denied?: boolean;
  screenshot?: Screenshot;
  cursor?: { x: number; y: number };
}

/** `next` long-polls for up to 20 s on the broker; everything else is quick. */
export const RELAY_TIMEOUTS: Record<RelayOperation, number> = { connect: 20_000, next: 40_000, result: 20_000, disconnect: 20_000 };

/** The relay answered with something this app cannot speak; retrying will not help. */
export class RelayProtocolError extends Error {}

const MAX_ERROR = 1024;
const MAX_RESPONSE = 64 * 1024;

const REQUIRED: Record<Exclude<ActionName, "wait">, (keyof ComputerAction)[]> = {
  screenshot: [],
  left_click: ["coordinate"],
  right_click: ["coordinate"],
  middle_click: ["coordinate"],
  double_click: ["coordinate"],
  triple_click: ["coordinate"],
  mouse_move: ["coordinate"],
  left_click_drag: ["start_coordinate", "coordinate"],
  left_mouse_down: [],
  left_mouse_up: [],
  scroll: ["scroll_direction"],
  type: ["text"],
  key: ["text"],
  cursor_position: [],
  zoom: ["region"],
  open_url: ["url"],
};

const FIELDS = new Set<string>([
  "action", "coordinate", "start_coordinate", "text", "scroll_direction", "scroll_amount", "repeat", "duration", "region", "url",
]);

const isCount = (value: unknown) => Number.isInteger(value) && (value as number) >= 0;
const isPoint = (value: unknown) => Array.isArray(value) && value.length === 2 && value.every(isCount);

function validAction(value: unknown): value is ComputerAction {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const action = value as Record<string, unknown>;
  if (typeof action.action !== "string" || !Object.hasOwn(REQUIRED, action.action)) return false;
  if (Object.keys(action).some((key) => !FIELDS.has(key))) return false;
  if (REQUIRED[action.action as keyof typeof REQUIRED].some((key) => action[key] === undefined)) return false;
  if (action.coordinate !== undefined && !isPoint(action.coordinate)) return false;
  if (action.start_coordinate !== undefined && !isPoint(action.start_coordinate)) return false;
  if (action.text !== undefined && (typeof action.text !== "string" || !action.text.length || action.text.length > 4000)) return false;
  if (action.url !== undefined && (typeof action.url !== "string" || action.url.length > 2048)) return false;
  if (action.scroll_direction !== undefined && !["up", "down", "left", "right"].includes(action.scroll_direction as string)) return false;
  if (action.scroll_amount !== undefined && !isCount(action.scroll_amount)) return false;
  if (action.repeat !== undefined && !isCount(action.repeat)) return false;
  if (action.region !== undefined) {
    const region = action.region;
    if (!Array.isArray(region) || region.length !== 4 || !region.every(isCount) || region[2] <= region[0] || region[3] <= region[1]) return false;
  }
  return true;
}

/** Parses and validates a broker Response (COMPUTER_USE.md §2). */
export function parseRelayResponse(raw: string): RelayResponse {
  if (raw.length > MAX_RESPONSE) throw new RelayProtocolError("Computer use relay response is too large");
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    throw new RelayProtocolError("Computer use relay response is not valid JSON");
  }
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new RelayProtocolError("Computer use relay response is malformed");
  const body = value as Record<string, unknown>;
  if (body.protocol !== 2) {
    throw new RelayProtocolError("This run uses a different computer use protocol. Update the desktop app and recreate the run.");
  }
  if (typeof body.active !== "boolean" || typeof body.available !== "boolean") {
    throw new RelayProtocolError("Computer use relay response is malformed");
  }
  if (body.reason !== undefined && typeof body.reason !== "string") throw new RelayProtocolError("Computer use relay response is malformed");
  const response: RelayResponse = {
    protocol: 2, active: body.active, available: body.available, reason: (body.reason as string | undefined) ?? "",
  };
  if (body.request !== undefined && body.request !== null) {
    const request = body.request as Record<string, unknown>;
    if (typeof request !== "object" || typeof request.id !== "string" || !request.id || request.id.length > 128 || !validAction(request.action)) {
      throw new RelayProtocolError("Computer use relay sent an invalid request");
    }
    response.request = { id: request.id, action: request.action };
  }
  return response;
}

/** The broker decodes results strictly: only known keys, integer coordinates, no undefined fields. */
function wireResult(result: RelayResult): RelayResult {
  const wire: RelayResult = { requestId: result.requestId, ok: result.ok };
  if (!result.ok) wire.error = (result.error || "Action failed").slice(0, MAX_ERROR);
  if (result.denied) wire.denied = true;
  if (result.screenshot) {
    const { mediaType, data, width, height } = result.screenshot;
    wire.screenshot = { mediaType, data, width, height };
  }
  if (result.cursor) wire.cursor = { x: Math.round(result.cursor.x), y: Math.round(result.cursor.y) };
  return wire;
}

/** One ExchangeComputerUse round trip. */
export async function exchange(
  run: RunRef,
  sessionId: string,
  operation: RelayOperation,
  options: { result?: RelayResult; signal?: AbortSignal } = {},
): Promise<RelayResponse> {
  const { result, signal } = options;
  const outcome = result && wireResult(result);
  const response = await client.exchangeComputerUse({
    namespace: run.namespace,
    name: run.name,
    sessionId,
    operation,
    requestId: result?.requestId ?? "",
    outcomeJson: outcome ? JSON.stringify(outcome) : "",
  }, { timeoutMs: RELAY_TIMEOUTS[operation], signal });
  return parseRelayResponse(response.responseJson);
}
