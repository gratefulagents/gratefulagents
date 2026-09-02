import { render, type RenderOptions } from "@testing-library/react";
import type { ReactElement, ReactNode } from "react";
import { vi } from "vitest";

import { RunActionsProvider, type RunAction, type RunActions } from "./RunActionsContext";

type RunActionOverrides = {
  [K in keyof RunActions]?: RunActions[K] extends (...args: never[]) => unknown
    ? RunActions[K]
    : Partial<RunActions[K]>;
};

export function idleAction(overrides: Partial<RunAction> = {}): RunAction {
  return { can: false, run: vi.fn(), busy: false, ...overrides };
}

/** A fully inert action set; override only what the test cares about. */
export function makeRunActions(overrides: RunActionOverrides = {}): RunActions {
  return {
    retry: idleAction(overrides.retry),
    stop: idleAction(overrides.stop),
    promote: idleAction(overrides.promote),
    delete: idleAction(overrides.delete),
    interrupt: idleAction(overrides.interrupt),
    rename: { can: false, run: vi.fn(), ...overrides.rename },
    extendRuntime: {
      can: false,
      open: false,
      setOpen: vi.fn(),
      value: "1h",
      setValue: vi.fn(),
      submit: vi.fn(),
      busy: false,
      isPaused: false,
      ...overrides.extendRuntime,
    },
    share: { open: false, setOpen: vi.fn(), ...overrides.share },
    focusComposer: overrides.focusComposer ?? vi.fn(),
    openInspectorTab: overrides.openInspectorTab ?? vi.fn(),
  };
}

export function renderWithRunActions(
  ui: ReactElement,
  actions: RunActionOverrides = {},
  options?: Omit<RenderOptions, "wrapper">,
) {
  const value = makeRunActions(actions);
  const wrapper = ({ children }: { children: ReactNode }) => (
    <RunActionsProvider value={value}>{children}</RunActionsProvider>
  );
  return {
    actions: value,
    ...render(ui, { ...options, wrapper }),
  };
}
