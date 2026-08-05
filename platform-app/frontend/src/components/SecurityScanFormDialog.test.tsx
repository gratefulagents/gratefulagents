import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import { SecurityScanFormDialog } from "@/components/SecurityScanFormDialog";
import { client } from "@/lib/client";
import {
  SecurityScanConfigSchema,
  SecurityScanConfigSpecSchema,
} from "@/rpc/platform/service_pb";

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
    listSecurityWorkflows: vi.fn().mockResolvedValue({
      workflows: [{ name: "payments-workflow", tasks: [{ name: "a" }], usageCount: 0, referencingScans: [] }],
    }),
    listSecurityRankers: vi.fn().mockResolvedValue({
      rankers: [{ name: "payments-ranker", rules: ["r"], usageCount: 0, referencingScans: [] }],
    }),
    listSecurityPostScripts: vi.fn().mockResolvedValue({
      postScripts: [{ name: "write-poc", prompt: "p", usageCount: 0, referencingScans: [] }],
    }),
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

describe("SecurityScanFormDialog duplicate mode", () => {
  function sourceConfig() {
    return create(SecurityScanConfigSchema, {
      namespace: "user-alice",
      name: "nightly",
      spec: create(SecurityScanConfigSpecSchema, {
        repoUrl: "https://github.com/acme/payments.git",
        schedule: "@daily",
        minSeverity: "medium",
      }),
    });
  }

  function renderDuplicateDialog() {
    render(
      <SecurityScanFormDialog
        duplicateFrom={sourceConfig()}
        trigger={<button>Duplicate</button>}
        defaultOpen
      />,
    );
  }

  it("pre-fills from the source config but requires a new name", async () => {
    renderDuplicateDialog();

    expect(screen.getByText("Duplicate nightly")).toBeTruthy();
    expect((screen.getByLabelText(/Repository URL/) as HTMLInputElement).value).toBe(
      "https://github.com/acme/payments.git",
    );
    expect((screen.getByLabelText(/Schedule/) as HTMLInputElement).value).toBe("@daily");
    expect((screen.getByLabelText(/Name/) as HTMLInputElement).value).toBe("");

    // Submitting without a name is a review-step validation error, not a create.
    fireEvent.submit(document.querySelector("form") as HTMLFormElement);
    expect((await screen.findByRole("alert")).textContent).toContain(
      "Give the duplicated scan a new name.",
    );
    expect(client.createSecurityScan).not.toHaveBeenCalled();
  });

  it("creates a new scan with the copied spec and never updates the source", async () => {
    renderDuplicateDialog();

    fireEvent.change(screen.getByLabelText(/Name/), { target: { value: "nightly-copy" } });
    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    await waitFor(() => {
      expect(client.createSecurityScan).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.name).toBe("nightly-copy");
    expect(request.spec?.repoUrl).toBe("https://github.com/acme/payments.git");
    expect(request.spec?.minSeverity).toBe("medium");
    expect(client.updateSecurityScan).not.toHaveBeenCalled();
  });

  it("surfaces a name collision instead of overwriting the existing scan", async () => {
    vi.mocked(client.createSecurityScan).mockRejectedValueOnce(
      new Error("[already_exists] SecurityScan user-alice/nightly already exists"),
    );
    renderDuplicateDialog();

    fireEvent.change(screen.getByLabelText(/Name/), { target: { value: "nightly" } });
    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    expect((await screen.findByRole("alert")).textContent).toContain("already exists");
    expect(client.updateSecurityScan).not.toHaveBeenCalled();
  });

  it("selects a library workflow and refs, sending workflowRef and appended refs", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });

    fireEvent.click(screen.getByRole("button", { name: /Workflow tasks/ }));
    const workflowSelect = (await screen.findByLabelText("Library workflow")) as HTMLSelectElement;
    fireEvent.change(workflowSelect, { target: { value: "payments-workflow" } });
    // Snapshot semantics are explained next to the picker.
    expect(screen.getAllByText(/snapshotted/i).length).toBeGreaterThan(0);
    // Inline task editing is disabled while a library workflow is selected.
    expect(screen.queryByRole("button", { name: "Add workflow task" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: /Rankers & post-scripts/ }));
    fireEvent.click(await screen.findByRole("checkbox", { name: /payments-ranker/ }));
    fireEvent.click(screen.getByRole("checkbox", { name: /write-poc/ }));

    const form = document.querySelector("form");
    fireEvent.submit(form as HTMLFormElement);

    await waitFor(() => {
      expect(client.createSecurityScan).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.spec?.workflowRef).toBe("payments-workflow");
    expect(request.spec?.workflow).toEqual([]);
    expect(request.spec?.rankerRefs).toEqual(["payments-ranker"]);
    expect(request.spec?.postScriptRefs).toEqual(["write-poc"]);
  });
});

