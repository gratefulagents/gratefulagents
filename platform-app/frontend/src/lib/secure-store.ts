// Operator — secure token store.
//
// Uses the Tauri 2 `plugin-store` when available (disk-backed, app-scoped,
// OS-protected by keychain/file permissions). In browser dev, falls back
// to localStorage so hot-reload keeps you signed in.

import { isTauri } from "./is-tauri";

type Awaitable<T> = T | Promise<T>;

interface Store {
  get<T = unknown>(key: string): Awaitable<T | null>;
  set(key: string, value: unknown): Awaitable<void>;
  delete(key: string): Awaitable<void>;
}

let storePromise: Promise<Store> | null = null;

function localStore(): Store {
  return {
    get<T>(k: string): T | null {
      const v = localStorage.getItem(k);
      if (v == null) return null;
      try {
        return JSON.parse(v) as T;
      } catch {
        localStorage.removeItem(k);
        return null;
      }
    },
    set(k, v) {
      localStorage.setItem(k, JSON.stringify(v));
    },
    delete(k) {
      localStorage.removeItem(k);
    },
  };
}

async function getStore(): Promise<Store> {
  if (!isTauri) return localStore();
  if (!storePromise) {
    storePromise = (async (): Promise<Store> => {
      try {
        const mod = await import("@tauri-apps/plugin-store");
        const s = await mod.load("gratefulagents.bin", { defaults: {}, autoSave: true });
        return {
          async get<T>(k: string): Promise<T | null> {
            const v = await s.get<T>(k);
            return v ?? null;
          },
          async set(k, v) {
            await s.set(k, v);
          },
          async delete(k) {
            await s.delete(k);
          },
        };
      } catch {
        return localStore();
      }
    })();
  }
  return storePromise;
}

export async function storeGet<T = unknown>(key: string): Promise<T | null> {
  const s = await getStore();
  return (await s.get<T>(key)) ?? null;
}

export async function storeSet(key: string, value: unknown): Promise<void> {
  const s = await getStore();
  await s.set(key, value);
}

export async function storeDelete(key: string): Promise<void> {
  const s = await getStore();
  await s.delete(key);
}
