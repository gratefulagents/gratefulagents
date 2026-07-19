import { describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import type { UnaryRequest, UnaryResponse } from "@connectrpc/connect";
import { createAuthInterceptor, type RefreshOutcome } from "./auth-interceptor";

const ok = { message: "ok" } as unknown as UnaryResponse;

function makeReq(opts?: { stream?: boolean }) {
  return {
    stream: opts?.stream ?? false,
    method: { name: "ListProjects" },
    signal: new AbortController().signal,
    header: new Headers(),
  } as unknown as UnaryRequest;
}

function setup(opts: {
  outcome: RefreshOutcome;
  tokens?: (string | null)[];
}) {
  const tokens = opts.tokens ?? ["old-token", "new-token"];
  let call = 0;
  const getAccessToken = vi.fn(() => tokens[Math.min(call++, tokens.length - 1)] ?? null);
  const refresh = vi.fn(async () => opts.outcome);
  const onUnauthorized = vi.fn();
  const interceptor = createAuthInterceptor(getAccessToken, refresh, onUnauthorized);
  return { interceptor, refresh, onUnauthorized };
}

describe("createAuthInterceptor", () => {
  it("retries a unary call once after a successful token refresh", async () => {
    const { interceptor, refresh, onUnauthorized } = setup({
      outcome: { token: "new-token", sessionInvalid: false },
    });
    const next = vi
      .fn<(req: UnaryRequest) => Promise<UnaryResponse>>()
      .mockRejectedValueOnce(new ConnectError("expired", Code.Unauthenticated))
      .mockResolvedValueOnce(ok);

    const req = makeReq();
    await expect(interceptor(next as never)(req as never)).resolves.toBe(ok);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(next).toHaveBeenCalledTimes(2);
    expect(req.header.get("Authorization")).toBe("Bearer new-token");
    expect(onUnauthorized).not.toHaveBeenCalled();
  });

  it("does not re-send streams (one-shot request body); refreshes then rethrows", async () => {
    const { interceptor, refresh, onUnauthorized } = setup({
      outcome: { token: "new-token", sessionInvalid: false },
    });
    const next = vi
      .fn<(req: UnaryRequest) => Promise<UnaryResponse>>()
      .mockRejectedValue(new ConnectError("expired", Code.Unauthenticated));

    await expect(
      interceptor(next as never)(makeReq({ stream: true }) as never),
    ).rejects.toMatchObject({ code: Code.Unauthenticated });
    // The stream is NOT re-driven through the consumed iterable…
    expect(next).toHaveBeenCalledTimes(1);
    // …but the token was refreshed so the caller's reconnect succeeds.
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(onUnauthorized).not.toHaveBeenCalled();
  });

  it("keeps the session on transient refresh failure (no sign-out)", async () => {
    const { interceptor, onUnauthorized } = setup({
      outcome: { token: null, sessionInvalid: false },
    });
    const next = vi
      .fn<(req: UnaryRequest) => Promise<UnaryResponse>>()
      .mockRejectedValue(new ConnectError("expired", Code.Unauthenticated));

    await expect(interceptor(next as never)(makeReq() as never)).rejects.toMatchObject({
      code: Code.Unauthenticated,
    });
    expect(onUnauthorized).not.toHaveBeenCalled();
    expect(next).toHaveBeenCalledTimes(1);
  });

  it("signs out only when the refresh token is definitively rejected", async () => {
    const { interceptor, onUnauthorized } = setup({
      outcome: { token: null, sessionInvalid: true },
    });
    const next = vi
      .fn<(req: UnaryRequest) => Promise<UnaryResponse>>()
      .mockRejectedValue(new ConnectError("expired", Code.Unauthenticated));

    await expect(interceptor(next as never)(makeReq() as never)).rejects.toMatchObject({
      code: Code.Unauthenticated,
    });
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });

  it("leaves non-auth errors alone", async () => {
    const { interceptor, refresh } = setup({
      outcome: { token: "new-token", sessionInvalid: false },
    });
    const next = vi
      .fn<(req: UnaryRequest) => Promise<UnaryResponse>>()
      .mockRejectedValue(new ConnectError("HTTP 424", Code.Unknown));

    await expect(interceptor(next as never)(makeReq() as never)).rejects.toMatchObject({
      code: Code.Unknown,
    });
    expect(refresh).not.toHaveBeenCalled();
    expect(next).toHaveBeenCalledTimes(1);
  });
});
