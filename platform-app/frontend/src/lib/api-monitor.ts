export type ApiCallState = "pending" | "success" | "error";

export interface ApiCallRecord {
  id: number;
  method: string;
  url: string;
  path: string;
  startedAt: number;
  durationMs: number | null;
  status: number | null;
  statusText: string | null;
  state: ApiCallState;
  error: string | null;
  requestHeaders: [string, string][];
  requestBody: string | null;
  responseHeaders: [string, string][];
  responseBody: string | null;
}

type ApiCallListener = () => void;

const MAX_RECORDS = 100;
const MAX_BODY_CHARS = 20_000;

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

function headerEntries(headers: HeadersInit | Headers | undefined): [string, string][] {
  if (!headers) return [];
  try {
    return [...new Headers(headers as HeadersInit).entries()];
  } catch {
    return [];
  }
}

function captureRequestHeaders(
  input: RequestInfo | URL,
  init?: RequestInit,
): [string, string][] {
  if (init?.headers) return headerEntries(init.headers);
  if (typeof input === "object" && "headers" in input) {
    return headerEntries(input.headers);
  }
  return [];
}

function truncate(text: string): string {
  if (text.length <= MAX_BODY_CHARS) return text;
  return `${text.slice(0, MAX_BODY_CHARS)}\n… [truncated, ${text.length} chars total]`;
}

function captureRequestBody(init?: RequestInit): string | null {
  const body = init?.body;
  if (body == null) return null;
  if (typeof body === "string") return truncate(body);
  if (body instanceof URLSearchParams) return truncate(body.toString());
  if (body instanceof FormData) return "[FormData]";
  if (body instanceof Blob) return `[Blob ${body.size} bytes]`;
  if (body instanceof ArrayBuffer) return `[ArrayBuffer ${body.byteLength} bytes]`;
  if (ArrayBuffer.isView(body)) return `[Binary ${body.byteLength} bytes]`;
  return "[Stream]";
}

function isTextResponse(contentType: string | null): boolean {
  if (!contentType) return false;
  // Streaming protocols: reading a clone would buffer the tee'd stream for
  // the lifetime of the connection.
  if (
    contentType.includes("event-stream") ||
    contentType.includes("connect+") ||
    contentType.includes("grpc")
  ) {
    return false;
  }
  return (
    contentType.startsWith("text/") ||
    contentType.includes("json") ||
    contentType.includes("xml") ||
    contentType.includes("x-www-form-urlencoded")
  );
}

function touchRecord(record: ApiCallRecord): void {
  if (records.includes(record)) {
    records = [...records];
    emitChange();
  }
}

function captureResponseBody(record: ApiCallRecord, response: Response): void {
  const contentType = response.headers.get("content-type");
  if (response.bodyUsed || response.body == null) return;
  if (!isTextResponse(contentType)) {
    record.responseBody = contentType ? `[${contentType}]` : null;
    return;
  }
  try {
    // Read from a clone so the caller keeps the original stream. Only done
    // for finite text-like bodies (never event streams) to avoid tee buffering.
    void response
      .clone()
      .text()
      .then((text) => {
        record.responseBody = truncate(text);
        touchRecord(record);
      })
      .catch(() => {
        /* body unavailable; leave as null */
      });
  } catch {
    /* clone unsupported (e.g. already-consumed body); leave as null */
  }
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
    statusText: null,
    state: "pending",
    error: null,
    requestHeaders: captureRequestHeaders(input, init),
    requestBody: captureRequestBody(init),
    responseHeaders: [],
    responseBody: null,
  };

  records = [record, ...records].slice(0, MAX_RECORDS);
  emitChange();

  try {
    const response = await fetcher(input, init);
    record.durationMs = performance.now() - started;
    record.status = response.status;
    record.statusText = response.statusText || null;
    record.state = response.ok ? "success" : "error";
    record.responseHeaders = headerEntries(response.headers);
    captureResponseBody(record, response);
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
