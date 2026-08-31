import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { LoginPage } from "@/components/LoginPage";
import { APP_VERSION } from "@/lib/build-info";

const authState = {
  connectToServer: vi.fn(),
  environment: {
    endpointUrl: "http://operator.test",
    cfAccessClientId: "",
    cfAccessClientSecret: "",
  },
  error: "",
  isConnected: true,
  loginWithGoogle: vi.fn(),
  loginWithPassword: vi.fn(),
  redeemSetupToken: vi.fn(),
  config: { googleClientId: "" },
  workspaces: [],
};

vi.mock("../contexts/AuthContext", () => ({
  useAuth: () => authState,
}));

vi.mock("@/lib/platform", () => ({
  isTauri: false,
}));

vi.mock("@/lib/theme", () => ({
  useTheme: () => "light",
}));

vi.mock("@react-oauth/google", () => ({
  GoogleLogin: () => <button type="button">Google</button>,
  GoogleOAuthProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

afterEach(() => {
  cleanup();
  authState.loginWithPassword.mockReset();
  authState.redeemSetupToken.mockReset();
  authState.error = "";
  window.history.replaceState({}, "", "/");
});

describe("LoginPage", () => {
  it("shows the app version", () => {
    render(<LoginPage />);

    expect(screen.getByText(`build v${APP_VERSION}`)).toBeTruthy();
  });

  it("renders and submits the username/password form", async () => {
    render(<LoginPage />);

    fireEvent.change(screen.getByPlaceholderText("admin"), { target: { value: "admin" } });
    fireEvent.change(screen.getByPlaceholderText("Enter password"), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign In" }));

    await waitFor(() => {
      expect(authState.loginWithPassword).toHaveBeenCalledWith("admin", "secret");
    });
  });

  it("displays login errors", async () => {
    authState.loginWithPassword.mockRejectedValueOnce(new Error("bad credentials"));
    render(<LoginPage />);

    fireEvent.change(screen.getByPlaceholderText("admin"), { target: { value: "admin" } });
    fireEvent.change(screen.getByPlaceholderText("Enter password"), { target: { value: "bad" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign In" }));

    expect(await screen.findByText("bad credentials")).toBeTruthy();
  });

  it("redeems a setup_token from the URL and strips it", async () => {
    authState.redeemSetupToken.mockResolvedValueOnce(undefined);
    window.history.replaceState({}, "", "/login?setup_token=tok-123");

    render(<LoginPage />);

    await waitFor(() => {
      expect(authState.redeemSetupToken).toHaveBeenCalledWith("tok-123");
    });
    expect(window.location.search).not.toContain("setup_token");
    expect(window.location.pathname).toBe("/");
  });

  it("falls back to the password form when the setup link is rejected", async () => {
    authState.redeemSetupToken.mockRejectedValueOnce(new Error("invalid or expired setup link"));
    window.history.replaceState({}, "", "/login?setup_token=used-token");

    render(<LoginPage />);

    expect(
      await screen.findByText("This setup link is invalid or was already used. Sign in with your password instead."),
    ).toBeTruthy();
    expect(window.location.search).not.toContain("setup_token");

    authState.loginWithPassword.mockResolvedValueOnce(undefined);
    fireEvent.change(screen.getByPlaceholderText("admin"), { target: { value: "admin" } });
    fireEvent.change(screen.getByPlaceholderText("Enter password"), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign In" }));

    await waitFor(() => {
      expect(authState.loginWithPassword).toHaveBeenCalledWith("admin", "secret");
    });
  });

  it("does not call redeemSetupToken without a setup_token param", () => {
    render(<LoginPage />);

    expect(authState.redeemSetupToken).not.toHaveBeenCalled();
  });
});
