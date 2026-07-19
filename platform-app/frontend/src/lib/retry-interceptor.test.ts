import { describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import type { UnaryRequest, UnaryResponse } from "@connectrpc/connect";
import { createRetryInterceptor, isRetryableReadMethod } from "./retry-interceptor";

function makeReq(methodName: string, opts?: { stream?: boolean; signal?: AbortSignal }) {
  return {
    stream: opts?.stream ?? false,
    method: { name: methodName },
    signal: opts?.signal ?? new AbortController().signal,
    header: new Headers(),
  } as unknown as UnaryRequest;
}

const ok = { message: "ok" } as unknown as UnaryResponse;

function run(
  next: (req: UnaryRequest) => Promise<UnaryResponse>,
  req: UnaryRequest,
  options?: Parameters<typeof createRetryInterceptor>[0],
) {
  const interceptor = createRetryInterceptor({ baseDelayMs: 0, maxDelayMs: 0, ...options });
  return interceptor(next as never)(req as never);
}

describe("isRetryableReadMethod", () => {
  it("matches read verbs and rejects mutations", () => {
    for (const name of ["ListProjects", "GetProject", "WatchAgentRuns", "ExportAgentRun", "ReadFile"]) {
      expect(isRetryableReadMethod(name), name).toBe(true);
    }
    for (const name of ["CreateProject", "UpdateProject", "DeleteProject", "SendChatMessage", "RetryAgentRun", "InterruptAgentRun"]) {
      expect(isRetryableReadMethod(name), name).toBe(false);
    }
  });
});

describe("createRetryInterceptor", () => {
  it("retries transient failures on unary reads until success", async () => {
    const next = vi
      .fn<(req: UnaryRequest) => Promise<UnaryResponse>>()
      .mockRejectedValueOnce(new ConnectError("HTTP 424", Code.Unknown))
      .mockRejectedValueOnce(new SyntaxError("Unexpected end of JSON input"))
      .mockResolvedValueOnce(ok);

    await expect(run(next, makeReq("ListProjects"))).resolves.toBe(ok);
    expect(next).toHaveBeenCalledTimes(3);
  });

  it("gives up after maxAttempts and throws the last error", async () => {
    const next = vi
      .fn<(req: UnaryRequest) => Promise<UnaryResponse>>()
      .mockRejectedValue(new ConnectError("HTTP 503", Code.Unavailable));

    await expect(run(next, makeReq("ListProjects"), { maxAttempts: 3 })).rejects.toMatchObject({
      code: Code.Unavailable,
    });
    expect(next).toHaveBeenCalledTimes(3);
  });

  it("never retries mutations", async () => {
    const next = vi
      .fn<(req: UnaryRequest) => Promise<UnaryResponse>>()
      .mockRejectedValue(new ConnectError("HTTP 424", Code.Unknown));

    await expect(run(next, makeReq("CreateProject"))).rejects.toBeInstanceOf(ConnectError);
    expect(next).toHaveBeenCalledTimes(1);
  });

  it("never retries streams", async () => {
    const next = vi
      .fn<(req: UnaryRequest) => Promise<UnaryResponse>>()
      .mockRejectedValue(new ConnectError("HTTP 424", Code.Unknown));

    await expect(run(next, makeReq("WatchProjects", { stream: true }))).rejects.toBeInstanceOf(
      ConnectError,
    );
    expect(next).toHaveBeenCalledTimes(1);
  });

  it("does not retry definitive server answers", async () => {
    const next = vi
      .fn<(req: UnaryRequest) => Promise<UnaryResponse>>()
      .mockRejectedValue(new ConnectError("no access", Code.PermissionDenied));

    await expect(run(next, makeReq("ListProjects"))).rejects.toMatchObject({
      code: Code.PermissionDenied,
    });
    expect(next).toHaveBeenCalledTimes(1);
  });

  it("does not retry unauthenticated errors (auth interceptor owns those)", async () => {
    const next = vi
      .fn<(req: UnaryRequest) => Promise<UnaryResponse>>()
      .mockRejectedValue(new ConnectError("expired", Code.Unauthenticated));

    await expect(run(next, makeReq("ListProjects"))).rejects.toMatchObject({
      code: Code.Unauthenticated,
    });
    expect(next).toHaveBeenCalledTimes(1);
  });

  it("stops retrying once the caller aborts", async () => {
    const controller = new AbortController();
    const next = vi.fn<(req: UnaryRequest) => Promise<UnaryResponse>>().mockImplementation(() => {
      controller.abort();
      return Promise.reject(new ConnectError("HTTP 503", Code.Unavailable));
    });

    await expect(
      run(next, makeReq("ListProjects", { signal: controller.signal })),
    ).rejects.toBeInstanceOf(ConnectError);
    expect(next).toHaveBeenCalledTimes(1);
  });
});
