import { createContext, useContext, type FormEvent, type ReactNode } from "react";

import type { InspectorTab } from "./RunInspector";

/** One run-level action: whether it applies, how to run it, and whether it is in flight. */
export interface RunAction {
  can: boolean;
  run: () => void | Promise<void>;
  busy: boolean;
}

export interface RunActions {
  retry: RunAction;
  stop: RunAction;
  promote: RunAction;
  delete: RunAction;
  /** Stop the in-flight turn without killing the run. */
  interrupt: RunAction;
  rename: { can: boolean; run: (displayName: string) => void | Promise<void> };
  extendRuntime: {
    can: boolean;
    open: boolean;
    setOpen: (open: boolean) => void;
    value: string;
    setValue: (value: string) => void;
    submit: (event?: FormEvent<HTMLFormElement>) => void | Promise<void>;
    busy: boolean;
    isPaused: boolean;
  };
  share: { open: boolean; setOpen: (open: boolean) => void };
  /** Move keyboard focus into the composer so the user can reply right away. */
  focusComposer: () => void;
  /** Open the inspector on a given tab (used by "Run context" in the overflow menu). */
  openInspectorTab: (tab: InspectorTab) => void;
}

const RunActionsContext = createContext<RunActions | null>(null);

export function RunActionsProvider({ value, children }: { value: RunActions; children: ReactNode }) {
  return <RunActionsContext.Provider value={value}>{children}</RunActionsContext.Provider>;
}

export function useRunActions(): RunActions {
  const actions = useContext(RunActionsContext);
  if (!actions) {
    throw new Error("useRunActions must be used within a RunActionsProvider");
  }
  return actions;
}