describe("SecurityScanFormDialog repository events and notifications", () => {
  it("submits repository event triggers, checks, and notification rules", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });

    fireEvent.click(screen.getByRole("button", { name: /Repository events/ }));
    fireEvent.change(screen.getByLabelText("GitHub repository connection"), {
      target: { value: "widget-repo" },
    });
    fireEvent.click(screen.getByRole("switch", { name: "Scan pull requests" }));
    fireEvent.click(screen.getByRole("switch", { name: "Scan pushes" }));
    fireEvent.change(screen.getByLabelText("Push branch filters"), {
      target: { value: "main, release/*" },
    });
    fireEvent.click(screen.getByRole("switch", { name: "Diff scope" }));
    fireEvent.click(screen.getByRole("switch", { name: "Publish GitHub checks" }));
    fireEvent.click(screen.getByRole("switch", { name: "Upload SARIF to code scanning" }));

    fireEvent.click(screen.getByRole("button", { name: /Notifications/ }));
    fireEvent.click(screen.getByRole("button", { name: "Add notification rule" }));
    fireEvent.change(screen.getByLabelText(/Rule name/), { target: { value: "critical-alerts" } });
    fireEvent.change(screen.getByLabelText("Minimum severity"), { target: { value: "critical" } });
    fireEvent.change(screen.getByLabelText("Slack webhook secret"), {
      target: { value: "slack-webhook" },
    });

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);
    await waitFor(() => {
      expect(client.createSecurityScan).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.spec?.triggers?.repositoryRef).toBe("widget-repo");
    expect(request.spec?.triggers?.onPullRequest).toBe(true);
    expect(request.spec?.triggers?.onPush).toBe(true);
    expect(request.spec?.triggers?.branches).toEqual(["main", "release/*"]);
    expect(request.spec?.triggers?.diffScope).toBe(true);
    expect(request.spec?.triggers?.allowForks).toBe(false);
    expect(request.spec?.checks?.enabled).toBe(true);
    expect(request.spec?.checks?.uploadSarif).toBe(true);
    expect(request.spec?.checks?.includeFindingSummaries).toBe(false);
    expect(request.spec?.notifications).toHaveLength(1);
    expect(request.spec?.notifications?.[0].name).toBe("critical-alerts");
    expect(request.spec?.notifications?.[0].minSeverity).toBe("critical");
    expect(request.spec?.notifications?.[0].slackWebhookSecretRef).toBe("slack-webhook");
  });

  it("explains fork and credential safety in the section help text", () => {
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: /Repository events/ }));
    expect(screen.getByText(/the scan run itself never receives them/)).toBeTruthy();
    expect(screen.getByText(/GitHub credential is stripped/)).toBeTruthy();
  });

  it("blocks event triggers without a repository connection", async () => {
    renderDialog();
    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Repository events/ }));
    fireEvent.click(screen.getByRole("switch", { name: "Scan pull requests" }));

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toMatch(/repository reference/i);
    expect(client.createSecurityScan).not.toHaveBeenCalled();
  });

  it("blocks notification rules without a channel", async () => {
    renderDialog();
    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Notifications/ }));
    fireEvent.click(screen.getByRole("button", { name: "Add notification rule" }));
    fireEvent.change(screen.getByLabelText(/Rule name/), { target: { value: "no-channel" } });

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toMatch(/at least one channel/i);
    expect(client.createSecurityScan).not.toHaveBeenCalled();
  });

  it("requires Linear key and team together", async () => {
    renderDialog();
    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Notifications/ }));
    fireEvent.click(screen.getByRole("button", { name: "Add notification rule" }));
    fireEvent.change(screen.getByLabelText(/Rule name/), { target: { value: "linear-only" } });
    fireEvent.change(screen.getByLabelText("Linear API key secret"), {
      target: { value: "linear-key" },
    });

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toMatch(/both the API key secret and the team ID/i);
    expect(client.createSecurityScan).not.toHaveBeenCalled();
  });
});
