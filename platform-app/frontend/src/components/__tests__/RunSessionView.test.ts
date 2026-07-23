import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import { messageForQuickAction } from "@/components/quickActions";
import {
  bucketActivityByMessage,
  findLatestPlanPresentation,
  planContentForPresentationGroup,
} from "@/components/run-session/helpers";
import { ActivityEntrySchema } from "@/rpc/platform/service_pb";

describe("messageForQuickAction", () => {
  it("routes approve actions through the structured action channel", () => {
    expect(messageForQuickAction({ id: "approve" })).toBe("__action:approve");
  });

  it("routes request-changes actions through the structured action channel", () => {
    expect(messageForQuickAction({ id: "request_changes" })).toBe("__action:request_changes");
  });

  it("routes mode-switch actions through the structured action channel", () => {
    expect(messageForQuickAction({ id: "approve_execute" })).toBe("__action:approve_execute");
  });
});

describe("plan presentation activity groups", () => {
  it("associates the durable plan only with the latest presentation's group", () => {
    const oldPresentation = create(ActivityEntrySchema, {
      type: "tool_use",
      tool: "present_plan",
      toolUseId: "old-plan",
    });
    const unrelated = create(ActivityEntrySchema, {
      type: "assistant_text",
      message: "Plan revised",
    });
    const latestPresentation = create(ActivityEntrySchema, {
      type: "tool_use",
      tool: "present_plan",
      toolUseId: "latest-plan",
    });
    const allEntries = [oldPresentation, unrelated, latestPresentation];
    const latest = findLatestPlanPresentation(allEntries);

    expect(latest).toBe(latestPresentation);
    expect(
      planContentForPresentationGroup(
        [oldPresentation],
        latest,
        "## Current plan",
      ),
    ).toBeUndefined();
    expect(
      planContentForPresentationGroup(
        [unrelated, latestPresentation],
        latest,
        "## Current plan",
      ),
    ).toBe("## Current plan");
  });
});

describe("bucketActivityByMessage", () => {
  function e(ts: number, taskId = "", type = "subagent_progress") {
    return create(ActivityEntrySchema, { timestampUnix: BigInt(ts), taskId, type });
  }

  it("keeps a task's whole lifecycle in the segment where it started", () => {
    // Task starts before the user's 2nd message but completes after it.
    const entries = [
      e(10, "t1"), // started (before msg at 20)
      e(30, "t1", "subagent_completed"), // completed (after msg at 20)
      e(31), // unrelated later entry
    ];
    const { segments, trailing } = bucketActivityByMessage(entries, [5n, 20n, 40n]);
    expect(segments[0]).toHaveLength(0);
    expect(segments[1].map((x) => x.type)).toEqual(["subagent_progress", "subagent_completed"]);
    expect(segments[2]).toHaveLength(1);
    expect(trailing).toHaveLength(0);
  });

  it("puts entries after the last message into trailing, anchored by task start", () => {
    const entries = [
      e(50, "live-task"),
      e(90, "live-task", "subagent_completed"),
      e(60), // plain entry between messages... after last message here
    ];
    const { segments, trailing } = bucketActivityByMessage(entries, [5n, 20n]);
    expect(segments[0]).toHaveLength(0);
    expect(segments[1]).toHaveLength(0);
    expect(trailing).toHaveLength(3);
  });

  it("slices task-less entries by their own timestamps", () => {
    const entries = [e(1), e(15), e(25)];
    const { segments, trailing } = bucketActivityByMessage(entries, [10n, 20n]);
    expect(segments[0]).toHaveLength(1);
    expect(segments[1]).toHaveLength(1);
    expect(trailing).toHaveLength(1);
  });
});
