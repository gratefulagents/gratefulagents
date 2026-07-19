import { describe, expect, it } from "vitest";

import { applyDeltaEntries } from "./useActivityLog";
import type { ActivityEntry } from "@/rpc/platform/service_pb";

function entry(overrides: Partial<ActivityEntry>): ActivityEntry {
  return {
    eventId: 1n,
    timestampUnix: 1n,
    type: "text",
    toolUseId: "",
    message: "",
    output: "",
    ...overrides,
  } as ActivityEntry;
}

describe("applyDeltaEntries", () => {
  it("replaces a grown assistant_thinking entry in place without duplicating", () => {
    const thinking = entry({ eventId: 2n, type: "assistant_thinking", toolUseId: "think-1", message: "par" });
    const tool = entry({ eventId: 3n, timestampUnix: 2n, type: "tool_use", toolUseId: "tool-1" });
    const existing = [entry({ eventId: 1n }), thinking, tool];
    const grown = entry({
      eventId: 4n,
      type: "assistant_thinking",
      toolUseId: "think-1",
      message: "partial plus more",
    });
    const result = applyDeltaEntries(existing, [grown], 3n);
    expect(result.entries).toHaveLength(3);
    expect(result.entries[1]).toBe(grown);
    expect(result.entries[1]).not.toBe(thinking);
    expect(result.entries[0]).toBe(existing[0]);
    expect(result.entries[2]).toBe(tool);
    expect(result.lastEventId).toBe(4n);
  });

  it("appends a normal new entry", () => {
    const existing = [entry({ eventId: 1n })];
    const incoming = entry({ eventId: 2n, timestampUnix: 2n, message: "new" });
    const result = applyDeltaEntries(existing, [incoming], 1n);
    expect(result.entries).toHaveLength(2);
    expect(result.entries[0]).toBe(existing[0]);
    expect(result.entries[1]).toBe(incoming);
    expect(result.lastEventId).toBe(2n);
  });

  it("does not upsert an entry with the same toolUseId but a different type", () => {
    const thinking = entry({ eventId: 1n, type: "assistant_thinking", toolUseId: "shared", message: "thought" });
    const existing = [thinking];
    const attempt = entry({ eventId: 2n, type: "llm_attempt", toolUseId: "shared" });
    const result = applyDeltaEntries(existing, [attempt], 1n);
    expect(result.entries).toHaveLength(2);
    expect(result.entries[0]).toBe(thinking);
    expect(result.entries[1]).toBe(attempt);
  });

  it("does not upsert assistant_thinking entries with an empty toolUseId", () => {
    const existing = [entry({ eventId: 1n, type: "assistant_thinking", toolUseId: "", message: "a" })];
    const incoming = entry({ eventId: 2n, type: "assistant_thinking", toolUseId: "", message: "b" });
    const result = applyDeltaEntries(existing, [incoming], 1n);
    expect(result.entries).toHaveLength(2);
  });

  it("drops entries at or below the cursor and returns existing unchanged", () => {
    const existing = [entry({ eventId: 2n })];
    const result = applyDeltaEntries(existing, [entry({ eventId: 2n }), entry({ eventId: 1n })], 2n);
    expect(result.entries).toBe(existing);
    expect(result.lastEventId).toBe(2n);
  });
});
