import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { RuntimeProfilesSection } from "@/components/resources/RuntimeProfilesSection";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    listRuntimeProfiles: vi.fn(),
    createRuntimeProfile: vi.fn(),
    updateRuntimeProfile: vi.fn(),
    deleteRuntimeProfile: vi.fn(),
  },
}));

const listRuntimeProfiles = vi.mocked(client.listRuntimeProfiles);
const createRuntimeProfile = vi.mocked(client.createRuntimeProfile);
const updateRuntimeProfile = vi.mocked(client.updateRuntimeProfile);

const existing = {
  namespace: "ns",
  name: "locked-down",
  permissionMode: "read-only",
  gitRemoteWrites: "disabled",
  egressMode: "disabled",
  defaultTimeout: "45m0s",
  persistWorkspace: true,
  workspaceSize: "20Gi",
  enablePrivateProcfs: false,
  sandboxTemplateRef: "",
  runtimeClassName: "gvisor",
  warmPoolRef: "",
  commandPath: [],
  commandPathPrepend: [],
  commandPathAppend: [],
  extraReadOnlyPaths: [],
  extraWritablePaths: ["/cache/go"],
  commandEnv: { LANG: "C.UTF-8" },
  resourceRequests: { cpu: "250m" },
  resourceLimits: {},
  resourceClaims: [],
  maxConcurrentRuns: 3,
  perNamespaceMaxConcurrentRuns: 0,
  staleRunTimeout: "",
  replaceSpec: false,
};

beforeEach(() => {
  createRuntimeProfile.mockResolvedValue({} as never);
  updateRuntimeProfile.mockResolvedValue({} as never);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("RuntimeProfilesSection", () => {
  it("creates a profile from structured fields", async () => {
    listRuntimeProfiles.mockResolvedValue({ namespace: "ns", profiles: [] } as never);
    render(<RuntimeProfilesSection />);

    fireEvent.click((await screen.findAllByRole("button", { name: "New profile" }))[0]);
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: "ci-sandbox" } });
    fireEvent.click(within(dialog).getByRole("radio", { name: /Disabled/ }));

    fireEvent.click(within(dialog).getByRole("button", { name: /^Resources/ }));
    fireEvent.click(within(dialog).getAllByRole("button", { name: "+ cpu" })[0]);
    fireEvent.change(within(dialog).getByLabelText("Value 1"), { target: { value: "500m" } });

    fireEvent.click(within(dialog).getByRole("button", { name: /^Command sandbox/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Add variable" }));
    const variableInput = within(dialog).getByLabelText("Variable 1");
    fireEvent.change(variableInput, { target: { value: "GOFLAGS" } });
    // Scope to the env row so the resource request's value input is left alone.
    const envRow = variableInput.parentElement as HTMLElement;
    fireEvent.change(within(envRow).getByLabelText("Value 1"), { target: { value: "-mod=mod" } });

    const submit = within(dialog).getByRole("button", { name: "Create profile" });
    expect(submit.hasAttribute("disabled")).toBe(false);
    fireEvent.click(submit);

    await waitFor(() => expect(createRuntimeProfile).toHaveBeenCalledTimes(1));
    expect(createRuntimeProfile).toHaveBeenCalledWith({
      profile: expect.objectContaining({
        name: "ci-sandbox",
        permissionMode: "workspace-write",
        gitRemoteWrites: "enabled",
        egressMode: "disabled",
        resourceRequests: { cpu: "500m" },
        commandEnv: { GOFLAGS: "-mod=mod" },
        replaceSpec: true,
      }),
    });
  });

  it("flags an invalid default timeout and keeps the form unsaveable", async () => {
    listRuntimeProfiles.mockResolvedValue({ namespace: "ns", profiles: [] } as never);
    render(<RuntimeProfilesSection />);

    fireEvent.click((await screen.findAllByRole("button", { name: "New profile" }))[0]);
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: "slow" } });
    fireEvent.change(within(dialog).getByLabelText("Default timeout"), { target: { value: "soon" } });

    expect(await within(dialog).findByText(/must be a Go duration/)).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: "Create profile" }).hasAttribute("disabled")).toBe(true);

    fireEvent.click(within(dialog).getByRole("button", { name: "1h" }));
    await waitFor(() =>
      expect(within(dialog).getByRole("button", { name: "Create profile" }).hasAttribute("disabled")).toBe(false),
    );
    expect(createRuntimeProfile).not.toHaveBeenCalled();
  });

  it("hydrates an existing profile and updates it in place", async () => {
    listRuntimeProfiles.mockResolvedValue({ namespace: "ns", profiles: [existing] } as never);
    render(<RuntimeProfilesSection />);

    expect(await screen.findByText("locked-down")).toBeTruthy();
    expect(screen.getByText("read-only")).toBeTruthy();
    expect(screen.getByText("disabled egress")).toBeTruthy();
    expect(screen.getByText("git pushes off")).toBeTruthy();
    expect(screen.getByText(/persistent workspace \(20Gi\)/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Edit locked-down" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("radio", { name: /Read-only/ }).getAttribute("aria-checked")).toBe("true");
    expect((within(dialog).getByLabelText(/^Name/) as HTMLInputElement).disabled).toBe(true);
    expect((within(dialog).getByLabelText("Default timeout") as HTMLInputElement).value).toBe("45m0s");

    // Nothing changed yet, so saving is disabled.
    expect(within(dialog).getByRole("button", { name: "Save changes" }).hasAttribute("disabled")).toBe(true);

    fireEvent.click(within(dialog).getByRole("radio", { name: /Workspace write/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(updateRuntimeProfile).toHaveBeenCalledTimes(1));
    expect(updateRuntimeProfile).toHaveBeenCalledWith({
      profile: expect.objectContaining({
        name: "locked-down",
        permissionMode: "workspace-write",
        gitRemoteWrites: "disabled",
        egressMode: "disabled",
        persistWorkspace: true,
        workspaceSize: "20Gi",
        runtimeClassName: "gvisor",
        extraWritablePaths: ["/cache/go"],
        commandEnv: { LANG: "C.UTF-8" },
        resourceRequests: { cpu: "250m" },
        maxConcurrentRuns: 3,
        replaceSpec: true,
      }),
    });
    expect(createRuntimeProfile).not.toHaveBeenCalled();
  });
});
