import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import { OUTBOUND_MESSAGE_TTL_MS, PendingMessages, settleOutboundMessages, type OutboundMessage } from "./PendingMessages";
import { ChatMessageSchema } from "@/rpc/platform/service_pb";

afterEach(() => {
  cleanup();
});

describe("PendingMessages", () => {
  it("renders nothing when there are no pending messages", () => {
    const { container } = render(<PendingMessages messages={[]} />);
    expect(container.firstChild).toBeNull();
  });

  it("labels queued and steering messages with their content", () => {
    const queued = create(ChatMessageSchema, {
      role: "user",
      content: "please also update the docs",
      pending: true,
      queueMode: "enqueue",
    });
    const steering = create(ChatMessageSchema, {
      role: "user",
      content: "stop, wrong branch",
      pending: true,
      queueMode: "immediate",
    });

    render(<PendingMessages messages={[queued, steering]} />);

    expect(screen.getByText("Queued")).toBeTruthy();
    expect(screen.getByText("please also update the docs")).toBeTruthy();
    expect(screen.getByText("Steering")).toBeTruthy();
    expect(screen.getByText("stop, wrong branch")).toBeTruthy();
  });

  it("describes image-only messages", () => {
    const imageOnly = create(ChatMessageSchema, {
      role: "user",
      content: "",
      pending: true,
      queueMode: "enqueue",
      imageDataUrls: ["data:image/png;base64,AQID"],
    });

    render(<PendingMessages messages={[imageOnly]} />);

    expect(screen.getByText("1 image attachment")).toBeTruthy();
  });

  it("invokes the edit and cancel callbacks with the message", () => {
    const message = create(ChatMessageSchema, {
      id: 7n,
      role: "user",
      content: "queued follow-up",
      pending: true,
      queueMode: "enqueue",
    });
    const onEdit = vi.fn();
    const onCancel = vi.fn();

    render(<PendingMessages messages={[message]} onEdit={onEdit} onCancel={onCancel} />);

    fireEvent.click(screen.getByRole("button", { name: "Edit message" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel message" }));

    expect(onEdit).toHaveBeenCalledWith(message);
    expect(onCancel).toHaveBeenCalledWith(message);
  });

  it("disables the actions while an operation is in flight", () => {
    const message = create(ChatMessageSchema, {
      id: 7n,
      role: "user",
      content: "queued follow-up",
      pending: true,
      queueMode: "enqueue",
    });

    render(
      <PendingMessages messages={[message]} onEdit={vi.fn()} onCancel={vi.fn()} busy />,
    );

    expect(
      (screen.getByRole("button", { name: "Edit message" }) as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(
      (screen.getByRole("button", { name: "Cancel message" }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("hides the actions for messages without a durable id", () => {
    const legacy = create(ChatMessageSchema, {
      role: "user",
      content: "old snapshot message",
      pending: true,
      queueMode: "enqueue",
    });

    render(<PendingMessages messages={[legacy]} onEdit={vi.fn()} onCancel={vi.fn()} />);

    expect(screen.queryByRole("button", { name: "Edit message" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Cancel message" })).toBeNull();
  });

  it("shows no action buttons when handlers are absent (read-only viewers)", () => {
    const message = create(ChatMessageSchema, {
      id: 7n,
      role: "user",
      content: "queued follow-up",
      pending: true,
      queueMode: "enqueue",
    });

    render(<PendingMessages messages={[message]} />);

    expect(screen.queryByRole("button")).toBeNull();
  });

  it("labels terminal backlog as delivery-unconfirmed and disables actions", () => {
    const message = create(ChatMessageSchema, {
      id: 7n,
      role: "user",
      content: "too late",
      pending: true,
      queueMode: "immediate",
    });

    render(<PendingMessages messages={[message]} terminal onEdit={vi.fn()} onCancel={vi.fn()} />);

    expect(screen.getByText("Delivery unconfirmed — run ended")).toBeTruthy();
    expect(screen.getByText("too late")).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("renders locally sent messages as Sending… without row actions", () => {
    const outbound: OutboundMessage[] = [
      { clientMessageId: "c1", content: "on its way", imageCount: 0, sentAt: 0 },
      { clientMessageId: "c2", content: "", imageCount: 2, sentAt: 0 },
    ];

    render(<PendingMessages messages={[]} outbound={outbound} onCancel={vi.fn()} onEdit={vi.fn()} />);

    expect(screen.getAllByText("Sending…")).toHaveLength(2);
    expect(screen.getByText("on its way")).toBeTruthy();
    expect(screen.getByText("2 image attachments")).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
  });
});

describe("settleOutboundMessages", () => {
  const sent: OutboundMessage = { clientMessageId: "c1", content: "hi", imageCount: 0, sentAt: 1_000, messageId: 7n };
  const unacked: OutboundMessage = { clientMessageId: "c2", content: "yo", imageCount: 0, sentAt: 1_000 };

  it("drops rows once the conversation carries their server message id", () => {
    const echoed = create(ChatMessageSchema, { id: 7n, role: "user", content: "hi", pending: true });
    expect(settleOutboundMessages([sent, unacked], [echoed])).toEqual([unacked]);
  });

  it("returns the same array when nothing settled", () => {
    const outbound = [sent, unacked];
    expect(settleOutboundMessages(outbound, [])).toBe(outbound);
    expect(settleOutboundMessages(outbound, [], 1_000 + OUTBOUND_MESSAGE_TTL_MS - 1)).toBe(outbound);
  });

  it("expires rows past the TTL only when a clock is supplied", () => {
    expect(settleOutboundMessages([sent, unacked], [], 1_000 + OUTBOUND_MESSAGE_TTL_MS)).toEqual([]);
    expect(settleOutboundMessages([sent, unacked], [])).toEqual([sent, unacked]);
  });
});
