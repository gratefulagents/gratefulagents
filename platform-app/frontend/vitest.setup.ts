// Node ≥22 ships an experimental `globalThis.localStorage` that is
// non-functional unless the process is started with --localstorage-file, and
// under the jsdom environment it shadows the DOM Storage implementation. When
// the ambient localStorage is unusable, replace it with a Map-backed Storage
// so components and tests exercise real get/set semantics on every Node.
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
