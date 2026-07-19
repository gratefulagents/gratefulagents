import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import { PendingMessages } from "./PendingMessages";
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
});
