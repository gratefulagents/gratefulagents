import { create } from "@bufbuild/protobuf";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { ActivityEntrySchema } from "@/rpc/platform/service_pb";
import { ActivityFeed } from "./ActivityFeed";

afterEach(cleanup);

function entry(agentName: string, message: string, timestampUnix: bigint) {
  return create(ActivityEntrySchema, { agentName, type: "assistant_text", message, timestampUnix });
}

describe("agent handoff dividers", () => {
  it("renders a handoff as a separator between the two agents' output", () => {
    render(
      <ActivityFeed
        entries={[entry("main", "main response", 0n), entry("architect", "architect response", 1n)]}
        isLive
      />,
    );
    const separators = screen.getAllByRole("separator");
    expect(separators).toHaveLength(1);
    expect(separators[0].getAttribute("aria-label")).toBe("Handoff from main to architect");
    // Both agents are shown as chips: the root orchestrator keeps its ROOT styling.
    expect(separators[0].textContent).toBe("ROOTarchitect");
    expect(screen.getByText("main response")).toBeTruthy();
    expect(screen.getByText("architect response")).toBeTruthy();
  });

  it("does not announce the root agent when nothing switched", () => {
    render(<ActivityFeed entries={[entry("main", "hello", 0n), entry("main", "again", 1n)]} isLive={false} />);
    expect(screen.queryByRole("separator")).toBeNull();
  });

  it("announces a non-root agent that opens the feed", () => {
    render(<ActivityFeed entries={[entry("architect", "hello", 0n)]} isLive={false} />);
    expect(screen.getByRole("separator").getAttribute("aria-label")).toBe("Continuing as architect");
  });
});
