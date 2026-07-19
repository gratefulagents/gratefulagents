import { describe, expect, it } from "vitest";
import { create, type MessageInitShape } from "@bufbuild/protobuf";

import { messageDeliveryTimestamp, messageTimelineKey, orderDeliveredMessages, partitionConversation, sourceHref, thinkingLabel } from "./helpers";
import { ChatMessageSchema } from "@/rpc/platform/service_pb";

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

describe("messageDeliveryTimestamp", () => {
  it("anchors user messages to their delivery time when known", () => {
    const m = msg({ timestampUnix: 50n, deliveredAtUnix: 80n });
    expect(messageDeliveryTimestamp(m)).toBe(80n);
  });

  it("falls back to the created timestamp for undelivered or legacy messages", () => {
    const m = msg({ timestampUnix: 50n, deliveredAtUnix: 0n });
    expect(messageDeliveryTimestamp(m)).toBe(50n);
  });

  it("never re-anchors assistant messages", () => {
    const m = msg({ role: "assistant", timestampUnix: 50n, deliveredAtUnix: 80n });
    expect(messageDeliveryTimestamp(m)).toBe(50n);
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

  it("keeps an old-turn reply before queued messages delivered in the same second", () => {
    const assistant = msg({ id: 3n, role: "assistant", timestampUnix: 100n });
    const secondUser = msg({ id: 2n, timestampUnix: 90n, deliveredAtUnix: 100n });
    const firstUser = msg({ id: 1n, timestampUnix: 80n, deliveredAtUnix: 100n });
    expect(orderDeliveredMessages([secondUser, assistant, firstUser])).toEqual([assistant, firstUser, secondUser]);
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
