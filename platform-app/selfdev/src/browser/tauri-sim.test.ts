import * as vm from "node:vm";
import { describe, expect, it } from "vitest";
import { buildTauriSimScript } from "./tauri-sim";

// Pins the invoke contract of the __TAURI_INTERNALS__ stub by evaluating the
// init script in a sandboxed "window" and calling it like @tauri-apps/api does.

interface Sandbox {
  window: Sandbox;
  localStorage: {
    getItem(k: string): string | null;
    setItem(k: string, v: string): void;
    removeItem(k: string): void;
    key(i: number): string | null;
    readonly length: number;
  };
  console: Console;
  __TAURI_INTERNALS__?: {
    invoke(cmd: string, args?: Record<string, unknown>): Promise<unknown>;
    transformCallback(cb: (r: unknown) => void, once?: boolean): number;
    metadata: { currentWindow: { label: string } };
  };
  __TAURI_OS_PLUGIN_INTERNALS__?: { platform: string };
  __OPERATOR_FORCE_WEB_FETCH__?: boolean;
}

function evalSim(script: string): Sandbox {
  const store = new Map<string, string>();
  const localStorage: Sandbox["localStorage"] = {
    getItem: (k) => (store.has(k) ? (store.get(k) as string) : null),
    setItem: (k, v) => void store.set(k, String(v)),
    removeItem: (k) => void store.delete(k),
    key: (i) => [...store.keys()][i] ?? null,
    get length() {
      return store.size;
    },
  };
  const sandbox = { localStorage, console } as unknown as Sandbox;
  sandbox.window = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(script, sandbox as unknown as vm.Context);
  return sandbox;
}

describe("tauri-sim init script", () => {
  it("defines __TAURI_INTERNALS__ and flips the web-fetch escape hatch", () => {
    const sandbox = evalSim(buildTauriSimScript());
    expect(sandbox.__TAURI_INTERNALS__).toBeDefined();
    expect(sandbox.__TAURI_INTERNALS__?.metadata.currentWindow.label).toBe("main");
    expect(sandbox.__OPERATOR_FORCE_WEB_FETCH__).toBe(true);
    expect(sandbox.__TAURI_OS_PLUGIN_INTERNALS__?.platform).toBe("macos");
  });

  it("answers the plugin commands the app relies on", async () => {
    const sandbox = evalSim(buildTauriSimScript({ platform: "ios", appVersion: "9.9.9" }));
    const invoke = sandbox.__TAURI_INTERNALS__!.invoke;
    await expect(invoke("plugin:os|platform")).resolves.toBe("ios");
    await expect(invoke("plugin:app|version")).resolves.toBe("9.9.9");
    await expect(invoke("plugin:notification|is_permission_granted")).resolves.toBe(true);
    await expect(invoke("plugin:event|listen", { event: "tauri://focus" })).resolves.toBeTypeOf("number");
  });

  it("backs the store plugin with localStorage so storageState captures auth", async () => {
    const sandbox = evalSim(buildTauriSimScript());
    const invoke = sandbox.__TAURI_INTERNALS__!.invoke;
    await invoke("plugin:store|load", { path: "gratefulagents.bin" });
    await invoke("plugin:store|set", { rid: 1, key: "access_token::local", value: "tok-123" });
    await expect(invoke("plugin:store|get", { rid: 1, key: "access_token::local" })).resolves.toEqual([
      "tok-123",
      true,
    ]);
    await expect(invoke("plugin:store|has", { rid: 1, key: "missing" })).resolves.toBe(false);
    expect(sandbox.localStorage.getItem("__TAURI_STORE__.access_token::local")).toBe('"tok-123"');
    await expect(invoke("plugin:store|keys", { rid: 1 })).resolves.toEqual(["access_token::local"]);
  });

  it("serves fixture local credentials and cancels google oauth benignly", async () => {
    const creds = [
      { provider: "anthropic", label: "Anthropic OAuth", sourcePath: "/x", account: "a@b.c", authJson: "{}" },
    ];
    const sandbox = evalSim(buildTauriSimScript({ localCredentials: creds }));
    const invoke = sandbox.__TAURI_INTERNALS__!.invoke;
    await expect(invoke("detect_local_credentials")).resolves.toEqual(creds);
    await expect(invoke("plugin:google-oauth|start_google_oauth", { payload: {} })).resolves.toEqual({
      idToken: null,
      error: "cancelled",
    });
    await expect(invoke("start_openai_oauth")).rejects.toThrow(/not available/);
  });

  it("registers window callbacks via transformCallback", () => {
    const sandbox = evalSim(buildTauriSimScript());
    let got: unknown = null;
    const id = sandbox.__TAURI_INTERNALS__!.transformCallback((r) => (got = r), true);
    expect(id).toBeTypeOf("number");
    const cb = (sandbox as unknown as Record<string, (r: unknown) => void>)[`_${id}`];
    expect(cb).toBeTypeOf("function");
    cb({ ok: true });
    expect(got).toEqual({ ok: true });
  });
});
