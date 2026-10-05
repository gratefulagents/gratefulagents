import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { AddGitHubRepositoryDialog } from "@/components/AddGitHubRepositoryDialog";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    getGitHubAppConfig: vi.fn().mockResolvedValue({
      configured: false,
      appSlug: "",
      installUrl: "",
    }),
    listMyCredentials: vi.fn().mockResolvedValue({
      namespace: "user-alice",
      anthropicApiKeyPresent: true,
      openaiApiKeyPresent: false,
      anthropicOauthPresent: false,
      openaiOauthPresent: false,
      copilotOauthPresent: false,
      githubTokenPresent: true,
    }),
    listGitHubAppInstallations: vi.fn().mockResolvedValue({ installations: [] }),
    listGitHubAppInstallationRepositories: vi.fn().mockResolvedValue({ repositories: [] }),
    listAvailableModels: vi.fn().mockResolvedValue({ models: [] }),
    createGitHubRepositoryFromInstallation: vi.fn().mockResolvedValue({}),
    createGitHubRepositoryFromToken: vi.fn().mockResolvedValue({}),
  },
}));

// The real Select is a Base UI popup that is hard to drive in jsdom; replace
// it with a flat list of buttons that call onValueChange directly.
vi.mock("@/components/ui/select", async () => {
  const React = await import("react");
  const Ctx = React.createContext<{ onValueChange?: (value: string) => void }>({});
  type MockSelectProps = {
    value?: string;
    onValueChange?: (value: string) => void;
    disabled?: boolean;
    placeholder?: string;
    children?: import("react").ReactNode;
    [key: string]: unknown;
  };
  return {
    Select: ({ onValueChange, children }: MockSelectProps) => (
      <Ctx.Provider value={{ onValueChange }}>{children}</Ctx.Provider>
    ),
    SelectTrigger: ({ children, ...props }: MockSelectProps) => <div {...props}>{children}</div>,
    SelectValue: ({ placeholder }: MockSelectProps) => <span>{placeholder}</span>,
    SelectContent: ({ children }: MockSelectProps) => <div>{children}</div>,
    SelectItem: ({ value, disabled, children }: MockSelectProps) => {
      const ctx = React.useContext(Ctx);
      return (
        <button
          type="button"
          disabled={disabled}
          onClick={() => value !== undefined && ctx.onValueChange?.(value)}
        >
          {children}
        </button>
      );
    },
  };
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

async function openDialog() {
  render(<AddGitHubRepositoryDialog />);
  fireEvent.click(screen.getByRole("button", { name: /Add repository/ }));
  await waitFor(() => {
    expect(client.listMyCredentials).toHaveBeenCalled();
  });
}

async function fillModel(model: string) {
  fireEvent.click(screen.getByRole("button", { name: /Model/ }));
  fireEvent.change(await screen.findByLabelText(/^Model/), { target: { value: model } });
}

function submitForm() {
  const form = document.querySelector("form");
  expect(form).toBeTruthy();
  fireEvent.submit(form as HTMLFormElement);
}

describe("AddGitHubRepositoryDialog", () => {
  it("token method parses a GitHub URL and submits with saved credentials by default", async () => {
    await openDialog();

    fireEvent.change(await screen.findByLabelText(/Repository/), {
      target: { value: "https://github.com/acme/payments" },
    });
    await fillModel("claude-sonnet-4-5");
    submitForm();

    await waitFor(() => {
      expect(client.createGitHubRepositoryFromToken).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createGitHubRepositoryFromToken).mock.calls[0][0];
    expect(request.owner).toBe("acme");
    expect(request.repo).toBe("payments");
    expect(request.namespace).toBe("");
    expect(request.useSavedCredentials).toBe(true);
    expect(request.githubToken).toBe("");
    expect(request.policies?.configureRuntimeProfile).toBe(true);
    expect(client.createGitHubRepositoryFromInstallation).not.toHaveBeenCalled();
  });

  it("blocks token submit when no saved GitHub token and the token input is empty", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue({
      namespace: "user-alice",
      anthropicApiKeyPresent: true,
      openaiApiKeyPresent: false,
      anthropicOauthPresent: false,
      openaiOauthPresent: false,
      copilotOauthPresent: false,
      githubTokenPresent: false,
    } as never);
    await openDialog();

    fireEvent.change(await screen.findByLabelText(/Repository/), {
      target: { value: "acme/payments" },
    });
    await fillModel("claude-sonnet-4-5");
    submitForm();

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/No saved GitHub token/);
    expect(client.createGitHubRepositoryFromToken).not.toHaveBeenCalled();
  });

  it("app method sends useSavedCredentials on the installation request", async () => {
    vi.mocked(client.getGitHubAppConfig).mockResolvedValue({
      configured: true,
      appSlug: "my-app",
      installUrl: "https://github.com/apps/my-app/installations/new",
    } as never);
    vi.mocked(client.listGitHubAppInstallations).mockResolvedValue({
      installations: [{ id: 42n, accountLogin: "acme", accountType: "Organization" }],
    } as never);
    vi.mocked(client.listGitHubAppInstallationRepositories).mockResolvedValue({
      repositories: [
        {
          fullName: "acme/payments",
          owner: "acme",
          name: "payments",
          defaultBranch: "main",
          private: true,
          alreadyOnboarded: false,
        },
      ],
    } as never);
    await openDialog();

    fireEvent.click(await screen.findByRole("button", { name: /acme \(Organization\)/ }));
    fireEvent.click(await screen.findByRole("button", { name: /acme\/payments/ }));
    await fillModel("claude-sonnet-4-5");
    submitForm();

    await waitFor(() => {
      expect(client.createGitHubRepositoryFromInstallation).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createGitHubRepositoryFromInstallation).mock.calls[0][0];
    expect(request.installationId).toBe(42n);
    expect(request.owner).toBe("acme");
    expect(request.repo).toBe("payments");
    expect(request.useSavedCredentials).toBe(true);
    expect(client.createGitHubRepositoryFromToken).not.toHaveBeenCalled();
  });
});
