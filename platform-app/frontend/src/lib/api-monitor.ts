export type ApiCallState = "pending" | "success" | "error";

export interface ApiCallRecord {
  id: number;
  method: string;
  url: string;
  path: string;
  startedAt: number;
  durationMs: number | null;
  status: number | null;
  state: ApiCallState;
  error: string | null;
}

type ApiCallListener = () => void;

const listeners = new Set<ApiCallListener>();
let records: ApiCallRecord[] = [];
let nextId = 1;

function emitChange() {
  for (const listener of listeners) listener();
}

function normalizeUrl(input: RequestInfo | URL): { url: string; path: string } {
  const raw =
    typeof input === "string"
      ? input
      : input instanceof URL
        ? input.toString()
        : input.url;

  try {
    const parsed = new URL(raw, window.location.origin);
    return {
      url: parsed.toString(),
      path: `${parsed.pathname}${parsed.search}`,
    };
  } catch {
    return { url: raw, path: raw };
  }
}

function normalizeMethod(input: RequestInfo | URL, init?: RequestInit): string {
  if (init?.method) return init.method.toUpperCase();
  if (typeof input === "object" && "method" in input && input.method) {
    return input.method.toUpperCase();
  }
  return "GET";
}

export function subscribeApiCalls(listener: ApiCallListener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function getApiCallsSnapshot(): ApiCallRecord[] {
  return records;
}

export function clearApiCalls(): void {
  records = [];
  emitChange();
}

export async function monitoredFetch(
  fetcher: typeof globalThis.fetch,
  input: RequestInfo | URL,
  init?: RequestInit,
): Promise<Response> {
  const startedAt = Date.now();
  const started = performance.now();
  const { url, path } = normalizeUrl(input);
  const record: ApiCallRecord = {
    id: nextId++,
    method: normalizeMethod(input, init),
    url,
    path,
    startedAt,
    durationMs: null,
    status: null,
    state: "pending",
    error: null,
  };

  records = [record, ...records].slice(0, 100);
  emitChange();

  try {
    const response = await fetcher(input, init);
    record.durationMs = performance.now() - started;
    record.status = response.status;
    record.state = response.ok ? "success" : "error";
    records = [...records];
    emitChange();
    return response;
  } catch (err) {
    record.durationMs = performance.now() - started;
    record.state = "error";
    record.error = err instanceof Error ? err.message : "Request failed";
    records = [...records];
    emitChange();
    throw err;
  }
}
