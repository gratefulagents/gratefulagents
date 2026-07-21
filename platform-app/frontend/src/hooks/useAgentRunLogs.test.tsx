import { act, renderHook } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";

import { GetAgentRunLogsResponseSchema, type GetAgentRunLogsResponse } from "@/rpc/platform/service_pb";

const clientMock = vi.hoisted(() => ({
  getAgentRunLogs: vi.fn(),
}));

vi.mock("@/lib/client", () => ({ client: clientMock }));

import { useAgentRunLogs } from "./useAgentRunLogs";

afterEach(() => {
  vi.useRealTimers();
  clientMock.getAgentRunLogs.mockReset();
});

describe("useAgentRunLogs", () => {
  it("waits for a poll to finish before scheduling the next request and aborts on cleanup", async () => {
    vi.useFakeTimers();
    let resolveFirst: ((response: GetAgentRunLogsResponse) => void) | undefined;
    clientMock.getAgentRunLogs
      .mockImplementationOnce((_request, options: { signal: AbortSignal }) =>
        new Promise<GetAgentRunLogsResponse>((resolve, reject) => {
          resolveFirst = resolve;
          options.signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")));
        }),
      )
      .mockImplementation((_request, options: { signal: AbortSignal }) =>
        new Promise<GetAgentRunLogsResponse>((_resolve, reject) => {
          options.signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")));
        }),
      );

    const { result, unmount } = renderHook(() => useAgentRunLogs("demo", "run", "Running", { enabled: true }));
    expect(clientMock.getAgentRunLogs).toHaveBeenCalledTimes(1);

    await act(async () => {
      vi.advanceTimersByTime(10_000);
    });
    expect(clientMock.getAgentRunLogs).toHaveBeenCalledTimes(1);

    await act(async () => {
      resolveFirst?.(create(GetAgentRunLogsResponseSchema, {
        content: "ready\n",
        available: true,
        isComplete: false,
      }));
      await Promise.resolve();
    });
    expect(result.current.content).toBe("ready\n");

    await act(async () => {
      vi.advanceTimersByTime(4_999);
    });
    expect(clientMock.getAgentRunLogs).toHaveBeenCalledTimes(1);
    await act(async () => {
      vi.advanceTimersByTime(1);
    });
    expect(clientMock.getAgentRunLogs).toHaveBeenCalledTimes(2);

    const secondSignal = clientMock.getAgentRunLogs.mock.calls[1][1].signal as AbortSignal;
    expect(secondSignal.aborted).toBe(false);
    unmount();
    expect(secondSignal.aborted).toBe(true);
  });
});
