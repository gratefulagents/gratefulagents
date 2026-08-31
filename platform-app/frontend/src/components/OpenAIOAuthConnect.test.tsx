import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { OpenAIOAuthConnect } from "@/components/OpenAIOAuthConnect";

const startOpenAIOAuth = vi.fn();
const startOpenAIDeviceOAuth = vi.fn();
const pollOpenAIOAuth = vi.fn();
const cancelOpenAIOAuth = vi.fn();

vi.mock("@/lib/openai-oauth", () => ({
  startOpenAIOAuth: (...args: unknown[]) => startOpenAIOAuth(...args),
  startOpenAIDeviceOAuth: (...args: unknown[]) => startOpenAIDeviceOAuth(...args),
  pollOpenAIOAuth: (...args: unknown[]) => pollOpenAIOAuth(...args),
  cancelOpenAIOAuth: (...args: unknown[]) => cancelOpenAIOAuth(...args),
}));

vi.mock("@/lib/client", () => ({
  client: {
    updateMyCredentials: vi.fn(),
  },
}));

vi.mock("@/lib/native", () => ({
  copyText: vi.fn().mockResolvedValue(true),
  openExternal: vi.fn().mockResolvedValue(undefined),
}));

let mockIsTauri = false;
vi.mock("@/lib/platform", () => ({
  get isTauri() {
    return mockIsTauri;
  },
}));

beforeEach(() => {
  startOpenAIOAuth.mockResolvedValue({
    mode: "browser",
    authorizeUrl: "https://auth.openai.com/authorize?x=1",
    sessionId: "sess-browser",
  });
  startOpenAIDeviceOAuth.mockResolvedValue({
    mode: "device",
    verificationUri: "https://chatgpt.com/device",
    userCode: "ABCD-1234",
    interval: 5,
    sessionId: "sess-device",
  });
  pollOpenAIOAuth.mockResolvedValue({ status: "pending" });
  cancelOpenAIOAuth.mockResolvedValue(undefined);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  mockIsTauri = false;
});

describe("OpenAIOAuthConnect", () => {
  it("offers the device-code option proactively on desktop", async () => {
    mockIsTauri = true;
    render(<OpenAIOAuthConnect onSaved={vi.fn()} />);

    const deviceButton = screen.getByRole("button", { name: /Use a device code instead/ });
    expect(deviceButton).toBeTruthy();

    fireEvent.click(deviceButton);
    await waitFor(() => expect(screen.getByText("ABCD-1234")).toBeTruthy());
    expect(startOpenAIDeviceOAuth).toHaveBeenCalledTimes(1);
    expect(startOpenAIOAuth).not.toHaveBeenCalled();
  });

  it("keeps the browser sign-in as the primary desktop action", async () => {
    mockIsTauri = true;
    render(<OpenAIOAuthConnect onSaved={vi.fn()} />);

    fireEvent.click(screen.getByRole("button", { name: /Sign in with ChatGPT/ }));
    await waitFor(() =>
      expect(screen.getByText(/Finish signing in with ChatGPT in your browser/)).toBeTruthy(),
    );
    expect(startOpenAIOAuth).toHaveBeenCalledTimes(1);
    expect(startOpenAIDeviceOAuth).not.toHaveBeenCalled();
  });

  it("does not show the device-code option on web before a failure", () => {
    render(<OpenAIOAuthConnect onSaved={vi.fn()} />);

    expect(screen.getByRole("button", { name: /Sign in with ChatGPT/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Use a device code instead/ })).toBeNull();
  });

  it("still offers the device-code fallback on web after the browser start fails", async () => {
    startOpenAIOAuth.mockRejectedValue(new Error("no local port"));
    render(<OpenAIOAuthConnect onSaved={vi.fn()} />);

    fireEvent.click(screen.getByRole("button", { name: /Sign in with ChatGPT/ }));
    await waitFor(() => expect(screen.getByText("no local port")).toBeTruthy());
    expect(screen.getByRole("button", { name: /Use a device code instead/ })).toBeTruthy();
  });
});
