import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ApiKeyVerificationNote, useApiKeyVerification } from "@/hooks/useApiKeyVerification";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    listAvailableModels: vi.fn(),
  },
}));

function Harness({ namespace, providers }: { namespace: string; providers: string[] }) {
  const { verifications, verify } = useApiKeyVerification();
  return (
    <div>
      <button onClick={() => verify(namespace, providers)}>verify-now</button>
      {providers.map((p) => (
        <ApiKeyVerificationNote key={p} provider={p} state={verifications[p]} />
      ))}
    </div>
  );
}

afterEach(() => {
  cleanup();
  vi.mocked(client.listAvailableModels).mockReset();
});

describe("useApiKeyVerification", () => {
  it("shows a transient verifying state, then the model count and an example", async () => {
    let resolve!: (value: { models: string[] }) => void;
    vi.mocked(client.listAvailableModels).mockReturnValue(
      new Promise((r) => {
        resolve = r;
      }) as never,
    );

    render(<Harness namespace="user-alice" providers={["xai"]} />);
    expect(screen.queryByText(/Verifying/)).toBeNull();

    fireEvent.click(screen.getByText("verify-now"));
    await screen.findByText("Verifying xAI key…");
    expect(client.listAvailableModels).toHaveBeenCalledWith({
      namespace: "user-alice",
      provider: "xai",
      authMode: "api-key",
    });

    resolve({ models: ["grok-4", "grok-3"] });
    await screen.findByText("✓ xAI key verified — 2 models available (e.g. grok-4)");
    expect(screen.queryByText("Verifying xAI key…")).toBeNull();
  });

  it("uses singular copy for a single model", async () => {
    vi.mocked(client.listAvailableModels).mockResolvedValue({
      models: ["claude-opus-4-6"],
    } as never);

    render(<Harness namespace="user-alice" providers={["anthropic"]} />);
    fireEvent.click(screen.getByText("verify-now"));
    await screen.findByText("✓ Anthropic key verified — 1 model available (e.g. claude-opus-4-6)");
  });

  it("shows an advisory warning when the probe fails", async () => {
    vi.mocked(client.listAvailableModels).mockRejectedValue(new Error("401 invalid key"));

    render(<Harness namespace="user-alice" providers={["openrouter"]} />);
    fireEvent.click(screen.getByText("verify-now"));
    const note = await screen.findByText(
      "OpenRouter key saved, but verification failed: 401 invalid key. Check the key and try again.",
    );
    expect(note.getAttribute("role")).toBe("status");
  });

  it("probes every provider passed in one call", async () => {
    vi.mocked(client.listAvailableModels).mockImplementation((request) => {
      const { provider } = request as { provider: string };
      return Promise.resolve(
        provider === "anthropic" ? { models: ["claude-opus-4-6"] } : { models: [] },
      ) as never;
    });

    render(<Harness namespace="user-alice" providers={["anthropic", "openai"]} />);
    fireEvent.click(screen.getByText("verify-now"));

    await screen.findByText("✓ Anthropic key verified — 1 model available (e.g. claude-opus-4-6)");
    await screen.findByText("✓ OpenAI key verified — 0 models available");
    await waitFor(() => {
      expect(client.listAvailableModels).toHaveBeenCalledTimes(2);
    });
    expect(client.listAvailableModels).toHaveBeenCalledWith({
      namespace: "user-alice",
      provider: "openai",
      authMode: "api-key",
    });
  });
});
