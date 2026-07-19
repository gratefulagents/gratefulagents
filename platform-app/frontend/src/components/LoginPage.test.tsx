import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { LoginPage } from "@/components/LoginPage";

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
  authState.error = "";
});

describe("LoginPage", () => {
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
});
