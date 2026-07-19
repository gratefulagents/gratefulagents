import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Outside Tauri the secure store falls back to localStorage, so these tests
// drive the real get/set/delete paths against a Map-backed localStorage stub
// (this jsdom setup exposes a bare `localStorage` without the Storage API).
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

async function freshModule() {
  vi.resetModules();
  return import("@/lib/desktop-updater");
}

beforeEach(() => {
  installLocalStorage();
});

afterEach(() => {
  localStorage.clear();
  vi.unstubAllGlobals();
});

describe("release repository", () => {
  it("checks and links to releases in gratefulagents/gratefulagents", async () => {
    const updater = await freshModule();
    expect(updater.DISTRIBUTION_REPO).toBe("gratefulagents/gratefulagents");
    expect(updater.DISTRIBUTION_RELEASES_URL).toBe(
      "https://github.com/gratefulagents/gratefulagents/releases",
    );
  });
});

describe("legacy updater credential", () => {
  it("removes a token saved by older private-release builds", async () => {
    localStorage.setItem("desktopUpdaterGithubToken", JSON.stringify("old-token"));
    const updater = await freshModule();
    await updater.removeLegacyUpdaterToken();
    expect(localStorage.getItem("desktopUpdaterGithubToken")).toBeNull();
  });
});

describe("update check interval setting", () => {
  it("defaults to hourly when nothing is stored", async () => {
    const updater = await freshModule();
    expect(updater.DEFAULT_UPDATE_CHECK_INTERVAL_HOURS).toBe(1);
    await expect(updater.getUpdateCheckIntervalHours()).resolves.toBe(1);
  });

  it("offers launch-only through daily cadences", async () => {
    const updater = await freshModule();
    expect(updater.UPDATE_CHECK_INTERVAL_OPTIONS.map((o) => o.hours)).toEqual([0, 1, 6, 12, 24]);
  });

  it("round-trips stored values, including launch-only (0)", async () => {
    const updater = await freshModule();
    await updater.setUpdateCheckIntervalHours(6);
    await expect(updater.getUpdateCheckIntervalHours()).resolves.toBe(6);
    await updater.setUpdateCheckIntervalHours(0);
    await expect(updater.getUpdateCheckIntervalHours()).resolves.toBe(0);
  });

  it("falls back to the default when the store holds junk", async () => {
    const updater = await freshModule();
    localStorage.setItem("desktopUpdaterCheckIntervalHours", JSON.stringify("soon"));
    await expect(updater.getUpdateCheckIntervalHours()).resolves.toBe(1);
    localStorage.setItem("desktopUpdaterCheckIntervalHours", "not-even-json");
    await expect(updater.getUpdateCheckIntervalHours()).resolves.toBe(1);
  });

  it("sanitizes writes: junk becomes the default, out-of-range clamps to 0..168", async () => {
    const updater = await freshModule();
    await expect(updater.setUpdateCheckIntervalHours(Number.NaN)).resolves.toBe(1);
    await expect(updater.getUpdateCheckIntervalHours()).resolves.toBe(1);
    await expect(updater.setUpdateCheckIntervalHours(1000)).resolves.toBe(168);
    await expect(updater.getUpdateCheckIntervalHours()).resolves.toBe(168);
    await expect(updater.setUpdateCheckIntervalHours(-4)).resolves.toBe(0);
    await expect(updater.getUpdateCheckIntervalHours()).resolves.toBe(0);
  });

  it("dispatches the interval-changed window event with the sanitized hours", async () => {
    const updater = await freshModule();
    expect(updater.UPDATE_CHECK_INTERVAL_EVENT).toBe("desktop-updater:interval-changed");
    const seen: number[] = [];
    const onChange = (event: Event) =>
      seen.push((event as CustomEvent<{ hours: number }>).detail.hours);
    window.addEventListener(updater.UPDATE_CHECK_INTERVAL_EVENT, onChange);
    try {
      await updater.setUpdateCheckIntervalHours(12);
      await updater.setUpdateCheckIntervalHours(999);
    } finally {
      window.removeEventListener(updater.UPDATE_CHECK_INTERVAL_EVENT, onChange);
    }
    expect(seen).toEqual([12, 168]);
  });
});
