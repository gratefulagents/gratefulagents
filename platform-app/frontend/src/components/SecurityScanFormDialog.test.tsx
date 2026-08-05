import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { SecurityScanFormDialog } from "@/components/SecurityScanFormDialog";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    createSecurityScan: vi.fn().mockResolvedValue({ namespace: "ns", name: "nightly-scan" }),
    updateSecurityScan: vi.fn(),
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
  render(<SecurityScanFormDialog trigger={<button>New scan</button>} defaultOpen />);
}

describe("SecurityScanFormDialog", () => {
  it("shows only the essentials up front; advanced pieces stay collapsed", () => {
    renderDialog();

    // Essentials.
    expect(screen.getByLabelText(/Repository URL/)).toBeTruthy();
    expect(screen.getByLabelText(/Schedule/)).toBeTruthy();
    expect(screen.getByLabelText(/Name/)).toBeTruthy();

    // Collapsed option rows render as summaries…
    expect(screen.getByRole("button", { name: /Workflow tasks/ })).toBeTruthy();
    expect(screen.getByText("Default workflow")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Reporting/ })).toBeTruthy();
    expect(screen.getByText("min low · dedupe on")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Scope/ })).toBeTruthy();
    expect(screen.getByText("Whole repository")).toBeTruthy();

    // …and their fields stay out of the DOM until expanded.
    expect(screen.queryByLabelText("Minimum severity")).toBeNull();
    expect(screen.queryByLabelText("Focus")).toBeNull();
  });

  it("expands an option row in place", () => {
    renderDialog();

    fireEvent.click(screen.getByRole("button", { name: /Reporting/ }));

    expect(screen.getByLabelText("Minimum severity")).toBeTruthy();
    expect(screen.getByLabelText("Fail on severity")).toBeTruthy();
    expect(screen.getByLabelText("Parallelism")).toBeTruthy();
  });

  it("creates a scan from a repository URL with saved credentials by default", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: "@daily" }));

    const form = document.querySelector("form");
    expect(form).toBeTruthy();
    fireEvent.submit(form as HTMLFormElement);

    await waitFor(() => {
      expect(client.createSecurityScan).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.spec?.repoUrl).toBe("https://github.com/acme/payments.git");
    expect(request.spec?.schedule).toBe("@daily");
    expect(request.spec?.workflow).toEqual([]);
    expect(request.spec?.dedupe?.enabled).toBe(true);
    expect(request.useSavedCredentials).toBe(true);
    expect(request.policies?.configureRuntimeProfile).toBe(true);
  });

  it("submits configured severity and scope fields", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Reporting/ }));
    fireEvent.change(screen.getByLabelText("Minimum severity"), { target: { value: "medium" } });
    fireEvent.change(screen.getByLabelText("Fail on severity"), { target: { value: "high" } });
    fireEvent.click(screen.getByRole("button", { name: /Scope/ }));
    fireEvent.change(screen.getByLabelText("Exclude paths"), {
      target: { value: "vendor/**, testdata/**" },
    });

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    await waitFor(() => {
      expect(client.createSecurityScan).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.spec?.minSeverity).toBe("medium");
    expect(request.spec?.failOnSeverity).toBe("high");
    expect(request.spec?.scope?.excludePaths).toEqual(["vendor/**", "testdata/**"]);
  });

  it("keeps validation errors inline when the repository URL is missing", async () => {
    renderDialog();

    const form = document.querySelector("form");
    fireEvent.submit(form as HTMLFormElement);

    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(client.createSecurityScan).not.toHaveBeenCalled();
  });
});
