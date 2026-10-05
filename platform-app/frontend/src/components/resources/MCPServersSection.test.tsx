import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { MCPServersSection } from "@/components/resources/MCPServersSection";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    listMCPServers: vi.fn(),
    upsertMCPServer: vi.fn(),
    deleteMCPServer: vi.fn(),
  },
}));

vi.mock("@/hooks/useMySecretInventory", () => ({
  useMySecretInventory: () => ({
    secrets: [],
    integrations: [{ name: "grafana", keys: ["token"] }],
    loading: false,
    error: null,
    reload: vi.fn(),
  }),
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const listMCPServers = vi.mocked(client.listMCPServers);
const upsertMCPServer = vi.mocked(client.upsertMCPServer);

describe("MCPServersSection", () => {
  it("creates a server with command, args, env, and network access", async () => {
    listMCPServers.mockResolvedValue({ servers: [] } as never);
    upsertMCPServer.mockResolvedValue({} as never);

    render(<MCPServersSection />);
    expect(await screen.findByText("No MCP servers yet")).toBeTruthy();

    fireEvent.click(screen.getAllByRole("button", { name: "New server" })[0]);
    fireEvent.change(screen.getByPlaceholderText("my-tool"), { target: { value: "grafana" } });
    fireEvent.change(screen.getByLabelText(/^Command/), { target: { value: "uvx" } });
    fireEvent.change(screen.getByLabelText("Arguments"), { target: { value: "mcp-grafana==0.17.2 --disable-oncall" } });

    fireEvent.click(screen.getByRole("button", { name: "Add variable" }));
    fireEvent.change(screen.getByLabelText("Variable 1"), { target: { value: "GRAFANA_URL" } });
    fireEvent.change(screen.getByLabelText("Value 1"), { target: { value: "https://grafana.local" } });

    fireEvent.click(screen.getByRole("button", { name: /^Access/ }));
    fireEvent.click(screen.getByRole("switch", { name: "Allow network access" }));

    fireEvent.click(screen.getByRole("button", { name: "Create server" }));

    await waitFor(() =>
      expect(upsertMCPServer).toHaveBeenCalledWith({
        name: "grafana",
        version: "",
        description: "",
        command: "uvx",
        args: ["mcp-grafana==0.17.2", "--disable-oncall"],
        env: { GRAFANA_URL: "https://grafana.local" },
        allowEnv: [],
        secretEnv: [],
        trustReadOnlyHint: true,
        allowNetwork: true,
      }),
    );
    await waitFor(() => expect(listMCPServers).toHaveBeenCalledTimes(2));
  });

  it("flags duplicate environment variable names and blocks saving", async () => {
    listMCPServers.mockResolvedValue({
      servers: [
        {
          name: "grafana",
          version: "1.0.0",
          description: "Dashboards",
          command: "uvx",
          args: ["mcp-grafana"],
          env: { A: "1" },
          allowEnv: [],
          secretEnv: [{ name: "GRAFANA_TOKEN", secretName: "usercred-grafana", secretKey: "token", required: false }],
          trustReadOnlyHint: true,
          allowNetwork: true,
        },
      ],
    } as never);

    render(<MCPServersSection />);
    expect(await screen.findByRole("button", { name: "grafana" })).toBeTruthy();
    expect(screen.getByText("network")).toBeTruthy();
    expect(screen.getByText("1 secret")).toBeTruthy();
    expect(screen.getByText("uvx mcp-grafana")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Edit grafana" }));
    fireEvent.click(screen.getByRole("button", { name: /^Credentials/ }));
    fireEvent.click(screen.getByRole("button", { name: "Add variable" }));
    fireEvent.change(screen.getByLabelText("Variable 2"), { target: { value: "A" } });
    fireEvent.change(screen.getByLabelText("Value 2"), { target: { value: "2" } });

    expect(await screen.findByText("Duplicate variable: A")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Save changes" }) as HTMLButtonElement).disabled).toBe(true);
    expect(upsertMCPServer).not.toHaveBeenCalled();
  });
});
