import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, renderHook, waitFor } from "@testing-library/react";

import { useAvailableModels } from "@/hooks/useAvailableModels";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    listAvailableModels: vi.fn(),
  },
}));

const listModels = vi.mocked(client.listAvailableModels);

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("useAvailableModels", () => {
  it("fetches suggestions for the namespace, provider, and auth mode", async () => {
    listModels.mockResolvedValue({ models: ["gpt-5", "gpt-5-mini"] } as never);

    const { result } = renderHook(() =>
      useAvailableModels({ namespace: "user-alice", provider: "openai", authMode: "api-key" }),
    );

    expect(result.current.loading).toBe(true);
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.models).toEqual(["gpt-5", "gpt-5-mini"]);
    expect(result.current.error).toBeNull();
    expect(listModels).toHaveBeenCalledWith(
      { namespace: "user-alice", provider: "openai", authMode: "api-key" },
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
  });

  it("refetches and clears stale suggestions when the selection changes", async () => {
    listModels.mockImplementation(({ provider }) =>
      Promise.resolve({
        models: provider === "openai" ? ["gpt-5"] : ["claude-opus-4-6"],
      } as never),
    );

    const { result, rerender } = renderHook(
      ({ provider }: { provider: string }) =>
        useAvailableModels({ namespace: "user-alice", provider, authMode: "api-key" }),
      { initialProps: { provider: "openai" } },
    );
    await waitFor(() => expect(result.current.models).toEqual(["gpt-5"]));

    rerender({ provider: "anthropic" });
    expect(result.current.models).toEqual([]);
    await waitFor(() => expect(result.current.models).toEqual(["claude-opus-4-6"]));
    expect(listModels).toHaveBeenCalledTimes(2);
  });

  it("surfaces fetch failures as an error message", async () => {
    listModels.mockRejectedValue(new Error("catalog offline"));

    const { result } = renderHook(() =>
      useAvailableModels({ namespace: "user-alice", provider: "openai", authMode: "api-key" }),
    );

    await waitFor(() => expect(result.current.error).toBe("catalog offline"));
    expect(result.current.models).toEqual([]);
    expect(result.current.loading).toBe(false);
  });

  it("stays idle while disabled or the selection is incomplete", async () => {
    const { result, rerender } = renderHook(
      ({ enabled, provider }: { enabled: boolean; provider: string }) =>
        useAvailableModels({ namespace: "user-alice", provider, authMode: "api-key", enabled }),
      { initialProps: { enabled: false, provider: "openai" } },
    );

    expect(result.current).toEqual({ models: [], loading: false, error: null });
    rerender({ enabled: true, provider: "" });
    expect(result.current).toEqual({ models: [], loading: false, error: null });
    await Promise.resolve();
    expect(listModels).not.toHaveBeenCalled();
  });
});
