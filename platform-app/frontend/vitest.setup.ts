// Base UI dispatches PointerEvent from switch/select controls. jsdom 25 does
// not implement it, so user-like interactions otherwise throw or silently fail.
if (typeof window.PointerEvent !== "function") {
  Object.defineProperty(window, "PointerEvent", {
    configurable: true,
    value: MouseEvent,
  });
}

// Node ≥22 ships an experimental `globalThis.localStorage` that is
// non-functional unless the process is started with --localstorage-file, and
// under jsdom it can shadow the DOM Storage implementation. Use a Map-backed
// fallback so tests exercise real get/set semantics on every Node.
if (typeof globalThis.localStorage?.getItem !== "function") {
  const store = new Map<string, string>();
  const storage: Storage = {
    get length() {
      return store.size;
    },
    key: (index: number) => [...store.keys()][index] ?? null,
    getItem: (key: string) => store.get(String(key)) ?? null,
    setItem: (key: string, value: string) => void store.set(String(key), String(value)),
    removeItem: (key: string) => void store.delete(String(key)),
    clear: () => void store.clear(),
  };
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: storage,
  });
}
