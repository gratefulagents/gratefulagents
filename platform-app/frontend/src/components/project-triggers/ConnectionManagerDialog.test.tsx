import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ConnectionManagerDialog } from "@/components/project-triggers/ConnectionManagerDialog";
import type { ProjectConnection } from "@/components/project-triggers/types";

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

function renderDialog({
  connections = [] as ProjectConnection[],
  onCreate = vi.fn().mockResolvedValue(undefined),
  onUpdate = vi.fn().mockResolvedValue(undefined),
  onDelete = vi.fn().mockResolvedValue(undefined),
} = {}) {
  const onOpenChange = vi.fn();
  render(
    <ConnectionManagerDialog
      open
      onOpenChange={onOpenChange}
      connections={connections}
      onCreate={onCreate}
      onUpdate={onUpdate}
      onDelete={onDelete}
    />,
  );
  return { onCreate, onUpdate, onDelete, onOpenChange };
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("ConnectionManagerDialog", () => {
  it("shows the list view with a 'New connection' button on open", () => {
    renderDialog();
    expect(screen.getByText("Manage connections")).toBeTruthy();
    expect(screen.getByRole("button", { name: /New connection/ })).toBeTruthy();
  });

  it("clicking 'New connection' shows the provider selection step", () => {
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: /New connection/ }));
    expect(screen.getByText("GitHub")).toBeTruthy();
    expect(screen.getByText("Slack")).toBeTruthy();
    expect(screen.getByText("Linear")).toBeTruthy();
  });

  it("selecting GitHub provider shows the guided form", () => {
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: /New connection/ }));
    const cards = screen.getAllByRole("button");
    const githubCard = cards.find((b) => b.textContent?.includes("GitHub") && b.textContent?.includes("React to issues"));
    expect(githubCard).toBeTruthy();
    fireEvent.click(githubCard!);
    expect(screen.getByText("Personal access token")).toBeTruthy();
    expect(screen.getByLabelText("GitHub personal access token")).toBeTruthy();
  });

  it("selecting Slack provider shows the guided Slack form", () => {
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: /New connection/ }));
    const cards = screen.getAllByRole("button");
    const slackCard = cards.find((b) => b.textContent?.includes("Slack") && b.textContent?.includes("Chat with your agents"));
    expect(slackCard).toBeTruthy();
    fireEvent.click(slackCard!);
    expect(screen.getByLabelText("Slack bot token")).toBeTruthy();
    expect(screen.getByLabelText("Slack app token")).toBeTruthy();
  });

  it("selecting Linear provider shows the guided Linear form", () => {
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: /New connection/ }));
    const cards = screen.getAllByRole("button");
    const linearCard = cards.find((b) => b.textContent?.includes("Linear") && b.textContent?.includes("Turn Linear issues"));
    expect(linearCard).toBeTruthy();
    fireEvent.click(linearCard!);
    expect(screen.getByLabelText("Linear API key")).toBeTruthy();
  });

  it("submits a GitHub token connection and calls onCreate with {type:'github', github:{token:'…'}}", async () => {
    const onCreate = vi.fn().mockResolvedValue(undefined);
    renderDialog({ onCreate });

    fireEvent.click(screen.getByRole("button", { name: /New connection/ }));
    const cards = screen.getAllByRole("button");
    const githubCard = cards.find((b) => b.textContent?.includes("GitHub") && b.textContent?.includes("React to issues"));
    fireEvent.click(githubCard!);

    // Fill the token (using a generic placeholder value, not a real pattern)
    const tokenInput = screen.getByLabelText("GitHub personal access token");
    fireEvent.change(tokenInput, { target: { value: "test-github-credential-value" } });

    const nameInput = screen.getByLabelText("Connection name");
    fireEvent.change(nameInput, { target: { value: "github" } });

    const form = document.querySelector("form");
    expect(form).toBeTruthy();
    fireEvent.submit(form!);

    await waitFor(() => {
      expect(onCreate).toHaveBeenCalledTimes(1);
    });
    const connection = onCreate.mock.calls[0][0] as ProjectConnection;
    expect(connection.type).toBe("github");
    expect(connection.github?.token).toBe("test-github-credential-value");
    expect(connection.name).toBe("github");
  });

  it("shows validation error when Slack create is missing the second token", async () => {
    const onCreate = vi.fn().mockResolvedValue(undefined);
    renderDialog({ onCreate });

    fireEvent.click(screen.getByRole("button", { name: /New connection/ }));
    const cards = screen.getAllByRole("button");
    const slackCard = cards.find((b) => b.textContent?.includes("Slack") && b.textContent?.includes("Chat with your agents"));
    fireEvent.click(slackCard!);

    // Provide only the bot token, not the app token
    const botInput = screen.getByLabelText("Slack bot token");
    fireEvent.change(botInput, { target: { value: "only-bot-token-no-app-token" } });

    const nameInput = screen.getByLabelText("Connection name");
    fireEvent.change(nameInput, { target: { value: "slack" } });

    const form = document.querySelector("form");
    fireEvent.submit(form!);

    await waitFor(() => {
      expect(screen.getByRole("alert")).toBeTruthy();
    });
    expect(screen.getByRole("alert").textContent).toMatch(/xoxb-.*xapp-/);
    expect(onCreate).not.toHaveBeenCalled();
  });

  it("shows validation error for invalid connection name", async () => {
    const onCreate = vi.fn().mockResolvedValue(undefined);
    renderDialog({ onCreate });

    fireEvent.click(screen.getByRole("button", { name: /New connection/ }));
    const cards = screen.getAllByRole("button");
    const linearCard = cards.find((b) => b.textContent?.includes("Linear") && b.textContent?.includes("Turn Linear issues"));
    fireEvent.click(linearCard!);

    const nameInput = screen.getByLabelText("Connection name");
    fireEvent.change(nameInput, { target: { value: "My Connection!" } });

    const form = document.querySelector("form");
    fireEvent.submit(form!);

    await waitFor(() => {
      expect(screen.getByRole("alert")).toBeTruthy();
    });
    expect(screen.getByRole("alert").textContent).toMatch(/lowercase/);
    expect(onCreate).not.toHaveBeenCalled();
  });

  it("shows existing connections in the list", () => {
    const connections: ProjectConnection[] = [
      { name: "my-github", type: "github", github: { tokenSecret: "some-secret" } },
      { name: "my-slack", type: "slack", slack: { tokensSecret: "slack-secret" } },
    ];
    renderDialog({ connections });
    expect(screen.getByText("my-github")).toBeTruthy();
    expect(screen.getByText("my-slack")).toBeTruthy();
  });

  it("switching to GitHub App mode shows app-specific fields", () => {
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: /New connection/ }));
    const cards = screen.getAllByRole("button");
    const githubCard = cards.find((b) => b.textContent?.includes("GitHub") && b.textContent?.includes("React to issues"));
    fireEvent.click(githubCard!);

    fireEvent.click(screen.getByRole("button", { name: /GitHub App/i }));

    expect(screen.getByLabelText("GitHub App ID")).toBeTruthy();
    expect(screen.getByLabelText("GitHub App installation ID")).toBeTruthy();
    expect(screen.getByLabelText("GitHub App private key PEM")).toBeTruthy();
  });

  it("submits a Slack connection with both tokens provided", async () => {
    const onCreate = vi.fn().mockResolvedValue(undefined);
    renderDialog({ onCreate });

    fireEvent.click(screen.getByRole("button", { name: /New connection/ }));
    const cards = screen.getAllByRole("button");
    const slackCard = cards.find((b) => b.textContent?.includes("Slack") && b.textContent?.includes("Chat with your agents"));
    fireEvent.click(slackCard!);

    fireEvent.change(screen.getByLabelText("Slack bot token"), { target: { value: "bot-token-value" } });
    fireEvent.change(screen.getByLabelText("Slack app token"), { target: { value: "app-token-value" } });

    const nameInput = screen.getByLabelText("Connection name");
    fireEvent.change(nameInput, { target: { value: "slack" } });

    const form = document.querySelector("form");
    fireEvent.submit(form!);

    await waitFor(() => {
      expect(onCreate).toHaveBeenCalledTimes(1);
    });
    const connection = onCreate.mock.calls[0][0] as ProjectConnection;
    expect(connection.type).toBe("slack");
    expect(connection.slack?.botToken).toBe("bot-token-value");
    expect(connection.slack?.appToken).toBe("app-token-value");
  });
});
