import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router-dom";

import { useUrlFilters } from "@/hooks/useUrlFilters";

const SPEC = { q: "", severity: "all", status: "all", tab: "workflows" };

/**
 * The probe drives the hook through real UI events rather than exposing the
 * returned API to the test: mutating an outer binding during render is exactly
 * the impurity the react-hooks lint rules forbid, and clicking buttons is also
 * closer to how pages use these setters.
 */
function Probe() {
  const filters = useUrlFilters(SPEC);
  const location = useLocation();
  return (
    <div>
      <span data-testid="search">{location.search}</span>
      <span data-testid="values">{JSON.stringify(filters.values)}</span>
      <span data-testid="active">{filters.activeCount()}</span>
      <span data-testid="active-no-q">{filters.activeCount(["q"])}</span>
      <span data-testid="query-string">{filters.queryString}</span>
      <span data-testid="severity-active">{String(filters.isActive("severity"))}</span>
      <span data-testid="status-active">{String(filters.isActive("status"))}</span>
      <button type="button" onClick={() => filters.set("severity", "high")}>set severity high</button>
      <button type="button" onClick={() => filters.set("severity", "all")}>clear severity</button>
      <button type="button" onClick={() => filters.set("status", "open")}>set status open</button>
      <button
        type="button"
        onClick={() => filters.setMany({ severity: "critical", status: "open" })}
      >
        set many
      </button>
      <button type="button" onClick={() => filters.reset()}>reset</button>
    </div>
  );
}

afterEach(() => {
  cleanup();
});

function setup(initial = "/security/runs") {
  render(
    <MemoryRouter initialEntries={[initial]}>
      <Probe />
    </MemoryRouter>,
  );
}

const search = () => screen.getByTestId("search").textContent;
const click = (name: string) => fireEvent.click(screen.getByRole("button", { name }));

describe("useUrlFilters", () => {
  it("falls back to declared defaults when the URL is empty", () => {
    setup();
    expect(JSON.parse(screen.getByTestId("values").textContent!)).toEqual({
      q: "",
      severity: "all",
      status: "all",
      tab: "workflows",
    });
    expect(screen.getByTestId("active").textContent).toBe("0");
  });

  it("reads values from the query string", () => {
    setup("/security/runs?severity=critical&q=sql");
    expect(JSON.parse(screen.getByTestId("values").textContent!)).toMatchObject({
      severity: "critical",
      q: "sql",
      status: "all",
    });
    expect(screen.getByTestId("active").textContent).toBe("2");
  });

  it("writes non-default values and drops defaults from the URL", () => {
    setup();
    click("set severity high");
    expect(search()).toBe("?severity=high");
    click("clear severity");
    expect(search()).toBe("");
  });

  it("preserves unrelated query params", () => {
    setup("/security/runs?selected=finding-1");
    click("set status open");
    expect(search()).toContain("selected=finding-1");
    expect(search()).toContain("status=open");
  });

  it("sets several keys in one navigation", () => {
    setup();
    click("set many");
    expect(search()).toContain("severity=critical");
    expect(search()).toContain("status=open");
  });

  it("reset clears declared keys but keeps foreign ones", () => {
    setup("/security/library?tab=rankers&q=abc&selected=x");
    click("reset");
    expect(search()).toBe("?selected=x");
  });

  it("exposes a query string for carrying filters into detail links", () => {
    setup("/security/runs?severity=high");
    expect(screen.getByTestId("query-string").textContent).toBe("?severity=high");
    expect(screen.getByTestId("severity-active").textContent).toBe("true");
    expect(screen.getByTestId("status-active").textContent).toBe("false");
  });

  it("ignores listed keys when counting active filters", () => {
    setup("/security/runs?q=sql&severity=high");
    expect(screen.getByTestId("active").textContent).toBe("2");
    expect(screen.getByTestId("active-no-q").textContent).toBe("1");
  });
});
