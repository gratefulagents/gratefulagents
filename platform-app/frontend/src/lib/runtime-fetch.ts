import { isTauri } from "./platform";
import { monitoredFetch } from "./api-monitor";

// Selfdev tauri-sim escape hatch: when the selfdev harness fakes
// `__TAURI_INTERNALS__` in a plain browser (see selfdev/src/browser/tauri-sim.ts),
// there is no native plugin-http backend, so it sets this flag to keep network
// traffic on the browser's own fetch. Never set in real Tauri builds.
declare global {
  var __OPERATOR_FORCE_WEB_FETCH__: boolean | undefined;
}

export const runtimeFetch: typeof globalThis.fetch = async (input, init) => {
  if (!isTauri || globalThis.__OPERATOR_FORCE_WEB_FETCH__) {
    return monitoredFetch(globalThis.fetch.bind(globalThis), input, init);
  }

  const { fetch } = await import("@tauri-apps/plugin-http");
  return monitoredFetch(fetch as typeof globalThis.fetch, input, init);
};
