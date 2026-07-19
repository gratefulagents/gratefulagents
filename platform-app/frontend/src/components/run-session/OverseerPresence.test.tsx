import type { AnchorHTMLAttributes, ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import { AgentRunSchema, type AgentRun } from "@/rpc/platform/service_pb";
import { OverseerPresence } from "./OverseerPresence";

vi.mock("react-router-dom", () => ({
  Link: ({
    to,
    children,
    ...props
  }: AnchorHTMLAttributes<HTMLAnchorElement> & { to: string; children?: ReactNode }) => (
    <a href={to} {...props}>{children}</a>
  ),
}));

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

function matchMedia(initiallyReduced: boolean) {
  let reduced = initiallyReduced;
  const listeners = new Set<() => void>();
  const media = {
    get matches() { return reduced; },
    media: "(prefers-reduced-motion: reduce)",
    onchange: null,
    addEventListener: (_event: string, listener: () => void) => listeners.add(listener),
    removeEventListener: (_event: string, listener: () => void) => listeners.delete(listener),
    addListener: (listener: () => void) => listeners.add(listener),
    removeListener: (listener: () => void) => listeners.delete(listener),
    dispatchEvent: vi.fn(),
  };
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: vi.fn().mockReturnValue(media),
  });
  return {
    setReduced(value: boolean) {
      reduced = value;
      listeners.forEach((listener) => listener());
    },
  };
}

function run(values: {
  phase?: string;
  overseerDetaching?: boolean;
  overseerSummary?: AgentRun["overseerSummary"];
} = {}): AgentRun {
  return create(AgentRunSchema, {
    namespace: "demo",
    name: "animated-run",
    phase: values.phase ?? "Running",
    overseer: {
      modeRefName: "overseer",
      authority: "enforce",
      intervalMinutes: 10,
      maxInterventions: 5,
    },
    overseerSummary: values.overseerSummary ?? {
      runName: "animated-run-overseer",
      state: "active",
      checkpointsHandled: 4n,
      interventionsUsed: 1,
      lastVerdict: "all_clear",
      lastVerdictAtUnix: 10n,
    },
    overseerDetaching: values.overseerDetaching,
  });
}

function iris(): SVGSVGElement {
  const svg = document.querySelector<SVGSVGElement>(".overseer-iris");
  expect(svg).not.toBeNull();
  return svg!;
}

function irisAperture(svg: SVGSVGElement): number {
  const d = svg.querySelector(".overseer-iris-eye path")?.getAttribute("d") ?? "";
  const match = /Q16 ([\d.]+)/.exec(d);
  expect(match).not.toBeNull();
  return 16 - Number(match![1]);
}

