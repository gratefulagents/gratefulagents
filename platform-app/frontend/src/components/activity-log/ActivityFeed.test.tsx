import { create } from "@bufbuild/protobuf";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { ActivityEntrySchema } from "@/rpc/platform/service_pb";
import { ActivityFeed } from "./ActivityFeed";

afterEach(cleanup);

describe("execution attribution", () => {
  it("keeps the switch visible without expanding work details", () => {
    const entries = ["main", "architect"].map((agentName, index) => create(ActivityEntrySchema, {
      agentName, type: "assistant_text", message: `${agentName} response`, timestampUnix: BigInt(index),
    }));
    const { rerender } = render(<ActivityFeed entries={entries} isLive />);
    expect(screen.getByLabelText("Executing agent: architect").textContent).toContain("Execution switched");
    expect(screen.getByText("architect response")).toBeTruthy();
    rerender(<ActivityFeed entries={[...entries]} isLive={false} />);
    expect(screen.getAllByText("Execution switched")).toHaveLength(1);
  });
});
