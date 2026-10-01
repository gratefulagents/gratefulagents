import { describe, expect, it } from "vitest";
import { create, type MessageInitShape } from "@bufbuild/protobuf";

import {
  dedupeFinalReplies,
  messageDeliveryTimestampMs,
  messageTimelineKey,
  orderDeliveredMessages,
  partitionConversation,
  sourceHref,
  thinkingLabel,
} from "./helpers";
import { ActivityEntrySchema, ChatMessageSchema } from "@/rpc/platform/service_pb";

function msg(overrides: MessageInitShape<typeof ChatMessageSchema> = {}) {
  return create(ChatMessageSchema, { role: "user", content: "hello", ...overrides });
}

describe("thinkingLabel", () => {
  it("uses precise startup wording across live phases", () => {
    expect(thinkingLabel("Pending", "starting")).toBe("Queued to start…");
    expect(thinkingLabel("Provisioning", "cloning_repository")).toBe("Cloning repository…");
    expect(thinkingLabel("Running", "setting up workspace")).toBe("Preparing workspace…");
  });

  it("does not describe unknown active work as preparation", () => {
    expect(thinkingLabel("Running", "")).toBe("Working…");
    expect(thinkingLabel("Running", "analyzing-results")).toBe("Analyzing results…");
  });
});

describe("partitionConversation", () => {
  it("keeps delivered messages in the transcript and pulls pending ones out", () => {
    const delivered = msg({ content: "delivered", deliveredAtUnix: 100n });
    const queued = msg({ content: "queued follow-up", pending: true, queueMode: "enqueue" });
    const steering = msg({ content: "steer!", pending: true, queueMode: "immediate" });
    const assistant = msg({ role: "assistant", content: "reply" });

    const parts = partitionConversation([delivered, assistant, queued, steering]);

    expect(parts.delivered).toEqual([delivered, assistant]);
    expect(parts.pending).toEqual([queued, steering]);
  });

  it("drops pending messages with no visible content", () => {
    const empty = msg({ content: "   ", pending: true });
    const withImage = msg({
      content: "",
      pending: true,
      imageDataUrls: ["data:image/png;base64,AQID"],
    });

    const parts = partitionConversation([empty, withImage]);

    expect(parts.pending).toEqual([withImage]);
    expect(parts.delivered).toEqual([]);
  });
});

describe("sourceHref", () => {
  it("uses the canonical Linear detail route", () => {
    expect(sourceHref("LinearProject", "personal-ns", "payments")).toBe("/linear/personal-ns/payments");
  });
});

describe("messageDeliveryTimestampMs", () => {
  it("anchors user messages to their delivery time when known", () => {
    const m = msg({ timestampUnix: 50n, deliveredAtUnix: 80n });
    expect(messageDeliveryTimestampMs(m)).toBe(80_000n);
  });

  it("falls back to the created timestamp for undelivered or legacy messages", () => {
    const m = msg({ timestampUnix: 50n, deliveredAtUnix: 0n });
    expect(messageDeliveryTimestampMs(m)).toBe(50_000n);
  });

  it("never re-anchors assistant messages", () => {
    const m = msg({ role: "assistant", timestampUnix: 50n, deliveredAtUnix: 80n });
    expect(messageDeliveryTimestampMs(m)).toBe(50_000n);
  });

  it("prefers the database-clock millisecond fields when present", () => {
    expect(messageDeliveryTimestampMs(msg({ timestampUnix: 50n, timestampUnixMs: 50_250n }))).toBe(50_250n);
    expect(
      messageDeliveryTimestampMs(msg({ timestampUnix: 50n, deliveredAtUnix: 80n, deliveredAtUnixMs: 80_750n })),
    ).toBe(80_750n);
  });
});

