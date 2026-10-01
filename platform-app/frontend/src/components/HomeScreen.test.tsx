import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { create } from "@bufbuild/protobuf";

import { HomeScreen } from "@/components/HomeScreen";
import { AgentRunSchema, UserInputRequestSchema } from "@/rpc/platform/service_pb";

const runsState = { runs: [] as ReturnType<typeof run>[] };

vi.mock("@/hooks/useAgentRuns", () => ({ useAgentRuns: () => runsState }));
vi.mock("@/hooks/useWatchedList", () => ({
  useProjects: () => ({
    projects: [{ namespace: "team", name: "widget", displayName: "Widget", metrics: { totalRuns: 3 } }],
    loading: false,
  }),
}));
vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({ user: { id: "u1", name: "Dana Ops", username: "dana" } }),
}));
vi.mock("@/components/onboarding/SetupChecklist", () => ({ SetupChecklist: () => null }));
vi.mock("@/components/onboarding/FeatureTour", () => ({ FeatureTour: () => null }));
vi.mock("@/components/CreateProjectDialog", () => ({
  CreateProjectDialog: ({ trigger }: { trigger: React.ReactNode }) => trigger,
}));
// The real composer talks to the backend; a stand-in exposes the prefill contract.
vi.mock("@/components/NewChatComposer", () => ({
  NewChatComposer: ({ prefill }: { prefill?: { text: string } }) => (
    <textarea aria-label="Composer" readOnly value={prefill?.text ?? ""} />
  ),
}));

afterEach(cleanup);

function run(name: string, phase: string, createdAtUnix: number, inputType = "") {
  return create(AgentRunSchema, {
    namespace: "team",
    name,
    displayName: name,
    phase,
    createdAtUnix: BigInt(createdAtUnix),
    userInputRequest: inputType ? create(UserInputRequestSchema, { type: inputType }) : undefined,
  });
}

function renderHome() {
  return render(
    <MemoryRouter>
      <HomeScreen />
    </MemoryRouter>,
  );
}

describe("HomeScreen", () => {
  it("prefills the composer from a starter prompt", () => {
    renderHome();
    fireEvent.click(screen.getByRole("button", { name: "Fix a bug" }));
    expect((screen.getByLabelText("Composer") as HTMLTextAreaElement).value).toBe(
      "Find and fix the bug where ",
    );
  });

  it("filters tasks into active, needs-you, and done buckets", () => {
    runsState.runs = [
      run("working", "Running", 30),
      run("asking", "Running", 20, "question"),
      run("shipped", "Succeeded", 10),
    ];
    renderHome();
    const tasks = screen.getByRole("region", { name: "Tasks" });
    const names = () => within(tasks).queryAllByRole("link").map((a) => a.textContent ?? "");

    expect(names().filter((n) => /working|asking|shipped/.test(n))).toHaveLength(3);

    fireEvent.click(screen.getByRole("tab", { name: /Needs you/ }));
    expect(names().some((n) => n.includes("asking"))).toBe(true);
    expect(names().some((n) => n.includes("working"))).toBe(false);

    fireEvent.click(screen.getByRole("tab", { name: /Active/ }));
    expect(names().some((n) => n.includes("working"))).toBe(true);
    expect(names().some((n) => n.includes("asking"))).toBe(false);

    fireEvent.click(screen.getByRole("tab", { name: /Done/ }));
    expect(names().some((n) => n.includes("shipped"))).toBe(true);
    expect(names().some((n) => n.includes("working"))).toBe(false);
  });

  it("lists projects as quick-jump cards", () => {
    renderHome();
    const link = screen.getByRole("link", { name: /Widget/ });
    expect(link.getAttribute("href")).toBe("/projects/team/widget");
    expect(link.textContent).toContain("3 runs");
  });
});
