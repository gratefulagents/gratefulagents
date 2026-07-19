import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { createRef } from "react";
import { create } from "@bufbuild/protobuf";

import { RunSessionFooter } from "./RunSessionFooter";
import type { SlashCommand } from "./slashCommands";
import { AgentRunMessageMode, AgentRunSchema } from "@/rpc/platform/service_pb";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    listWorkspaceFiles: vi.fn().mockResolvedValue({ paths: [], truncated: false }),
    listAvailableModels: vi.fn().mockResolvedValue({ models: [] }),
  },
}));

const listWorkspaceFilesMock = client.listWorkspaceFiles as unknown as ReturnType<typeof vi.fn>;

afterEach(() => {
  cleanup();
});

const noopAttachments = {
  images: [],
  error: null,
  remove: vi.fn(),
  addFiles: vi.fn(),
  onPaste: vi.fn(),
};

const planCommand: SlashCommand = {
  id: "plan",
  trigger: "/plan",
  title: "Plan mode",
  description: "Read-only planning.",
  action: { kind: "mode", target: "plan" },
};

describe("RunSessionFooter", () => {
  it("shows failed state and wires retry", () => {
    const handleRetry = vi.fn();

    render(
      <RunSessionFooter
        isActive={false}
        isViewer={false}
        sending={false}
        canSendMessage={false}
        startupCopy="Preparing run…"
        attachments={noopAttachments}
        fileInputRef={createRef<HTMLInputElement>()}
        reply=""
        setReply={vi.fn()}
        handleSend={vi.fn()}
        sendMode={AgentRunMessageMode.ENQUEUE}
        setSendMode={vi.fn()}
        slashCommands={[]}
        onRunSlashCommand={vi.fn()}
        phase="Failed"
        blockedReason="Tests failed"
        canExtendRuntime={false}
        setExtendRuntimeOpen={vi.fn()}
        extendingRuntime={false}
        canRetry
        handleRetry={handleRetry}
        retrying={false}
      />,
    );

    expect(screen.getByText("Failed: Tests failed")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(handleRetry).toHaveBeenCalledTimes(1);
  });

  it("shows stopped state and wires resume", () => {
    const handleRetry = vi.fn();

    render(
      <RunSessionFooter
        isActive={false}
        isViewer={false}
        sending={false}
        canSendMessage={false}
        startupCopy="Preparing run…"
        attachments={noopAttachments}
        fileInputRef={createRef<HTMLInputElement>()}
        reply=""
        setReply={vi.fn()}
        handleSend={vi.fn()}
        sendMode={AgentRunMessageMode.ENQUEUE}
        setSendMode={vi.fn()}
        slashCommands={[]}
        onRunSlashCommand={vi.fn()}
        phase="Cancelled"
        blockedReason="cancelled by user"
        canExtendRuntime={false}
        setExtendRuntimeOpen={vi.fn()}
        extendingRuntime={false}
        canRetry
        handleRetry={handleRetry}
        retrying={false}
      />,
    );

    expect(screen.getByText("Run stopped.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(handleRetry).toHaveBeenCalledTimes(1);
  });

  it("opens the slash palette and runs a command on Enter without sending a message", () => {
    const onRunSlashCommand = vi.fn();
    const handleSend = vi.fn();
    const setReply = vi.fn();

    render(
      <RunSessionFooter
        isActive
        isViewer={false}
        sending={false}
        canSendMessage
        startupCopy="Preparing run…"
        attachments={noopAttachments}
        fileInputRef={createRef<HTMLInputElement>()}
        reply="/plan"
        setReply={setReply}
        handleSend={handleSend}
        sendMode={AgentRunMessageMode.ENQUEUE}
        setSendMode={vi.fn()}
        slashCommands={[planCommand]}
        onRunSlashCommand={onRunSlashCommand}
        phase="Running"
        blockedReason=""
        canExtendRuntime={false}
        setExtendRuntimeOpen={vi.fn()}
        extendingRuntime={false}
        canRetry={false}
        handleRetry={vi.fn()}
        retrying={false}
      />,
    );

    expect(screen.getByRole("listbox", { name: "Slash commands" })).toBeTruthy();
    expect(screen.getByRole("option", { name: /Plan mode/ })).toBeTruthy();

    const textarea = screen.getByRole("combobox", { name: "Type your reply" });
    fireEvent.keyDown(textarea, { key: "Enter" });

    expect(onRunSlashCommand).toHaveBeenCalledWith(planCommand);
    expect(handleSend).not.toHaveBeenCalled();
    expect(setReply).toHaveBeenCalledWith("");
  });

  it("runs a command on click via mousedown", () => {
    const onRunSlashCommand = vi.fn();

    render(
      <RunSessionFooter
        isActive
        isViewer={false}
        sending={false}
        canSendMessage
        startupCopy="Preparing run…"
        attachments={noopAttachments}
        fileInputRef={createRef<HTMLInputElement>()}
        reply="/"
        setReply={vi.fn()}
        handleSend={vi.fn()}
        sendMode={AgentRunMessageMode.ENQUEUE}
        setSendMode={vi.fn()}
        slashCommands={[planCommand]}
        onRunSlashCommand={onRunSlashCommand}
        phase="Running"
        blockedReason=""
        canExtendRuntime={false}
        setExtendRuntimeOpen={vi.fn()}
        extendingRuntime={false}
        canRetry={false}
        handleRetry={vi.fn()}
        retrying={false}
      />,
    );

    fireEvent.mouseDown(screen.getByRole("option", { name: /Plan mode/ }));
    expect(onRunSlashCommand).toHaveBeenCalledWith(planCommand);
  });

  it("opens the @ file picker and inserts the selected path", async () => {
    listWorkspaceFilesMock.mockResolvedValueOnce({
      paths: ["src/main.ts", "docs/readme.md"],
      truncated: false,
    });
    const setReply = vi.fn();

    render(
      <RunSessionFooter
        isActive
        isViewer={false}
        sending={false}
        canSendMessage
        startupCopy="Preparing run…"
        attachments={noopAttachments}
        fileInputRef={createRef<HTMLInputElement>()}
        reply="@main"
        setReply={setReply}
        handleSend={vi.fn()}
        sendMode={AgentRunMessageMode.ENQUEUE}
        setSendMode={vi.fn()}
        slashCommands={[]}
        onRunSlashCommand={vi.fn()}
        phase="Running"
        blockedReason=""
        canExtendRuntime={false}
        setExtendRuntimeOpen={vi.fn()}
        extendingRuntime={false}
        canRetry={false}
        handleRetry={vi.fn()}
        retrying={false}
        namespace="ns"
        name="run"
      />,
    );

    const option = await screen.findByRole("option");
    expect(listWorkspaceFilesMock).toHaveBeenCalled();
    expect(option.textContent).toBe("src/main.ts");
    fireEvent.mouseDown(option);
    expect(setReply).toHaveBeenCalledWith("@src/main.ts ");
  });

  it("always shows provider, auth mode, and model in the composer meta row", () => {
    const run = create(AgentRunSchema, {
      namespace: "ns",
      name: "run",
      phase: "Running",
      model: "anthropic/claude-opus-4-5",
      authMode: "oauth",
    });

    render(
      <RunSessionFooter
        isActive
        isViewer={false}
        sending={false}
        canSendMessage
        startupCopy="Preparing run…"
        attachments={noopAttachments}
        fileInputRef={createRef<HTMLInputElement>()}
        reply=""
        setReply={vi.fn()}
        handleSend={vi.fn()}
        sendMode={AgentRunMessageMode.ENQUEUE}
        setSendMode={vi.fn()}
        slashCommands={[]}
        onRunSlashCommand={vi.fn()}
        phase="Running"
        blockedReason=""
        canExtendRuntime={false}
        setExtendRuntimeOpen={vi.fn()}
        extendingRuntime={false}
        canRetry={false}
        handleRetry={vi.fn()}
        retrying={false}
        namespace="ns"
        name="run"
        run={run}
        canUpdateRuntimeConfig
        updatingRuntimeConfig={false}
        onUpdateRuntimeConfig={vi.fn()}
      />,
    );

    const trigger = screen.getByRole("button", { name: "Switch provider or model" });
    expect(trigger.textContent).toContain("anthropic");
    expect(trigger.textContent).toContain("oauth");
    expect(trigger.textContent).toContain("claude-opus-4-5");
  });

  it("renders the provider/model readout without a switcher for viewers", () => {
    const run = create(AgentRunSchema, {
      namespace: "ns",
      name: "run",
      phase: "Running",
      model: "openai/gpt-5.2",
      authMode: "api-key",
    });

    render(
      <RunSessionFooter
        isActive={false}
        isViewer
        sending={false}
        canSendMessage={false}
        startupCopy="Preparing run…"
        attachments={noopAttachments}
        fileInputRef={createRef<HTMLInputElement>()}
        reply=""
        setReply={vi.fn()}
        handleSend={vi.fn()}
        sendMode={AgentRunMessageMode.ENQUEUE}
        setSendMode={vi.fn()}
        slashCommands={[]}
        onRunSlashCommand={vi.fn()}
        phase="Running"
        blockedReason=""
        canExtendRuntime={false}
        setExtendRuntimeOpen={vi.fn()}
        extendingRuntime={false}
        canRetry={false}
        handleRetry={vi.fn()}
        retrying={false}
        namespace="ns"
        name="run"
        run={run}
        canUpdateRuntimeConfig={false}
        updatingRuntimeConfig={false}
        onUpdateRuntimeConfig={vi.fn()}
      />,
    );

    expect(screen.queryByRole("button", { name: "Switch provider or model" })).toBeNull();
    const readout = screen.getByTitle("Provider · auth · model");
    expect(readout.textContent).toContain("openai");
    expect(readout.textContent).toContain("api");
    expect(readout.textContent).toContain("gpt-5.2");
  });
});
