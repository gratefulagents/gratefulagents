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

// jsdom has no ResizeObserver. Layout-aware components (inspector tab strip,
// sub-agent graph, trace waterfall) observe their containers; a no-op observer
// lets them mount and lets tests drive size via mocked clientWidth instead.
if (typeof globalThis.ResizeObserver === "undefined") {
  class NoopResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  Object.defineProperty(globalThis, "ResizeObserver", {
    configurable: true,
    writable: true,
    value: NoopResizeObserver,
  });
}

// With a ResizeObserver present, Base UI's ScrollArea waits on
// `element.getAnimations()`, which jsdom does not implement. Report "no
// animations" so the thumb geometry pass resolves immediately.
if (typeof Element !== "undefined" && typeof Element.prototype.getAnimations !== "function") {
  Object.defineProperty(Element.prototype, "getAnimations", {
    configurable: true,
    writable: true,
    value: () => [],
  });
}

// Base UI otherwise defers unmounts until `getAnimations()` settles, which
// would turn every dialog/menu close in tests into an async wait.
(globalThis as { BASE_UI_ANIMATIONS_DISABLED?: boolean }).BASE_UI_ANIMATIONS_DISABLED = true;
