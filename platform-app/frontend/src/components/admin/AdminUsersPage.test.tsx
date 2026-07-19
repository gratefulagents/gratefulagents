import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import AdminUsersPage from "@/components/admin/AdminUsersPage";

const listUsers = vi.fn();
const updateUserRole = vi.fn();
const deleteUser = vi.fn();

vi.mock("@/lib/auth-client", () => ({
  getAuthClient: () => ({ listUsers, updateUserRole, deleteUser }),
}));

vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({
    user: { id: "u1", username: "alice", name: "Alice", email: "alice@x.dev", role: "admin" },
  }),
}));

const alice = {
  id: "u1",
  username: "alice",
  name: "Alice",
  email: "alice@x.dev",
  picture: "",
  role: "admin",
  createdAt: 1700000000n,
  lastLoginAt: BigInt(Math.floor(Date.now() / 1000) - 120),
};
const bob = {
  id: "u2",
  username: "bob",
  name: "Bob",
  email: "bob@x.dev",
  picture: "",
  role: "member",
  createdAt: 1700000000n,
  lastLoginAt: 0n,
};

afterEach(() => {
  cleanup();
  listUsers.mockReset();
  updateUserRole.mockReset();
  deleteUser.mockReset();
});

describe("AdminUsersPage", () => {
  it("lists users with role and last login", async () => {
    listUsers.mockResolvedValue({ users: [alice, bob] });
    render(<AdminUsersPage />);

    expect(await screen.findByText("Alice")).toBeTruthy();
    expect(screen.getByText("Bob")).toBeTruthy();
    // Bob never logged in.
    expect(screen.getByText("Never")).toBeTruthy();
    // Alice logged in 2 minutes ago.
    expect(screen.getByText("2m ago")).toBeTruthy();
    // The current user is marked and cannot be deleted.
    expect(screen.getByText("(you)")).toBeTruthy();
    expect(
      (screen.getByRole("button", { name: "Delete Alice" }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("promotes a member to admin", async () => {
    listUsers.mockResolvedValue({ users: [alice, bob] });
    updateUserRole.mockResolvedValue({ ...bob, role: "admin" });
    render(<AdminUsersPage />);

    fireEvent.click(await screen.findByRole("button", { name: /Promote to admin/ }));

    await waitFor(() => {
      expect(updateUserRole).toHaveBeenCalledWith({ userId: "u2", role: "admin" });
    });
    // The promote button disappears once Bob is an admin.
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: /Promote to admin/ })).toBeNull();
    });
  });

  it("deletes a user after confirmation", async () => {
    listUsers.mockResolvedValue({ users: [alice, bob] });
    deleteUser.mockResolvedValue({});
    render(<AdminUsersPage />);

    fireEvent.click(await screen.findByRole("button", { name: "Delete Bob" }));
    fireEvent.click(await screen.findByRole("button", { name: "Delete user" }));

    await waitFor(() => {
      expect(deleteUser).toHaveBeenCalledWith({ userId: "u2" });
    });
    await waitFor(() => {
      expect(screen.queryByText("Bob")).toBeNull();
    });
  });
});
