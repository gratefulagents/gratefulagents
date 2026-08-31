import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { AnthropicOAuthConnect } from "@/components/AnthropicOAuthConnect";

const startAnthropicOAuth = vi.fn();
const completeAnthropicOAuth = vi.fn();
const updateMyCredentials = vi.fn();

vi.mock("@/lib/anthropic-oauth", () => ({
  startAnthropicOAuth: (...args: unknown[]) => startAnthropicOAuth(...args),
  completeAnthropicOAuth: (...args: unknown[]) => completeAnthropicOAuth(...args),
}));

vi.mock("@/lib/client", () => ({
  client: {
    updateMyCredentials: (...args: unknown[]) => updateMyCredentials(...args),
  },
}));

vi.mock("@/lib/native", () => ({
  copyText: vi.fn().mockResolvedValue(true),
  openExternal: vi.fn().mockResolvedValue(undefined),
}));

const savedCredentials = {
  namespace: "user-alice",
  anthropicApiKeyPresent: false,
  openaiApiKeyPresent: false,
  openrouterApiKeyPresent: false,
  xaiApiKeyPresent: false,
  anthropicOauthPresent: true,
  openaiOauthPresent: false,
  copilotOauthPresent: false,
  githubTokenPresent: false,
};

const formatError =
  "That doesn’t look like the authorization code. Copy the full value from the confirmation page — it looks like code#state.";

async function renderAwaitingCode(onSaved = vi.fn()) {
  render(<AnthropicOAuthConnect onSaved={onSaved} />);
  fireEvent.click(screen.getByRole("button", { name: /Connect Claude/ }));
  await waitFor(() => expect(screen.getByPlaceholderText(/code#state/)).toBeTruthy());
  return onSaved;
}

beforeEach(() => {
  startAnthropicOAuth.mockResolvedValue({
    authorizeUrl: "https://claude.ai/oauth/authorize?x=1",
    sessionId: "sess-1",
  });
  completeAnthropicOAuth.mockResolvedValue({
    status: "completed",
    email: "alice@example.com",
    credentials: savedCredentials,
  });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("AnthropicOAuthConnect", () => {
  it("blocks a paste without a # separator and shows a specific error", async () => {
    await renderAwaitingCode();

    fireEvent.change(screen.getByPlaceholderText(/code#state/), {
      target: { value: "just-a-code-without-state" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Complete sign-in/ }));

    await waitFor(() => expect(screen.getByText(formatError)).toBeTruthy());
    expect(completeAnthropicOAuth).not.toHaveBeenCalled();
  });

  it("blocks pastes with extra '#' parts, whitespace, or missing halves", async () => {
    await renderAwaitingCode();
    const input = screen.getByPlaceholderText(/code#state/);
    const submit = screen.getByRole("button", { name: /Complete sign-in/ });

    for (const bad of ["code#state#extra", "code# state", "#state", "code#"]) {
      fireEvent.change(input, { target: { value: bad } });
      fireEvent.click(submit);
      await waitFor(() => expect(screen.getByText(formatError)).toBeTruthy());
    }
    expect(completeAnthropicOAuth).not.toHaveBeenCalled();
  });

  it("completes sign-in with a well-formed code#state value", async () => {
    const onSaved = await renderAwaitingCode();

    fireEvent.change(screen.getByPlaceholderText(/code#state/), {
      target: { value: "  abc123#def456  " },
    });
    fireEvent.click(screen.getByRole("button", { name: /Complete sign-in/ }));

    await waitFor(() =>
      expect(screen.getByText(/Claude credentials saved for alice@example.com/)).toBeTruthy(),
    );
    expect(completeAnthropicOAuth).toHaveBeenCalledWith("abc123#def456", "sess-1");
    expect(onSaved).toHaveBeenCalledWith(savedCredentials);
    expect(screen.queryByText(formatError)).toBeNull();
  });

  it("tells the user to paste the whole string from the confirmation page", async () => {
    await renderAwaitingCode();
    expect(
      screen.getByText(/paste the whole\s+string shown on the confirmation page/),
    ).toBeTruthy();
  });
});
