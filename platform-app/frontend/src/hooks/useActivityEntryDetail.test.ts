import { beforeEach, describe, expect, it, vi } from "vitest";

const refStore: unknown[] = [];
let refIndex = 0;

vi.mock("react", async () => {
  const actual = await vi.importActual<typeof import("react")>("react");
  return {
    ...actual,
    useRef<T>(initial: T) {
      const index = refIndex++;
      if (refStore.length <= index) {
        refStore[index] = { current: initial };
      }
      return refStore[index] as { current: T };
    },
    useCallback<T>(callback: T) {
      return callback;
    },
  };
});

const clientMock = vi.hoisted(() => ({
  getActivityEntryDetail: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: clientMock,
}));

import { useActivityEntryDetail } from "./useActivityEntryDetail";
import type { ActivityEntry } from "@/rpc/platform/service_pb";

function entry(overrides: Partial<ActivityEntry>): ActivityEntry {
  return { eventId: 0n, toolUseId: "", type: "tool_use", ...overrides } as ActivityEntry;
}

describe("useActivityEntryDetail", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    refStore.length = 0;
    refIndex = 0;
  });

  function useRendered(namespace = "ns", name = "run") {
    refIndex = 0;
    return useActivityEntryDetail(namespace, name);
  }

  it("fetches the full payload and caches it by event id", async () => {
    clientMock.getActivityEntryDetail.mockResolvedValue({ inputRaw: "full-in", output: "full-out" });
    const fetchDetail = useRendered();
    const e = entry({ eventId: 7n, toolUseId: "tu-1" });

    await expect(fetchDetail(e)).resolves.toEqual({ inputRaw: "full-in", output: "full-out" });
    await expect(fetchDetail(e)).resolves.toEqual({ inputRaw: "full-in", output: "full-out" });

    expect(clientMock.getActivityEntryDetail).toHaveBeenCalledTimes(1);
    expect(clientMock.getActivityEntryDetail).toHaveBeenCalledWith({
      namespace: "ns",
      name: "run",
      eventId: 7n,
      toolUseId: "tu-1",
    });
  });

  it("falls back to tool-use id keys when the entry has no event id", async () => {
    clientMock.getActivityEntryDetail.mockResolvedValue({ inputRaw: "in", output: "out" });
    const fetchDetail = useRendered();

    await fetchDetail(entry({ eventId: 0n, toolUseId: "tu-1", type: "tool_use" }));
    await fetchDetail(entry({ eventId: 0n, toolUseId: "tu-1", type: "tool_use" }));
    await fetchDetail(entry({ eventId: 0n, toolUseId: "tu-1", type: "tool_result" }));

    // Same tool_use is cached; the result entry for the same tool-use id is a
    // distinct payload and fetched separately.
    expect(clientMock.getActivityEntryDetail).toHaveBeenCalledTimes(2);
  });

  it("evicts failed fetches so they can be retried", async () => {
    clientMock.getActivityEntryDetail
      .mockRejectedValueOnce(new Error("boom"))
      .mockResolvedValueOnce({ inputRaw: "in", output: "out" });
    const fetchDetail = useRendered();
    const e = entry({ eventId: 3n });

    await expect(fetchDetail(e)).rejects.toThrow("boom");
    await Promise.resolve();
    await expect(fetchDetail(e)).resolves.toEqual({ inputRaw: "in", output: "out" });
    expect(clientMock.getActivityEntryDetail).toHaveBeenCalledTimes(2);
  });

  it("scopes the cache to the run identity", async () => {
    clientMock.getActivityEntryDetail.mockResolvedValue({ inputRaw: "in", output: "out" });
    const e = entry({ eventId: 5n });

    await useRendered("ns", "run-a")(e);
    await useRendered("ns", "run-b")(e);

    expect(clientMock.getActivityEntryDetail).toHaveBeenCalledTimes(2);
    expect(clientMock.getActivityEntryDetail).toHaveBeenLastCalledWith(
      expect.objectContaining({ name: "run-b" }),
    );
  });
});
