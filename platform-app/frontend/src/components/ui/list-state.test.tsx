import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useEffect, useRef } from "react";

import { ListState } from "@/components/ui/list-state";

afterEach(cleanup);

/** Counts mounts so tests can detect an unwanted unmount/remount cycle. */
function makeProbe() {
  const counter = { mounts: 0 };
  function Probe() {
    const mounted = useRef(false);
    useEffect(() => {
      if (!mounted.current) {
        mounted.current = true;
        counter.mounts += 1;
      }
    }, []);
    return <div data-testid="content">content</div>;
  }
  return { Probe, counter };
}

describe("ListState", () => {
  it("keeps children mounted when a transient error appears and clears", () => {
    const { Probe, counter } = makeProbe();
    const view = (error: string | null) => (
      <ListState loading={false} error={error} empty={false}>
        <Probe />
      </ListState>
    );

    const { rerender } = render(view(null));
    expect(counter.mounts).toBe(1);

    // Stream drops: banner appears, but the page content must not remount —
    // remounting destroys open dialogs, form edits, and scroll position.
    rerender(view("stream AgentRuns: connection lost"));
    expect(screen.getByRole("alert").textContent).toContain("Connection trouble");
    expect(screen.getByRole("button", { name: "Refresh" })).toBeTruthy();
    expect(screen.getByTestId("content")).toBeTruthy();
    expect(counter.mounts).toBe(1);

    // Stream recovers: banner goes away, content still not remounted.
    rerender(view(null));
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByTestId("content")).toBeTruthy();
    expect(counter.mounts).toBe(1);
  });

  it("calls the supplied refresh action from the connection banner", () => {
    let refreshes = 0;
    render(
      <ListState
        loading={false}
        error="[unknown] error decoding response body"
        empty={false}
        onRetry={() => { refreshes += 1; }}
      >
        <div>content</div>
      </ListState>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(refreshes).toBe(1);
  });

  it("shows the full error card only when there is no data", () => {
    render(
      <ListState loading={false} error="boom" empty>
        <div data-testid="content">content</div>
      </ListState>,
    );
    expect(screen.getByRole("alert").textContent).toContain("Couldn't load");
    expect(screen.queryByTestId("content")).toBeNull();
  });

  it("shows the empty state when empty without error", () => {
    render(
      <ListState loading={false} error={null} empty emptyTitle="Nothing here">
        <div data-testid="content">content</div>
      </ListState>,
    );
    expect(screen.getByText("Nothing here")).toBeTruthy();
    expect(screen.queryByTestId("content")).toBeNull();
  });
});
