import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useMySecretInventory } from "@/hooks/useMySecretInventory";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    listMyCredentials: vi.fn().mockResolvedValue({
      namespace: "user-alice",
      secrets: [{ name: "alice-only", keys: ["token"] }],
      integrations: [{ name: "grafana", keys: ["token"] }],
    }),
  },
}));

afterEach(() => {
  vi.clearAllMocks();
});

describe("useMySecretInventory", () => {
  it("returns caller inventory for resources in the caller namespace", async () => {
    const { result } = renderHook(() => useMySecretInventory("user-alice"));

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.secrets.map((secret) => secret.name)).toEqual(["alice-only"]);
    expect(result.current.integrations.map((integration) => integration.name)).toEqual(["grafana"]);
  });

  it("hides caller inventory for resources in another user's namespace", async () => {
    const { result } = renderHook(() => useMySecretInventory("user-bob"));

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.secrets).toEqual([]);
    expect(result.current.integrations).toEqual([]);
    expect(client.listMyCredentials).toHaveBeenCalledTimes(1);
  });
});
