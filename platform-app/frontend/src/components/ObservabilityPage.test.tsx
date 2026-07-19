import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ObservabilityPage } from "@/components/ObservabilityPage";

vi.mock("@/components/ObservabilityOverview", () => ({
  ObservabilityOverview: () => <section>Historical metrics</section>,
}));

afterEach(cleanup);

describe("ObservabilityPage", () => {
  it("presents observability as its own page", () => {
    render(<ObservabilityPage />);

    expect(screen.getByRole("heading", { level: 1, name: "Observability" })).toBeTruthy();
    expect(screen.getByText("Track historical usage, cost, and reliability across visible runs.")).toBeTruthy();
    expect(screen.getByText("Historical metrics")).toBeTruthy();
  });
});
