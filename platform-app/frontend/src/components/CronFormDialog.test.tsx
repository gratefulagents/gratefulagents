import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { CronFormDialog } from "@/components/CronFormDialog";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    createCron: vi.fn().mockResolvedValue({ namespace: "ns", name: "nightly" }),
    updateCron: vi.fn(),
    listMyCredentials: vi.fn().mockResolvedValue({
      namespace: "ns",
      anthropicApiKeyPresent: false,
      openaiApiKeyPresent: false,
      anthropicOauthPresent: false,
      openaiOauthPresent: false,
      copilotOauthPresent: false,
      githubTokenPresent: false,
    }),
    listAvailableModels: vi.fn().mockResolvedValue({ models: [] }),
    listMCPServers: vi.fn().mockResolvedValue({ servers: [] }),
    listSkills: vi.fn().mockResolvedValue({ skills: [] }),
    listRuntimeImages: vi.fn().mockResolvedValue({ images: [] }),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function renderDialog() {
  render(<CronFormDialog trigger={<button>New cron</button>} defaultOpen />);
}

describe("CronFormDialog", () => {
  it("shows only the essentials up front; defaults stay collapsed as summaries", () => {
    renderDialog();

    // Essentials.
    expect(screen.getByLabelText(/Prompt/)).toBeTruthy();
    expect(screen.getByLabelText(/Schedule/)).toBeTruthy();
    expect(screen.getByLabelText(/Name/)).toBeTruthy();

    // Collapsed option rows render as summaries…
    expect(screen.getByRole("button", { name: /Repository/ })).toBeTruthy();
    expect(screen.getByText("No repository")).toBeTruthy();
    expect(screen.getByText("Anthropic · saved credentials")).toBeTruthy();
    expect(screen.getByText("Default image")).toBeTruthy();

    // …and their fields stay out of the DOM until expanded.
    expect(screen.queryByLabelText("Repository URL")).toBeNull();
    expect(screen.queryByLabelText("Custom instructions")).toBeNull();
  });

  it("expands an option row in place", () => {
    renderDialog();

    fireEvent.click(screen.getByRole("button", { name: /Repository/ }));

    expect(screen.getByLabelText("Repository URL")).toBeTruthy();
    expect(screen.getByLabelText("Base branch")).toBeTruthy();
  });

  it("creates a cron from the essentials with saved credentials by default", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText(/Prompt/), {
      target: { value: "Summarize open PRs" },
    });
    fireEvent.click(screen.getByRole("button", { name: "@daily" }));

    const form = document.querySelector("form");
    expect(form).toBeTruthy();
    fireEvent.submit(form as HTMLFormElement);

    await waitFor(() => {
      expect(client.createCron).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createCron).mock.calls[0][0];
    expect(request.schedule).toBe("@daily");
    expect(request.prompt).toBe("Summarize open PRs");
    expect(request.useSavedCredentials).toBe(true);
    expect(request.concurrencyPolicy).toBe("Forbid");
    expect(request.policies?.configureRuntimeProfile).toBe(true);
  });

  it("keeps validation errors inline", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText(/Prompt/), { target: { value: "x" } });
    const form = document.querySelector("form");
    fireEvent.submit(form as HTMLFormElement);

    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(client.createCron).not.toHaveBeenCalled();
  });
});
