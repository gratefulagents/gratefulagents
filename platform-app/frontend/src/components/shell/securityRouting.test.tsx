import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";

import { RedirectPreservingQuery, SectionNotFound } from "@/components/shell/routing";
import { SecurityNav } from "@/components/SecurityNav";
import { Breadcrumbs } from "@/components/shell/Breadcrumbs";

afterEach(() => {
  cleanup();
});

function Landed() {
  const location = useLocation();
  return <span data-testid="landed">{location.pathname + location.search}</span>;
}

function renderRoutes(initial: string) {
  render(
    <MemoryRouter initialEntries={[initial]}>
      <Routes>
        <Route path="/security" element={<Landed />} />
        <Route path="/security/runs" element={<Landed />} />
        <Route path="/security/configs" element={<Landed />} />
        <Route path="/security/library" element={<Landed />} />
        <Route path="/security/overview" element={<RedirectPreservingQuery to="/security" />} />
        <Route path="/security/scans" element={<RedirectPreservingQuery to="/security/runs" />} />
        <Route path="/security/findings" element={<RedirectPreservingQuery to="/security/runs" />} />
        <Route
          path="/security/configs/:name"
          element={<RedirectPreservingQuery to="/security/configs" params={{ q: ":name" }} />}
        />
        <Route
          path="/security/library/:tab"
          element={<RedirectPreservingQuery to="/security/library" params={{ tab: ":tab" }} />}
        />
        <Route
          path="/security/*"
          element={(
            <SectionNotFound
              section="security"
              links={[{ to: "/security", label: "Security overview" }]}
            />
          )}
        />
      </Routes>
    </MemoryRouter>,
  );
}

describe("security redirects", () => {
  it("redirects legacy paths to their current route", () => {
    renderRoutes("/security/scans");
    expect(screen.getByTestId("landed").textContent).toBe("/security/runs");
  });

  it("keeps the query string across a redirect", () => {
    renderRoutes("/security/scans?severity=critical&status=open");
    const landed = screen.getByTestId("landed").textContent!;
    expect(landed.startsWith("/security/runs?")).toBe(true);
    expect(landed).toContain("severity=critical");
    expect(landed).toContain("status=open");
  });

  it("turns a namespace-less config link into a pre-filtered list", () => {
    renderRoutes("/security/configs/nightly-webapp");
    expect(screen.getByTestId("landed").textContent).toBe(
      "/security/configs?q=nightly-webapp",
    );
  });

  it("turns a library sub-path into the tab query param", () => {
    renderRoutes("/security/library/policy-packs");
    expect(screen.getByTestId("landed").textContent).toBe(
      "/security/library?tab=policy-packs",
    );
  });

  it("shows a recoverable not-found page for unknown security paths", () => {
    renderRoutes("/security/nope/deeper/still");
    expect(screen.getByRole("alert")).toBeTruthy();
    expect(screen.getByText("/security/nope/deeper/still")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Security overview" })).toBeTruthy();
  });
});

describe("SecurityNav", () => {
  function renderNav(path: string) {
    render(
      <MemoryRouter initialEntries={[path]}>
        <SecurityNav />
      </MemoryRouter>,
    );
  }

  it("marks the overview current only on the exact path", () => {
    renderNav("/security");
    expect(screen.getByRole("link", { name: /Overview/ }).getAttribute("aria-current")).toBe("page");
  });

  it("keeps Scan runs current on a scan detail page", () => {
    renderNav("/security/demo/nightly-webapp-1");
    expect(screen.getByRole("link", { name: /Scan runs/ }).getAttribute("aria-current")).toBe("page");
    expect(screen.getByRole("link", { name: /Overview/ }).getAttribute("aria-current")).toBeNull();
  });

  it("keeps Configurations current on a config detail page", () => {
    renderNav("/security/configs/demo/nightly-webapp");
    expect(screen.getByRole("link", { name: /Configurations/ }).getAttribute("aria-current")).toBe("page");
  });

  it("renders optional counts", () => {
    render(
      <MemoryRouter initialEntries={["/security/runs"]}>
        <SecurityNav counts={{ "/security/runs": 12 }} />
      </MemoryRouter>,
    );
    expect(screen.getByText("12")).toBeTruthy();
  });
});

describe("security breadcrumbs", () => {
  function renderCrumbs(path: string) {
    render(
      <MemoryRouter initialEntries={[path]}>
        <Breadcrumbs />
      </MemoryRouter>,
    );
  }

  it("builds Security / Scan runs / run for a scan detail page", () => {
    renderCrumbs("/security/demo/nightly-webapp-1");
    expect(screen.getByRole("link", { name: "Security" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Scan runs" })).toBeTruthy();
    expect(screen.getByText("nightly-webapp-1")).toBeTruthy();
  });

  it("adds a Finding crumb and links the run back with its filters", () => {
    renderCrumbs("/security/demo/nightly-webapp-1/findings/abc?severity=high");
    const runLink = screen.getByRole("link", { name: "nightly-webapp-1" });
    expect(runLink.getAttribute("href")).toBe(
      "/security/demo/nightly-webapp-1?severity=high",
    );
    expect(screen.getByText("Finding")).toBeTruthy();
  });

  it("labels the configurations section", () => {
    renderCrumbs("/security/configs/demo/nightly-webapp");
    expect(screen.getByRole("link", { name: "Configurations" })).toBeTruthy();
    expect(screen.getByText("nightly-webapp")).toBeTruthy();
  });
});
