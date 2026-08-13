import { afterEach, describe, expect, it, vi } from "vitest";

import {
  clearApiCalls,
  getApiCallsSnapshot,
  monitoredFetch,
} from "./api-monitor";

function jsonResponse(body: unknown, init?: ResponseInit): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "content-type": "application/json" },
    ...init,
  });
}

describe("monitoredFetch", () => {
  afterEach(() => {
    clearApiCalls();
  });

  it("redacts sensitive headers and body fields", async () => {
    const fetcher = vi.fn(async () =>
      jsonResponse(
        { accessToken: "resp-access", refreshToken: "resp-refresh", user: { name: "a" } },
        { headers: { "content-type": "application/json", "set-cookie": "sid=1" } },
      ),
    );

    await monitoredFetch(fetcher, "https://api.example.com/auth.v1.AuthService/Login", {
      method: "POST",
      headers: {
        Authorization: "Bearer secret-token",
        "CF-Access-Client-Secret": "cf-secret",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ username: "alice", password: "hunter2" }),
    });

    const record = getApiCallsSnapshot()[0];
    expect(Object.fromEntries(record.requestHeaders)).toMatchObject({
      authorization: "[redacted]",
      "cf-access-client-secret": "[redacted]",
      "content-type": "application/json",
    });
    expect(record.requestBody).toContain('"username":"alice"');
    expect(record.requestBody).toContain('"password":"[redacted]"');
    expect(record.requestBody).not.toContain("hunter2");

    await vi.waitFor(() => {
      expect(record.responseBody).not.toBeNull();
    });
    expect(record.responseBody).toContain('"accessToken":"[redacted]"');
    expect(record.responseBody).toContain('"refreshToken":"[redacted]"');
    expect(record.responseBody).not.toContain("resp-access");
    expect(Object.fromEntries(record.responseHeaders)).toMatchObject({
      "set-cookie": "[redacted]",
    });
  });

  it("keeps response body reads bounded for large responses", async () => {
    const huge = "x".repeat(100_000);
    const fetcher = vi.fn(async () =>
      new Response(huge, {
        status: 200,
        headers: { "content-type": "text/plain" },
      }),
    );

    const response = await monitoredFetch(fetcher, "https://api.example.com/big");
    // Caller still receives the full body.
    expect((await response.text()).length).toBe(100_000);

    const record = getApiCallsSnapshot()[0];
    await vi.waitFor(() => {
      expect(record.responseBody).not.toBeNull();
    });
    expect(record.responseBody!.length).toBeLessThan(21_000);
    expect(record.responseBody).toContain("truncated");
  });

  it("labels non-text responses without reading them", async () => {
    const fetcher = vi.fn(async () =>
      new Response(new Uint8Array([1, 2, 3]), {
        status: 200,
        headers: { "content-type": "application/octet-stream" },
      }),
    );

    await monitoredFetch(fetcher, "https://api.example.com/blob");
    const record = getApiCallsSnapshot()[0];
    expect(record.responseBody).toBe("[application/octet-stream]");
  });
});
