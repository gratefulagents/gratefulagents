import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { create } from "@bufbuild/protobuf";

import { AgentRunTable } from "@/components/AgentRunTable";
import { AgentRunSchema } from "@/rpc/platform/service_pb";

afterEach(cleanup);

function run(name: string, standingRunRole = "") {
  return create(AgentRunSchema, {
    namespace: "team",
    name,
    phase: "Running",
    standingRunRole,
  });
}

describe("AgentRunTable standing runs", () => {
  it("badges standing supervisor runs and leaves ordinary runs unbadged", () => {
    render(
      <MemoryRouter>
        <AgentRunTable
          runs={[run("acme-payments-maintainer", "maintainer"), run("fix-login-bug")]}
          loading={false}
          emptyMessage="No runs."
        />
      </MemoryRouter>,
    );

    expect(screen.getByText("maintainer")).toBeTruthy();
    expect(
      screen.getByRole("link", { name: "acme-payments-maintainer" }).getAttribute("href"),
    ).toBe("/runs/team/acme-payments-maintainer");
    expect(screen.getByRole("link", { name: "fix-login-bug" })).toBeTruthy();
    expect(screen.getAllByText("maintainer")).toHaveLength(1);
  });
});
