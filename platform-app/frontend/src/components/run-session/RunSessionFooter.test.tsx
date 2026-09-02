import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen } from "@testing-library/react";
import { createRef } from "react";
import { create } from "@bufbuild/protobuf";

import { RunSessionFooter, type RunSessionComposer } from "./RunSessionFooter";
import type { SlashCommand } from "./slashCommands";
import { renderWithRunActions } from "./testing";
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
  videos: [],
  error: null,
  processing: false,
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

function composer(overrides: Partial<RunSessionComposer> = {}): RunSessionComposer {
  return {
    reply: "",
    setReply: vi.fn(),
    handleSend: vi.fn(),
    sendMode: AgentRunMessageMode.ENQUEUE,
    setSendMode: vi.fn(),
    slashCommands: [],
    onRunSlashCommand: vi.fn(),
    fileInputRef: createRef<HTMLInputElement>(),
    attachments: noopAttachments,
    ...overrides,
  };
}

function runWith(phase: string, blockedReason = "") {
  return create(AgentRunSchema, { namespace: "ns", name: "run", phase, blockedReason });
}

describe("RunSessionFooter", () => {
  it("shows failed state and wires retry", () => {
    const { actions } = renderWithRunActions(
      <RunSessionFooter
        run={runWith("Failed", "Tests failed")}
        isActive={false}
        isViewer={false}
        sending={false}
        canSendMessage={false}
        startupCopy="Preparing run…"
        composer={composer()}
      />,
      { retry: { can: true } },
    );

    expect(screen.getByText("Failed: Tests failed")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(actions.retry.run).toHaveBeenCalledTimes(1);
  });

  it("shows stopped state and wires resume", () => {
    const { actions } = renderWithRunActions(
      <RunSessionFooter
        run={runWith("Cancelled", "cancelled by user")}
        isActive={false}
        isViewer={false}
        sending={false}
        canSendMessage={false}
        startupCopy="Preparing run…"
        composer={composer()}
      />,
      { retry: { can: true } },
    );

    expect(screen.getByText("Run stopped.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(actions.retry.run).toHaveBeenCalledTimes(1);
  });

  it("opens the slash palette and runs a command on Enter without sending a message", () => {
    const onRunSlashCommand = vi.fn();
    const handleSend = vi.fn();
    const setReply = vi.fn();

    renderWithRunActions(
      <RunSessionFooter
        run={runWith("Running")}
        isActive
        isViewer={false}
        sending={false}
        canSendMessage
        startupCopy="Preparing run…"
        composer={composer({ reply: "/plan", setReply, handleSend, slashCommands: [planCommand], onRunSlashCommand })}
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

  it("only exposes combobox semantics while a menu is mounted", () => {
    const { rerender } = renderWithRunActions(
      <RunSessionFooter
        run={runWith("Running")}
        isActive
        isViewer={false}
        sending={false}
        canSendMessage
        startupCopy="Preparing run…"
        composer={composer({ reply: "hello", slashCommands: [planCommand] })}
      />,
    );

    const plain = screen.getByRole("textbox", { name: "Type your reply" });
    expect(plain.getAttribute("role")).toBeNull();
    expect(plain.getAttribute("aria-expanded")).toBeNull();
    expect(plain.getAttribute("aria-controls")).toBeNull();
    expect(screen.queryByRole("listbox")).toBeNull();

    rerender(
      <RunSessionFooter
        run={runWith("Running")}
        isActive
        isViewer={false}
        sending={false}
        canSendMessage
        startupCopy="Preparing run…"
        composer={composer({ reply: "/", slashCommands: [planCommand] })}
      />,
    );

    const combobox = screen.getByRole("combobox", { name: "Type your reply" });
    const listbox = screen.getByRole("listbox", { name: "Slash commands" });
    expect(combobox.getAttribute("aria-expanded")).toBe("true");
    expect(combobox.getAttribute("aria-controls")).toBe(listbox.id);
    const option = screen.getByRole("option", { name: /Plan mode/ });
    expect(option.getAttribute("aria-selected")).toBe("true");
    expect(combobox.getAttribute("aria-activedescendant")).toBe(option.id);
  });

  it("runs a command on click via mousedown", () => {
    const onRunSlashCommand = vi.fn();

    renderWithRunActions(
      <RunSessionFooter
        run={runWith("Running")}
        isActive
        isViewer={false}
        sending={false}
        canSendMessage
        startupCopy="Preparing run…"
        composer={composer({ reply: "/", slashCommands: [planCommand], onRunSlashCommand })}
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

    renderWithRunActions(
      <RunSessionFooter
        run={runWith("Running")}
        isActive
        isViewer={false}
        sending={false}
        canSendMessage
        startupCopy="Preparing run…"
        composer={composer({ reply: "@main", setReply })}
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

  it("swallows Enter while the file picker is still loading instead of sending", async () => {
    let resolveFiles: (value: { paths: string[]; truncated: boolean }) => void = () => {};
    listWorkspaceFilesMock.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveFiles = resolve;
        }),
    );
    const handleSend = vi.fn();

    renderWithRunActions(
      <RunSessionFooter
        run={runWith("Running")}
        isActive
        isViewer={false}
        sending={false}
        canSendMessage
        startupCopy="Preparing run…"
        composer={composer({ reply: "@main", handleSend })}
        namespace="ns"
        name="run"
      />,
    );

    expect(await screen.findByText("Loading files…")).toBeTruthy();
    fireEvent.keyDown(screen.getByRole("combobox", { name: "Type your reply" }), { key: "Enter" });
    expect(handleSend).not.toHaveBeenCalled();

    resolveFiles({ paths: ["src/main.ts"], truncated: false });
    expect(await screen.findByRole("option")).toBeTruthy();
  });

  it("exposes Steer/Queue as a radio group", () => {
    const setSendMode = vi.fn();

    renderWithRunActions(
      <RunSessionFooter
        run={runWith("Running")}
        isActive
        isViewer={false}
        sending={false}
        canSendMessage
        startupCopy="Preparing run…"
        composer={composer({ setSendMode })}
      />,
    );

    const group = screen.getByRole("radiogroup", { name: "Delivery" });
    const steer = screen.getByRole("radio", { name: "Steer" });
    const queue = screen.getByRole("radio", { name: "Queue" });
    expect(group.contains(steer) && group.contains(queue)).toBe(true);
    expect(steer.getAttribute("aria-checked")).toBe("false");
    expect(queue.getAttribute("aria-checked")).toBe("true");

    fireEvent.click(steer);
    expect(setSendMode).toHaveBeenCalledWith(AgentRunMessageMode.IMMEDIATE);
  });

  it("always shows provider, auth mode, and model in the composer meta row", () => {
    const run = create(AgentRunSchema, {
      namespace: "ns",
      name: "run",
      phase: "Running",
      model: "anthropic/claude-opus-4-5",
      authMode: "oauth",
    });

    renderWithRunActions(
      <RunSessionFooter
        run={run}
        isActive
        isViewer={false}
        sending={false}
        canSendMessage
        startupCopy="Preparing run…"
        composer={composer()}
        namespace="ns"
        name="run"
        runtimeConfig={{ canUpdate: true, updating: false, onUpdate: vi.fn() }}
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

    renderWithRunActions(
      <RunSessionFooter
        run={run}
        isActive={false}
        isViewer
        sending={false}
        canSendMessage={false}
        startupCopy="Preparing run…"
        composer={composer()}
        namespace="ns"
        name="run"
        runtimeConfig={{ canUpdate: false, updating: false, onUpdate: vi.fn() }}
      />,
    );

    expect(screen.queryByRole("button", { name: "Switch provider or model" })).toBeNull();
    const readout = screen.getByTitle("Provider · auth · model");
    expect(readout.textContent).toContain("openai");
    expect(readout.textContent).toContain("api");
    expect(readout.textContent).toContain("gpt-5.2");
  });

  function renderActiveFooter(
    composerOverrides: Partial<RunSessionComposer> = {},
    actions: Parameters<typeof renderWithRunActions>[1] = {},
  ) {
    return renderWithRunActions(
      <RunSessionFooter
        run={runWith("Running")}
        isActive
        isViewer={false}
        sending={false}
        canSendMessage
        startupCopy="Preparing run…"
        composer={composer(composerOverrides)}
      />,
      actions,
    );
  }

  it("keeps the stop control disabled as Stopping… once an interrupt was accepted", () => {
    const { actions } = renderActiveFooter({}, { interrupt: { can: true, pending: true } });

    const stopping = screen.getByRole("button", { name: "Stopping the current turn" });
    expect((stopping as HTMLButtonElement).disabled).toBe(true);
    expect(stopping.textContent).toContain("Stopping…");
    expect(screen.queryByRole("button", { name: "Stop the current turn" })).toBeNull();
    fireEvent.click(stopping);
    expect(actions.interrupt.run).not.toHaveBeenCalled();
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("re-enables the stop control and shows the fallback hint when the interrupt stalls", () => {
    const { actions } = renderActiveFooter({}, { interrupt: { can: true, pending: false, stalled: true } });

    expect(screen.getByRole("status").textContent).toContain("Still running");
    fireEvent.click(screen.getByRole("button", { name: "Stop the current turn" }));
    expect(actions.interrupt.run).toHaveBeenCalledTimes(1);
  });

  it("hides the Steer/Queue toggle unless the agent is mid-turn", () => {
    renderActiveFooter({ showSendMode: false });
    expect(screen.queryByRole("radio", { name: "Steer" })).toBeNull();
    expect(screen.queryByRole("radio", { name: "Queue" })).toBeNull();
    cleanup();

    renderActiveFooter({ showSendMode: true });
    expect(screen.getByRole("radio", { name: "Steer" })).toBeTruthy();
    expect(screen.getByRole("radio", { name: "Queue" })).toBeTruthy();
  });
});
