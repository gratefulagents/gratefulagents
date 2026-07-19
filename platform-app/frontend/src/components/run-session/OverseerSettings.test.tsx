import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { client } from "@/lib/client";
import type { AgentRun } from "@/rpc/platform/service_pb";
import { OverseerSettings } from "./OverseerSettings";

vi.mock("@/lib/client", () => ({ client: {
  attachAgentRunOverseer: vi.fn(), updateAgentRunOverseer: vi.fn(), detachAgentRunOverseer: vi.fn(),
} }));

afterEach(() => { cleanup(); vi.clearAllMocks(); });

const base = { namespace: "ns", name: "run", phase: "Running", overseerDetaching: false } as AgentRun;
const attached = { ...base, overseer: { modeRefName: "review", modeRefVersion: "v2", modeRefChannel: "stable", model: "opus", authority: "enforce", intervalMinutes: 20, maxInterventions: 0 } } as AgentRun;

describe("OverseerSettings", () => {
  it("attaches with the complete configuration, including an explicit zero cap", async () => {
    vi.mocked(client.attachAgentRunOverseer).mockResolvedValue(attached);
    render(<OverseerSettings run={base} canManage />);
    fireEvent.change(screen.getByLabelText("Overseer mode name"), { target: { value: "review" } });
    fireEvent.change(screen.getByLabelText("Overseer mode version"), { target: { value: "v2" } });
    fireEvent.change(screen.getByLabelText("Overseer mode channel"), { target: { value: "stable" } });
    fireEvent.change(screen.getByLabelText("Overseer model"), { target: { value: "opus" } });
    fireEvent.change(screen.getByLabelText("Overseer authority"), { target: { value: "enforce" } });
    fireEvent.change(screen.getByLabelText("Overseer interval"), { target: { value: "20" } });
    fireEvent.change(screen.getByLabelText("Overseer max interventions"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Attach overseer" }));
    await waitFor(() => expect(client.attachAgentRunOverseer).toHaveBeenCalledWith({ namespace: "ns", name: "run", overseer: { modeRefName: "review", modeRefVersion: "v2", modeRefChannel: "stable", model: "opus", authority: "enforce", intervalMinutes: 20, maxInterventions: 0 } }));
    expect(await screen.findByText("Attached")).toBeTruthy();
  });

  it("updates mutable settings and detaches while keeping mode and model immutable", async () => {
    vi.mocked(client.updateAgentRunOverseer).mockResolvedValue({ ...attached, overseer: { ...attached.overseer!, authority: "observe", intervalMinutes: 7, maxInterventions: 0 } } as AgentRun);
    vi.mocked(client.detachAgentRunOverseer).mockResolvedValue({ ...base, overseerDetaching: true } as AgentRun);
    render(<OverseerSettings run={attached} canManage />);
    expect(screen.queryByLabelText("Overseer mode name")).toBeNull();
    expect(screen.getByText("opus")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Overseer authority"), { target: { value: "observe" } });
    fireEvent.change(screen.getByLabelText("Overseer interval"), { target: { value: "7" } });
    fireEvent.click(screen.getByRole("button", { name: "Save overseer" }));
    await waitFor(() => expect(client.updateAgentRunOverseer).toHaveBeenCalledWith({ namespace: "ns", name: "run", authority: "observe", intervalMinutes: 7, maxInterventions: 0 }));
    fireEvent.click(screen.getByRole("button", { name: "Detach overseer" }));
    expect(await screen.findByText("Detaching")).toBeTruthy();
  });

  it("adopts later watch snapshots after showing a mutation response", async () => {
    vi.mocked(client.attachAgentRunOverseer).mockResolvedValue(attached);
    const { rerender } = render(<OverseerSettings run={base} canManage />);
    fireEvent.click(screen.getByRole("button", { name: "Attach overseer" }));
    expect(await screen.findByText("Attached")).toBeTruthy();

    rerender(<OverseerSettings canManage run={{ ...attached, overseerSummary: { runName: "run-overseer", state: "checking", checkpointsHandled: 3n, interventionsUsed: 2, completionRejectionsUsed: 1, lastVerdict: "revise", lastSummary: "Fresh watch summary", lastVerdictAtUnix: 1n } } as AgentRun} />);
    expect(await screen.findByText("Fresh watch summary")).toBeTruthy();
    expect(screen.getByText("checking")).toBeTruthy();
  });

  it("shows the complete status summary", () => {
    render(<OverseerSettings canManage run={{ ...attached, overseerSummary: { runName: "run-overseer", state: "checking", checkpointsHandled: 3n, interventionsUsed: 2, completionRejectionsUsed: 1, lastVerdict: "revise", lastSummary: "Needs tests", lastVerdictAtUnix: 1n } } as AgentRun} />);
    for (const text of ["checking", "run-overseer", "3", "2", "1", "revise", "Needs tests"]) expect(screen.getByText(text)).toBeTruthy();
  });

  it("gates controls for nonowners and terminal runs", () => {
    const { rerender } = render(<OverseerSettings run={base} canManage={false} />);
    expect(screen.queryByRole("button", { name: "Attach overseer" })).toBeNull();
    rerender(<OverseerSettings run={{ ...base, phase: "Succeeded" } as AgentRun} canManage />);
    expect(screen.queryByRole("button", { name: "Attach overseer" })).toBeNull();
  });

  it("shows detaching state and summary markers without allowing reattach", () => {
    const { rerender } = render(<OverseerSettings run={{ ...base, overseerDetaching: true } as AgentRun} canManage />);
    expect(screen.getByText("Overseer detachment is in progress.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Attach overseer" })).toBeNull();
    rerender(<OverseerSettings run={{ ...base, overseerSummary: { state: "stopped", checkpointsHandled: 0n } } as AgentRun} canManage />);
    expect(screen.getByText("This overseer attachment is no longer active.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Attach overseer" })).toBeNull();
  });
});
