import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AgentRunSchema } from "@/rpc/platform/service_pb";
import { RunHeader, RunUsageSummary } from "./RunHeader";
import { resolveRunUsageTokens } from "./runUsage";
import { renderWithRunActions } from "./testing";

vi.mock("@/lib/client", () => ({
  client: {
    listAvailableModes: vi.fn().mockResolvedValue({ modes: [] }),
    listAvailableModels: vi.fn().mockResolvedValue({ models: [] }),
  },
  binaryClient: { exportAgentRunArchive: vi.fn() },
}));

vi.mock("@/components/ui/toaster", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

afterEach(cleanup);

function makeRun(overrides: Parameters<typeof create<typeof AgentRunSchema>>[1] = {}) {
  return create(AgentRunSchema, {
    namespace: "ns",
    name: "run-1",
    phase: "Running",
    myPermission: "owner",
    ...overrides,
  });
}

function renderHeader(
  run = makeRun(),
  actions: Parameters<typeof renderWithRunActions>[1] = {},
  props: Partial<React.ComponentProps<typeof RunHeader>> = {},
) {
  return renderWithRunActions(
    <MemoryRouter>
      <RunHeader
        namespace="ns"
        name="run-1"
        run={run}
        viewers={[]}
        prUrls={[]}
        showCreatePRButton={false}
        displayCostUsd={null}
        sessionMetrics={null}
        permissions={{ isOwnerOrAdmin: true, isViewer: false }}
        inspector={{ open: false, onToggle: vi.fn(), attention: false }}
        plan={{ hasPlan: false, planContent: "" }}
        {...props}
      />
    </MemoryRouter>,
    actions,
  );
}

describe("RunHeader", () => {
  it("makes Reply the primary action while the agent waits for input and demotes Stop", () => {
    const run = makeRun({ userInputRequest: { type: "question", message: "Which branch?" } });
    const { actions } = renderHeader(run, {
      stop: { can: true },
      promote: { can: true },
      focusComposer: vi.fn(),
    });

    const reply = screen.getByRole("button", { name: "Reply" });
    fireEvent.click(reply);
    expect(actions.focusComposer).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Mark succeeded" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Run actions" }));
    expect(screen.getByRole("menuitem", { name: "Stop run" })).toBeTruthy();
  });

  it("labels the primary action Review plan when a plan awaits approval", () => {
    const run = makeRun({ userInputRequest: { type: "plan_review", message: "Plan ready" } });
    renderHeader(run, { stop: { can: true } });

    expect(screen.getByRole("button", { name: "Review plan" })).toBeTruthy();
  });

  it("falls back to Stop when nothing is awaiting input", () => {
    const { actions } = renderHeader(makeRun(), { stop: { can: true } });

    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    expect(actions.stop.run).toHaveBeenCalledTimes(1);
  });

  it("renders the canonical status badge for the phase", () => {
    const run = makeRun({ userInputRequest: { type: "question", message: "?" } });
    renderHeader(run, {}, { permissions: { isOwnerOrAdmin: false, isViewer: true } });

    expect(screen.getByRole("button", { name: /Run status: Awaiting input/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Reply" })).toBeNull();
  });

  it("drops the run-context sheet button and opens the inspector's Context tab from the overflow menu", () => {
    const { actions } = renderHeader();

    expect(screen.queryByRole("button", { name: "Run context" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Run actions" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Run context" }));
    expect(actions.openInspectorTab).toHaveBeenCalledWith("context");
  });

  it("links pull requests as external anchors and keeps them reachable from the overflow menu", () => {
    renderHeader(makeRun(), {}, { prUrls: ["https://github.com/acme/app/pull/7"] });

    const pill = screen.getByRole("link", { name: "Pull request" });
    expect(pill.getAttribute("href")).toBe("https://github.com/acme/app/pull/7");
    expect(pill.getAttribute("target")).toBe("_blank");
    expect(pill.getAttribute("rel")).toBe("noreferrer");

    fireEvent.click(screen.getByRole("button", { name: "Run actions" }));
    const item = screen.getByRole("menuitem", { name: "Open pull request" });
    expect(item.getAttribute("href")).toBe("https://github.com/acme/app/pull/7");
    expect(item.getAttribute("target")).toBe("_blank");
  });

  it("keeps the rename pencil visible without hover", () => {
    renderHeader(makeRun(), { rename: { can: true } });

    const pencil = screen.getByRole("button", { name: "Rename run" });
    expect(pencil.className).toContain("opacity-60");
    expect(pencil.className).not.toContain("group-hover");
  });
});

describe("RunUsageSummary", () => {
  it("always identifies cost, input tokens, and output tokens", () => {
    render(<RunUsageSummary costUsd={0.12345} inputTokens={12_345} outputTokens={678} />);

    const usage = screen.getByLabelText("Run usage");
    expect(within(usage).getByTitle("Cost").textContent).toBe("Cost$0.1235");
    expect(within(usage).getByTitle("Input tokens").textContent).toBe("In12.3k");
    expect(within(usage).getByTitle("Output tokens").textContent).toBe("Out678");
  });

  it("preserves unknown usage from default protobuf token values", () => {
    const tokens = resolveRunUsageTokens(0n, 0n, null);
    render(<RunUsageSummary costUsd={null} {...tokens} />);

    const usage = screen.getByLabelText("Run usage");
    expect(within(usage).getByTitle("Cost").textContent).toBe("Cost$—");
    expect(within(usage).getByTitle("Input tokens").textContent).toBe("In—");
    expect(within(usage).getByTitle("Output tokens").textContent).toBe("Out—");
  });

  it("displays explicit zero usage from trace telemetry, muted", () => {
    const tokens = resolveRunUsageTokens(0n, 0n, {
      hasUsage: true,
      inputTokens: 0,
      outputTokens: 0,
    });
    render(<RunUsageSummary costUsd={0} {...tokens} />);

    const usage = screen.getByLabelText("Run usage");
    const cost = within(usage).getByTitle("Cost");
    expect(cost.textContent).toBe("Cost$0.0000");
    expect(cost.querySelector("dd")?.className).toContain("text-muted-foreground");
    expect(within(usage).getByTitle("Input tokens").textContent).toBe("In0");
    expect(within(usage).getByTitle("Output tokens").textContent).toBe("Out0");
  });
});