describe("orderDeliveredMessages", () => {
  it("prefers the durable delivery sequence over ambiguous timestamps", () => {
    const user = msg({ id: 2n, timestampUnix: 90n, deliveredAtUnix: 100n, deliverySequence: 11n });
    const assistant = msg({ id: 3n, role: "assistant", timestampUnix: 100n, deliverySequence: 10n });
    expect(orderDeliveredMessages([user, assistant])).toEqual([assistant, user]);
  });

  it("places an old-turn assistant reply before a queued message delivered later", () => {
    const queued = msg({ id: 2n, content: "next", timestampUnix: 30n, deliveredAtUnix: 60n });
    const oldReply = msg({ id: 3n, role: "assistant", content: "done", timestampUnix: 40n });
    expect(orderDeliveredMessages([queued, oldReply])).toEqual([oldReply, queued]);
  });

  it("orders by milliseconds where the seconds tie would misorder", () => {
    const assistant = msg({ id: 3n, role: "assistant", timestampUnix: 100n, timestampUnixMs: 100_900n });
    const secondUser = msg({ id: 2n, timestampUnix: 90n, deliveredAtUnix: 100n, deliveredAtUnixMs: 100_400n });
    const firstUser = msg({ id: 1n, timestampUnix: 80n, deliveredAtUnix: 100n, deliveredAtUnixMs: 100_200n });
    expect(orderDeliveredMessages([secondUser, assistant, firstUser])).toEqual([firstUser, secondUser, assistant]);

    const earlyReply = msg({ id: 4n, role: "assistant", timestampUnix: 100n, timestampUnixMs: 100_100n });
    expect(orderDeliveredMessages([secondUser, earlyReply, firstUser])).toEqual([earlyReply, firstUser, secondUser]);
  });

  it("orders a same-instant reply and stamped user row by delivery sequence, else by ID", () => {
    const assistant = msg({ id: 3n, role: "assistant", timestampUnixMs: 100_000n });
    const user = msg({ id: 2n, timestampUnix: 90n, deliveredAtUnixMs: 100_000n });
    expect(orderDeliveredMessages([assistant, user])).toEqual([user, assistant]);
    expect(orderDeliveredMessages([user, assistant])).toEqual([user, assistant]);

    const seqAssistant = msg({ id: 3n, role: "assistant", timestampUnixMs: 100_000n, deliverySequence: 7n });
    const seqUser = msg({ id: 2n, deliveredAtUnixMs: 100_000n, deliverySequence: 8n });
    expect(orderDeliveredMessages([seqUser, seqAssistant])).toEqual([seqAssistant, seqUser]);
  });

  it("keeps an unstamped kickoff before a same-second assistant reply", () => {
    const kickoff = msg({ id: 1n, timestampUnix: 100n });
    const assistant = msg({ id: 2n, role: "assistant", timestampUnix: 100n });
    expect(orderDeliveredMessages([assistant, kickoff])).toEqual([kickoff, assistant]);
  });

  it("orders same-second non-user roles by durable ID", () => {
    const assistant = msg({ id: 12n, role: "assistant", timestampUnix: 100n });
    const system = msg({ id: 11n, role: "system", timestampUnix: 100n });
    expect(orderDeliveredMessages([assistant, system])).toEqual([system, assistant]);
    expect(orderDeliveredMessages([system, assistant])).toEqual([system, assistant]);
  });

  it("uses the durable ID for a stable timeline key", () => {
    expect(messageTimelineKey(msg({ id: 42n, timestampUnix: 100n }), 0)).toBe("message:42");
  });
});

describe("dedupeFinalReplies", () => {
  function text(message: string) {
    return create(ActivityEntrySchema, { type: "assistant_text", message });
  }
  const tool = create(ActivityEntrySchema, { type: "tool_use", toolUseId: "t" });

  it("removes one match from the message's own segment", () => {
    const a = text("done");
    const b = text("done");
    const reply = msg({ role: "assistant", content: " done " });
    const out = dedupeFinalReplies([reply], [[a, tool, b]], []);
    expect(out.segments).toEqual([[a, tool]]);
    expect(out.trailing).toEqual([]);
  });

  it("removes a match recorded just after the message from the next segment", () => {
    const reply = msg({ role: "assistant", content: "done" });
    const next = msg({ role: "user", content: "thanks" });
    const late = text("done");
    const segments = [[tool], [late, tool]];
    const out = dedupeFinalReplies([reply, next], segments, []);
    expect(out.segments).toEqual([[tool], [tool]]);
    expect(segments[1]).toEqual([late, tool]);
  });

  it("removes a match from the trailing bucket after the last message", () => {
    const reply = msg({ role: "assistant", content: "done" });
    const late = text("done");
    const other = text("more work");
    const out = dedupeFinalReplies([reply], [[tool]], [late, other]);
    expect(out.segments).toEqual([[tool]]);
    expect(out.trailing).toEqual([other]);
  });

  it("leaves user messages and non-matching entries alone", () => {
    const echo = text("hello");
    const out = dedupeFinalReplies([msg({ content: "hello" })], [[echo]], []);
    expect(out.segments).toEqual([[echo]]);
  });
});