describe("OverseerPresence", () => {
  it("only appears for a live run with an attached overseer", () => {
    const withoutOverseer = create(AgentRunSchema, { phase: "Running" });
    const { rerender } = render(<OverseerPresence run={withoutOverseer} />);
    expect(screen.queryByRole("status")).toBeNull();

    rerender(<OverseerPresence run={run()} />);
    expect(screen.getByRole("status", { name: /Overseer active/i })).toBeTruthy();

    rerender(<OverseerPresence run={run({ phase: "Succeeded" })} />);
    expect(screen.queryByRole("status")).toBeNull();

    rerender(<OverseerPresence run={run({ overseerDetaching: true })} />);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("renders the iris mark with visible non-motion status", () => {
    matchMedia(false);
    render(<OverseerPresence run={run()} />);

    expect(screen.getByRole("status", { name: /Overseer active.*Guarding this run · checks every 10m/i })).toBeTruthy();
    expect(screen.getByText("Active")).toBeTruthy();
    const svg = iris();
    expect(svg.dataset.irisState).toBe("active");
    expect(svg.querySelector(".overseer-iris-pupil circle")).not.toBeNull();
    expect(document.querySelector(".overseer-presence-control")?.getAttribute("data-motion")).toBe("playing");
  });

  it("links the mark to the standing overseer chat", () => {
    matchMedia(false);
    render(<OverseerPresence run={run()} />);

    expect(screen.getByRole("link", { name: /Open overseer chat/i }).getAttribute("href"))
      .toBe("/runs/demo/animated-run-overseer");
  });

  it("opens the eye wide while checking a checkpoint", () => {
    matchMedia(false);
    const { rerender } = render(<OverseerPresence run={run()} />);
    const activeAperture = irisAperture(iris());

    rerender(
      <OverseerPresence
        run={run({ overseerSummary: { ...run().overseerSummary!, state: "checking" } })}
      />,
    );

    expect(screen.getByRole("status", { name: /Overseer checking.*Reviewing the latest checkpoint/i })).toBeTruthy();
    expect(document.querySelector(".overseer-presence-control")?.getAttribute("data-state")).toBe("checking");
    const svg = iris();
    expect(svg.dataset.irisState).toBe("checking");
    expect(irisAperture(svg)).toBeGreaterThan(activeAperture);
  });

  it.each([
    ["observing", "Observing", "Watching without intervening"],
    ["capped", "Cap reached", "Monitoring with interventions paused"],
    ["degraded", "Degraded", "Supervision is operating fail-open"],
    ["escalated", "Escalated", "This run needs attention"],
  ])("maps the %s lifecycle state to visible status", (state, label, copy) => {
    matchMedia(false);
    render(
      <OverseerPresence
        run={run({ overseerSummary: { ...run().overseerSummary!, state } })}
      />,
    );

    expect(screen.getByText(label)).toBeTruthy();
    expect(screen.getByRole("status", { name: new RegExp(`${copy} · checks every 10m`, "i") })).toBeTruthy();
    expect(iris().dataset.irisState).toBe(state);
  });

  it("narrows the eye and underlines it when the intervention cap is reached", () => {
    matchMedia(false);
    const { rerender } = render(<OverseerPresence run={run()} />);
    const activeAperture = irisAperture(iris());

    rerender(
      <OverseerPresence
        run={run({ overseerSummary: { ...run().overseerSummary!, state: "capped" } })}
      />,
    );

    const svg = iris();
    expect(irisAperture(svg)).toBeLessThan(activeAperture);
    expect(svg.querySelector(".overseer-iris-cap")).not.toBeNull();
    expect(svg.querySelector(".overseer-iris-rays")).toBeNull();
  });

  it("snaps the eye wide with alert rays when escalated", () => {
    matchMedia(false);
    render(
      <OverseerPresence
        run={run({ overseerSummary: { ...run().overseerSummary!, state: "escalated" } })}
      />,
    );

    const svg = iris();
    expect(irisAperture(svg)).toBe(11);
    expect(svg.querySelector(".overseer-iris-rays")).not.toBeNull();
    expect(svg.querySelector(".overseer-iris-cap")).toBeNull();
  });

  it("shows the backend reason when supervision is degraded", () => {
    matchMedia(false);
    const reason = "Overseer episode 2 completed without a valid verdict; supervision is fail-open.";
    render(
      <OverseerPresence
        run={run({
          overseerSummary: {
            ...run().overseerSummary!,
            state: "degraded",
            lastSummary: reason,
          },
        })}
      />,
    );

    expect(screen.getByRole("status", { name: new RegExp(reason, "i") })).toBeTruthy();
    expect(screen.getByRole("link", { name: /Open overseer chat/i }).getAttribute("title")).toContain(reason);
  });

  it("keeps a degraded status visible before the overseer run is created", () => {
    matchMedia(false);
    const reason = "Configured overseer mode could not be resolved.";
    render(
      <OverseerPresence
        run={run({
          overseerSummary: {
            ...run().overseerSummary!,
            runName: "",
            state: "degraded",
            lastSummary: reason,
          },
        })}
      />,
    );

    expect(screen.queryByRole("link")).toBeNull();
    const statusControl = screen.getByRole("button", { name: /Overseer chat unavailable — Degraded/i });
    expect(statusControl.getAttribute("aria-disabled")).toBe("true");
    expect(statusControl.getAttribute("title")).toContain(reason);
  });

  it.each(["cancelled", "detaching", "unavailable"])("hides the inactive %s lifecycle state", (state) => {
    matchMedia(false);
    render(
      <OverseerPresence
        run={run({ overseerSummary: { ...run().overseerSummary!, state } })}
      />,
    );

    expect(screen.queryByRole("status")).toBeNull();
  });

  it("keeps the status visible but marks motion paused for reduced motion", () => {
    matchMedia(true);
    render(<OverseerPresence run={run()} />);

    expect(screen.getByRole("status", { name: /Overseer active/i })).toBeTruthy();
    expect(document.querySelector(".overseer-presence-control")?.getAttribute("data-motion")).toBe("paused");
    expect(iris().dataset.irisState).toBe("active");
  });

  it("re-enables motion when the system motion preference changes", () => {
    const media = matchMedia(true);
    render(<OverseerPresence run={run()} />);
    expect(document.querySelector(".overseer-presence-control")?.getAttribute("data-motion")).toBe("paused");

    act(() => media.setReduced(false));
    expect(document.querySelector(".overseer-presence-control")?.getAttribute("data-motion")).toBe("playing");
  });

  it("remounts the iris on state change so the settle beat replays", () => {
    matchMedia(false);
    const { rerender } = render(<OverseerPresence run={run()} />);
    const first = iris();

    rerender(
      <OverseerPresence
        run={run({
          overseerSummary: {
            ...run().overseerSummary!,
            lastVerdict: "steer",
            lastVerdictAtUnix: 11n,
          },
        })}
      />,
    );
    expect(iris()).toBe(first);

    rerender(
      <OverseerPresence
        run={run({ overseerSummary: { ...run().overseerSummary!, state: "checking" } })}
      />,
    );
    expect(iris()).not.toBe(first);
  });

  it("briefly applies semantic feedback when a new verdict arrives", () => {
    vi.useFakeTimers();
    matchMedia(false);
    const { rerender } = render(<OverseerPresence run={run()} />);

    rerender(
      <OverseerPresence
        run={run({
          overseerSummary: {
            ...run().overseerSummary!,
            lastVerdict: "steer",
            lastVerdictAtUnix: 11n,
          },
        })}
      />,
    );

    const control = document.querySelector(".overseer-presence-control");
    const firstRipple = document.querySelector(".overseer-verdict-ripple");
    const firstStatus = screen.getByRole("status", { name: /Last verdict: Guidance sent/i });
    expect(control?.getAttribute("data-feedback")).toBe("warning");
    expect(firstStatus).toBeTruthy();

    rerender(
      <OverseerPresence
        run={run({
          overseerSummary: {
            ...run().overseerSummary!,
            lastVerdict: "steer",
            lastVerdictAtUnix: 12n,
          },
        })}
      />,
    );
    expect(document.querySelector(".overseer-verdict-ripple")).not.toBe(firstRipple);
    expect(screen.getByRole("status", { name: /Last verdict: Guidance sent/i })).not.toBe(firstStatus);

    act(() => vi.advanceTimersByTime(1400));
    expect(control?.getAttribute("data-feedback")).toBe("none");
  });
});
