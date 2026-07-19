import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// This jsdom setup exposes a bare `localStorage` stub without the Storage API,
// so install a minimal Map-backed implementation for these tests.
function installLocalStorage() {
  const map = new Map<string, string>();
  const storage = {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => {
      map.set(k, String(v));
    },
    removeItem: (k: string) => {
      map.delete(k);
    },
    clear: () => map.clear(),
    key: (i: number) => Array.from(map.keys())[i] ?? null,
    get length() {
      return map.size;
    },
  };
  vi.stubGlobal("localStorage", storage);
  return storage;
}

// workspaces.ts keeps module-level state and reads localStorage at import time,
// so each test re-imports the module fresh after resetting storage.
async function freshModule() {
  vi.resetModules();
  return import("@/lib/workspaces");
}

beforeEach(() => {
  installLocalStorage();
});

afterEach(() => {
  localStorage.clear();
  vi.unstubAllGlobals();
});

describe("workspaces store", () => {
  it("seeds a single implicit workspace on the web build", async () => {
    const ws = await freshModule();
    const all = ws.getWorkspaces();
    expect(all).toHaveLength(1);
    expect(ws.getActiveWorkspaceId()).toBe(all[0].id);
    expect(all[0].endpointUrl).toBe("");
  });

  it("migrates the legacy single-endpoint keys into a default workspace", async () => {
    localStorage.setItem("operator.endpoint", "https://legacy.example.com");
    localStorage.setItem("operator.cf_access_client_id", "cf-id");
    localStorage.setItem("operator.cf_access_client_secret", "cf-secret");

    const ws = await freshModule();
    const all = ws.getWorkspaces();
    expect(all).toHaveLength(1);
    expect(all[0].endpointUrl).toBe("https://legacy.example.com");
    expect(all[0].cfAccessClientId).toBe("cf-id");
    expect(all[0].cfAccessClientSecret).toBe("cf-secret");
    expect(all[0].name).toBe("legacy.example.com");
    expect(ws.getActiveWorkspaceId()).toBe(all[0].id);
  });

  it("adds a workspace and makes it active", async () => {
    const ws = await freshModule();
    const created = ws.addWorkspace({
      name: "Prod",
      endpointUrl: "https://prod.example.com",
      cfAccessClientId: "",
      cfAccessClientSecret: "",
    });
    expect(ws.getWorkspaces()).toHaveLength(2);
    expect(ws.getActiveWorkspaceId()).toBe(created.id);
    expect(ws.getActiveWorkspace()?.name).toBe("Prod");
  });

  it("derives a name from the endpoint host when none is given", async () => {
    const ws = await freshModule();
    const created = ws.addWorkspace({
      name: "",
      endpointUrl: "https://my-operator.example.com:8443/",
      cfAccessClientId: "",
      cfAccessClientSecret: "",
    });
    expect(created.name).toBe("my-operator.example.com:8443");
  });

  it("updates an existing workspace by id", async () => {
    const ws = await freshModule();
    const created = ws.addWorkspace({
      name: "A",
      endpointUrl: "https://a.example.com",
      cfAccessClientId: "",
      cfAccessClientSecret: "",
    });
    const updated = ws.updateWorkspace(created.id, { name: "Renamed" });
    expect(updated?.name).toBe("Renamed");
    expect(ws.getWorkspaces().find((w) => w.id === created.id)?.name).toBe("Renamed");
  });

  it("upsertActiveWorkspace edits the active workspace in place", async () => {
    const ws = await freshModule();
    const before = ws.getActiveWorkspaceId();
    ws.upsertActiveWorkspace({
      endpointUrl: "https://edited.example.com",
      cfAccessClientId: "",
      cfAccessClientSecret: "",
    });
    expect(ws.getWorkspaces()).toHaveLength(1);
    expect(ws.getActiveWorkspaceId()).toBe(before);
    expect(ws.getActiveWorkspace()?.endpointUrl).toBe("https://edited.example.com");
  });

  it("switches the active workspace", async () => {
    const ws = await freshModule();
    const first = ws.getActiveWorkspaceId();
    const second = ws.addWorkspace({
      name: "Second",
      endpointUrl: "https://second.example.com",
      cfAccessClientId: "",
      cfAccessClientSecret: "",
    });
    ws.setActiveWorkspace(first);
    expect(ws.getActiveWorkspaceId()).toBe(first);
    ws.setActiveWorkspace(second.id);
    expect(ws.getActiveWorkspaceId()).toBe(second.id);
  });

  it("removing the active workspace falls back to a remaining one", async () => {
    const ws = await freshModule();
    const first = ws.getActiveWorkspaceId();
    const second = ws.addWorkspace({
      name: "Second",
      endpointUrl: "https://second.example.com",
      cfAccessClientId: "",
      cfAccessClientSecret: "",
    });
    expect(ws.getActiveWorkspaceId()).toBe(second.id);
    ws.removeWorkspace(second.id);
    expect(ws.getWorkspaces()).toHaveLength(1);
    expect(ws.getActiveWorkspaceId()).toBe(first);
  });

  it("persists workspaces to localStorage", async () => {
    const ws = await freshModule();
    ws.addWorkspace({
      name: "Persisted",
      endpointUrl: "https://persisted.example.com",
      cfAccessClientId: "",
      cfAccessClientSecret: "",
    });
    const raw = localStorage.getItem("gratefulagents.workspaces");
    expect(raw).toBeTruthy();
    expect(JSON.parse(raw as string)).toHaveLength(2);
  });

  it("clears auth secrets for a removed workspace namespace", async () => {
    const ws = await freshModule();
    const { clearWorkspaceSecrets, workspaceAuthStoreKeys } = await import("@/lib/client");
    const second = ws.addWorkspace({
      name: "Second",
      endpointUrl: "https://second.example.com",
      cfAccessClientId: "",
      cfAccessClientSecret: "",
    });
    const firstKeys = workspaceAuthStoreKeys("default");
    const secondKeys = workspaceAuthStoreKeys(second.id);
    localStorage.setItem(firstKeys.accessToken, JSON.stringify("keep-access"));
    localStorage.setItem(secondKeys.accessToken, JSON.stringify("drop-access"));
    localStorage.setItem(secondKeys.refreshToken, JSON.stringify("drop-refresh"));
    localStorage.setItem(secondKeys.user, JSON.stringify({ id: "drop-user" }));

    await clearWorkspaceSecrets(second.id);
    ws.removeWorkspace(second.id);

    expect(localStorage.getItem(firstKeys.accessToken)).toBe(JSON.stringify("keep-access"));
    expect(localStorage.getItem(secondKeys.accessToken)).toBeNull();
    expect(localStorage.getItem(secondKeys.refreshToken)).toBeNull();
    expect(localStorage.getItem(secondKeys.user)).toBeNull();
  });

  it("drops corrupt local secure-store values instead of throwing", async () => {
    const { storeGet } = await import("@/lib/secure-store");
    localStorage.setItem("bad-json", "{not-json");

    await expect(storeGet("bad-json")).resolves.toBeNull();
    expect(localStorage.getItem("bad-json")).toBeNull();
  });
});
