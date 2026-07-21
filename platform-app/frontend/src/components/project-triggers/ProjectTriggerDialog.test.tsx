import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ProjectTriggerDialog } from "@/components/project-triggers/ProjectTriggerDialog";
import type { ProjectConnection, ProjectTrigger } from "@/components/project-triggers/types";

vi.mock("@/lib/native", () => ({
  openExternal: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("@/components/ui/select", async () => {
  const React = await import("react");
  const Ctx = React.createContext<{ onValueChange?: (value: string) => void }>({});
  type P = { value?: string; onValueChange?: (v: string) => void; children?: React.ReactNode; placeholder?: string; disabled?: boolean; [k: string]: unknown };
  return {
    Select: ({ onValueChange, children }: P) => <Ctx.Provider value={{ onValueChange }}>{children}</Ctx.Provider>,
    SelectTrigger: ({ children, ...props }: P) => <div {...props}>{children}</div>,
    SelectValue: ({ placeholder }: P) => <span>{placeholder}</span>,
    SelectContent: ({ children }: P) => <div>{children}</div>,
    SelectItem: ({ value, children }: P) => {
      const ctx = React.useContext(Ctx);
      return <button type="button" onClick={() => value !== undefined && ctx.onValueChange?.(value)}>{children}</button>;
    },
  };
});

const GITHUB_CONNECTION: ProjectConnection = {
  name: "my-github",
  type: "github",
  github: { tokenSecret: "some-secret" },
};

const SLACK_CONNECTION: ProjectConnection = {
  name: "my-slack",
  type: "slack",
  slack: { tokensSecret: "slack-secret" },
};

function renderDialog({
  trigger = undefined as ProjectTrigger | undefined,
  connections = [] as ProjectConnection[],
  onSave = vi.fn().mockResolvedValue(undefined),
  onManageConnections = vi.fn(),
} = {}) {
  const onOpenChange = vi.fn();
  render(
    <ProjectTriggerDialog
      trigger={trigger}
      open
      onOpenChange={onOpenChange}
      onSave={onSave}
      connections={connections}
      onManageConnections={onManageConnections}
    />,
  );
  return { onSave, onManageConnections, onOpenChange };
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("ProjectTriggerDialog", () => {
  it("shows type choice step on create", () => {
    renderDialog();
    expect(screen.getByText("New trigger")).toBeTruthy();
    // All four trigger type cards should appear
    const buttons = screen.getAllByRole("button");
    const githubCard = buttons.find((b) => b.textContent?.includes("GitHub") && b.textContent?.includes("React to issues"));
    const slackCard = buttons.find((b) => b.textContent?.includes("Slack") && b.textContent?.includes("channel"));
    const cronCard = buttons.find((b) => b.textContent?.includes("Scheduled") || b.textContent?.includes("schedule"));
    const linearCard = buttons.find((b) => b.textContent?.includes("Linear") && b.textContent?.includes("issue"));
    expect(githubCard).toBeTruthy();
    expect(slackCard).toBeTruthy();
    expect(cronCard).toBeTruthy();
    expect(linearCard).toBeTruthy();
  });

  it("selecting GitHub type shows the details step with connection select and repository field", () => {
    renderDialog({ connections: [GITHUB_CONNECTION] });
    const buttons = screen.getAllByRole("button");
    const githubCard = buttons.find((b) => b.textContent?.includes("GitHub") && b.textContent?.includes("React to issues"));
    fireEvent.click(githubCard!);
    expect(screen.getByLabelText("Repository")).toBeTruthy();
    expect(screen.getByLabelText("React to issue events")).toBeTruthy();
    expect(screen.getByLabelText("React to comment events")).toBeTruthy();
  });

  it("shows empty state with 'Add connection' when no matching connection exists", () => {
    renderDialog({ connections: [] });
    const buttons = screen.getAllByRole("button");
    const githubCard = buttons.find((b) => b.textContent?.includes("GitHub") && b.textContent?.includes("React to issues"));
    fireEvent.click(githubCard!);
    expect(screen.getByRole("button", { name: /Add connection/ })).toBeTruthy();
    expect(screen.getByText(/No GitHub connection yet/)).toBeTruthy();
  });

  it("clicking 'Add connection' in empty state calls onManageConnections", () => {
    const onManageConnections = vi.fn();
    renderDialog({ connections: [], onManageConnections });
    const buttons = screen.getAllByRole("button");
    const githubCard = buttons.find((b) => b.textContent?.includes("GitHub") && b.textContent?.includes("React to issues"));
    fireEvent.click(githubCard!);
    fireEvent.click(screen.getByRole("button", { name: /Add connection/ }));
    expect(onManageConnections).toHaveBeenCalledTimes(1);
  });

  it("selecting Slack type shows all trigger behavior fields", () => {
    renderDialog({ connections: [SLACK_CONNECTION] });
    const buttons = screen.getAllByRole("button");
    const slackCard = buttons.find((b) => b.textContent?.includes("Slack") && b.textContent?.includes("channel"));
    fireEvent.click(slackCard!);
    expect(screen.getByLabelText("Slack conversation ID")).toBeTruthy();
    expect(screen.getByLabelText("Allowed Slack commanders")).toBeTruthy();
    expect(screen.getByLabelText("Slack channel reply mode")).toBeTruthy();
    expect(screen.getByLabelText("Slack conversation memory minutes")).toBeTruthy();
  });

  it("selecting cron type shows preset chips that fill the schedule input", () => {
    renderDialog();
    const buttons = screen.getAllByRole("button");
    const cronCard = buttons.find((b) => b.textContent?.includes("Scheduled") || (b.textContent?.includes("cron") || b.textContent?.includes("schedule")));
    fireEvent.click(cronCard!);

    // Find preset chip "Every hour"
    const hourlyBtn = screen.getByRole("button", { name: /Preset: Every hour/ });
    expect(hourlyBtn).toBeTruthy();
    fireEvent.click(hourlyBtn);

    const scheduleInput = screen.getByLabelText("Cron schedule") as HTMLInputElement;
    expect(scheduleInput.value).toBe("0 * * * *");
  });

  it("cron: clicking a preset chip fills the schedule and shows human-readable label", () => {
    renderDialog();
    const buttons = screen.getAllByRole("button");
    const cronCard = buttons.find((b) => b.textContent?.includes("Scheduled") || b.textContent?.includes("recurring"));
    fireEvent.click(cronCard!);

    const dailyBtn = screen.getByRole("button", { name: /Preset: Daily 9 am/ });
    fireEvent.click(dailyBtn);

    const scheduleInput = screen.getByLabelText("Cron schedule") as HTMLInputElement;
    expect(scheduleInput.value).toBe("0 9 * * *");
    // The human-readable description should appear
    expect(screen.getByText("Daily at 09:00")).toBeTruthy();
  });

  it("shows validation error when trigger name is empty on submit", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    renderDialog({ connections: [GITHUB_CONNECTION], onSave });

    const buttons = screen.getAllByRole("button");
    const githubCard = buttons.find((b) => b.textContent?.includes("GitHub") && b.textContent?.includes("React to issues"));
    fireEvent.click(githubCard!);

    // Select connection
    fireEvent.click(screen.getByText("my-github"));

    // Don't fill name; submit
    const form = document.querySelector("form");
    fireEvent.submit(form!);

    await waitFor(() => {
      expect(screen.getByRole("alert")).toBeTruthy();
    });
    expect(screen.getByRole("alert").textContent).toMatch(/name/i);
    expect(onSave).not.toHaveBeenCalled();
  });

  it("shows validation error when no connection is selected (empty connections list)", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    // Use empty connections so auto-selection leaves connectionRef empty
    renderDialog({ connections: [], onSave });

    const buttons = screen.getAllByRole("button");
    const githubCard = buttons.find((b) => b.textContent?.includes("GitHub") && b.textContent?.includes("React to issues"));
    fireEvent.click(githubCard!);

    // Fill name only
    const nameInput = screen.getByLabelText("Trigger name");
    fireEvent.change(nameInput, { target: { value: "my-trigger" } });

    const form = document.querySelector("form");
    fireEvent.submit(form!);

    await waitFor(() => {
      expect(screen.getByRole("alert")).toBeTruthy();
    });
    expect(screen.getByRole("alert").textContent).toMatch(/connection/i);
    expect(onSave).not.toHaveBeenCalled();
  });

  it("edit mode jumps directly to the details step", () => {
    const existingTrigger: ProjectTrigger = {
      name: "existing-trigger",
      type: "github",
      github: { connectionRef: "my-github", owner: "acme", repo: "payments", issues: true, comments: false },
    };
    renderDialog({ trigger: existingTrigger, connections: [GITHUB_CONNECTION] });

    // Type choice description should not be visible
    expect(screen.queryByText(/Choose what will start an agent run/)).toBeNull();
    // The details form should be shown directly
    expect(screen.getByLabelText("Repository")).toBeTruthy();
  });

  it("creates a cron trigger with correct shape", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    renderDialog({ onSave });

    const buttons = screen.getAllByRole("button");
    const cronCard = buttons.find((b) => b.textContent?.includes("Scheduled") || b.textContent?.includes("recurring") || b.textContent?.includes("hourly"));
    fireEvent.click(cronCard!);

    fireEvent.click(screen.getByRole("button", { name: /Preset: Daily 9 am/ }));

    fireEvent.change(screen.getByLabelText("Prompt"), { target: { value: "Summarize open PRs" } });
    fireEvent.change(screen.getByLabelText("Trigger name"), { target: { value: "daily-summary" } });

    const form = document.querySelector("form");
    fireEvent.submit(form!);

    await waitFor(() => {
      expect(onSave).toHaveBeenCalledTimes(1);
    });
    const saved = onSave.mock.calls[0][0] as ProjectTrigger;
    expect(saved.type).toBe("cron");
    expect(saved.cron?.schedule).toBe("0 9 * * *");
    expect(saved.cron?.prompt).toBe("Summarize open PRs");
    expect(saved.name).toBe("daily-summary");
  });

  it("creates a GitHub trigger with issues checked", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    renderDialog({ connections: [GITHUB_CONNECTION], onSave });

    const buttons = screen.getAllByRole("button");
    const githubCard = buttons.find((b) => b.textContent?.includes("GitHub") && b.textContent?.includes("React to issues"));
    fireEvent.click(githubCard!);

    // Select connection by clicking the connection select item
    fireEvent.click(screen.getByText("my-github"));

    fireEvent.change(screen.getByLabelText("Repository"), { target: { value: "acme/payments" } });

    const issueCheckbox = screen.getByLabelText("React to issue events") as HTMLInputElement;
    if (!issueCheckbox.checked) fireEvent.click(issueCheckbox);

    fireEvent.change(screen.getByLabelText("Trigger name"), { target: { value: "gh-trigger" } });

    const form = document.querySelector("form");
    fireEvent.submit(form!);

    await waitFor(() => {
      expect(onSave).toHaveBeenCalledTimes(1);
    });
    const saved = onSave.mock.calls[0][0] as ProjectTrigger;
    expect(saved.type).toBe("github");
    expect(saved.github?.connectionRef).toBe("my-github");
    expect(saved.github?.owner).toBe("acme");
    expect(saved.github?.repo).toBe("payments");
    expect(saved.github?.issues).toBe(true);
  });

  it("creates a GitHub trigger with repository maintainer settings", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    renderDialog({ connections: [GITHUB_CONNECTION], onSave });

    const githubCard = screen.getAllByRole("button").find(
      (button) => button.textContent?.includes("GitHub") && button.textContent?.includes("React to issues"),
    );
    fireEvent.click(githubCard!);
    fireEvent.click(screen.getByText("my-github"));
    fireEvent.change(screen.getByLabelText("Repository"), { target: { value: "acme/payments" } });
    fireEvent.click(screen.getByLabelText("Enable repository maintainer"));
    fireEvent.change(screen.getByLabelText("Maintainer max concurrent dispatches"), { target: { value: "3" } });
    fireEvent.change(screen.getByLabelText("Maintainer max dispatches per day"), { target: { value: "12" } });
    fireEvent.change(screen.getByLabelText("Maintainer standup interval"), { target: { value: "6h" } });
    fireEvent.change(screen.getByLabelText("Maintainer mode"), { target: { value: "repository-maintainer" } });
    fireEvent.change(screen.getByLabelText("Maintainer model"), { target: { value: "gpt-5" } });
    fireEvent.click(screen.getByLabelText("Allow maintainer pull request merge"));
    fireEvent.change(screen.getByLabelText("Trigger name"), { target: { value: "gh-trigger" } });
    fireEvent.submit(document.querySelector("form")!);

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect((onSave.mock.calls[0][0] as ProjectTrigger).github).toMatchObject({
      maintainerEnabled: true,
      maintainerMaxConcurrentDispatches: 3,
      maintainerMaxDispatchesPerDay: 12,
      maintainerStandupInterval: "6h",
      maintainerModeRef: "repository-maintainer",
      maintainerModel: "gpt-5",
      maintainerAllowPrMerge: true,
    });
  });

  it("preserves GitHub repository maintainer settings while editing", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    const trigger: ProjectTrigger = {
      name: "issues",
      type: "github",
      github: {
        connectionRef: "my-github",
        owner: "acme",
        repo: "payments",
        issues: true,
        triggerKeyword: "@grateful",
        pollInterval: "2m",
        authAllowedUsers: ["maintainer"],
        authDenyUsers: ["blocked-user"],
        maintainerEnabled: true,
        maintainerMaxConcurrentDispatches: 4,
        maintainerMaxDispatchesPerDay: 15,
        maintainerStandupInterval: "8h",
        maintainerModeRef: "repository-maintainer",
        maintainerModel: "gpt-5-mini",
        maintainerAllowPrMerge: true,
      },
    };
    renderDialog({ trigger, connections: [GITHUB_CONNECTION], onSave });

    expect(screen.getByLabelText<HTMLInputElement>("Enable repository maintainer").checked).toBe(true);
    expect(screen.getByLabelText<HTMLInputElement>("Maintainer max concurrent dispatches").value).toBe("4");
    expect(screen.getByLabelText<HTMLInputElement>("Maintainer mode").value).toBe("repository-maintainer");
    fireEvent.submit(document.querySelector("form")!);

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect((onSave.mock.calls[0][0] as ProjectTrigger).github).toMatchObject({
      triggerKeyword: "@grateful",
      pollInterval: "2m",
      authAllowedUsers: ["maintainer"],
      authDenyUsers: ["blocked-user"],
      maintainerEnabled: true,
      maintainerMaxConcurrentDispatches: 4,
      maintainerMaxDispatchesPerDay: 15,
      maintainerStandupInterval: "8h",
      maintainerModeRef: "repository-maintainer",
      maintainerModel: "gpt-5-mini",
      maintainerAllowPrMerge: true,
    });
  });

  it("creates a Slack trigger with authorization, reply, and memory settings", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    renderDialog({ connections: [SLACK_CONNECTION], onSave });

    const slackCard = screen.getAllByRole("button").find(
      (button) => button.textContent?.includes("Slack") && button.textContent?.includes("channel"),
    );
    fireEvent.click(slackCard!);
    fireEvent.click(screen.getByText("my-slack"));
    fireEvent.change(screen.getByLabelText("Slack conversation ID"), { target: { value: "C0123ABC" } });
    fireEvent.change(screen.getByLabelText("Allowed Slack commanders"), {
      target: { value: "U01OWNER, U02HELPER, U02HELPER" },
    });
    fireEvent.click(screen.getByText("Post directly"));
    fireEvent.change(screen.getByLabelText("Slack conversation memory minutes"), {
      target: { value: "90" },
    });
    fireEvent.change(screen.getByLabelText("Trigger name"), { target: { value: "team-chat" } });
    fireEvent.submit(document.querySelector("form")!);

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    const saved = onSave.mock.calls[0][0] as ProjectTrigger;
    expect(saved.slack).toEqual({
      connectionRef: "my-slack",
      channel: "C0123ABC",
      channelReplyMode: "auto",
      commanders: ["U01OWNER", "U02HELPER"],
      sessionIdleMinutes: 90,
    });
  });

  it("preserves existing Slack behavior fields while editing", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    const trigger: ProjectTrigger = {
      name: "team-chat",
      type: "slack",
      slack: {
        connectionRef: "my-slack",
        channel: "C0123ABC",
        channelReplyMode: "auto",
        commanders: ["U02HELPER"],
        sessionIdleMinutes: 240,
      },
    };
    renderDialog({ trigger, connections: [SLACK_CONNECTION], onSave });

    expect((screen.getByLabelText("Slack conversation ID") as HTMLInputElement).value).toBe("C0123ABC");
    expect((screen.getByLabelText("Allowed Slack commanders") as HTMLInputElement).value).toBe("U02HELPER");
    expect((screen.getByLabelText("Slack conversation memory minutes") as HTMLInputElement).value).toBe("240");
    fireEvent.submit(document.querySelector("form")!);

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect((onSave.mock.calls[0][0] as ProjectTrigger).slack).toEqual(trigger.slack);
  });

  it("creates an unscoped Slack trigger when the conversation ID is left empty", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    renderDialog({ connections: [SLACK_CONNECTION], onSave });

    const slackCard = screen.getAllByRole("button").find(
      (button) => button.textContent?.includes("Slack") && button.textContent?.includes("channel"),
    );
    fireEvent.click(slackCard!);
    fireEvent.click(screen.getByText("my-slack"));
    fireEvent.change(screen.getByLabelText("Trigger name"), { target: { value: "team-chat" } });
    fireEvent.submit(document.querySelector("form")!);

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    const saved = onSave.mock.calls[0][0] as ProjectTrigger;
    expect(saved.slack?.channel).toBe("");
  });

  it("accepts a Slack channel name for server-side ID resolution", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    renderDialog({ connections: [SLACK_CONNECTION], onSave });

    const slackCard = screen.getAllByRole("button").find(
      (button) => button.textContent?.includes("Slack") && button.textContent?.includes("channel"),
    );
    fireEvent.click(slackCard!);
    fireEvent.click(screen.getByText("my-slack"));
    fireEvent.change(screen.getByLabelText("Slack conversation ID"), { target: { value: "#engineering" } });
    fireEvent.change(screen.getByLabelText("Trigger name"), { target: { value: "team-chat" } });
    fireEvent.submit(document.querySelector("form")!);

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    const saved = onSave.mock.calls[0][0] as ProjectTrigger;
    expect(saved.slack?.channel).toBe("#engineering");
  });

  it("Linear type shows empty state with onManageConnections when no Linear connection exists", () => {
    const onManageConnections = vi.fn();
    renderDialog({ connections: [GITHUB_CONNECTION], onManageConnections });

    const buttons = screen.getAllByRole("button");
    const linearCard = buttons.find((b) => b.textContent?.includes("Linear") && b.textContent?.includes("issue"));
    fireEvent.click(linearCard!);

    expect(screen.getByText(/No Linear connection yet/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /Add connection/ }));
    expect(onManageConnections).toHaveBeenCalledTimes(1);
  });
});
