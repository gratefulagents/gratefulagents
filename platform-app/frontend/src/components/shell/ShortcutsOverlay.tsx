import * as React from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Kbd } from "@/components/shell/Kbd";
import { APP_SHORTCUTS } from "@/components/shell/shortcuts";

/**
 * Keyboard shortcuts cheat-sheet. Opens on ⌘/. The user can memorise the
 * set or keep the sheet pinned visually on a second monitor.
 */
export function ShortcutsOverlay({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[540px]">
        <DialogHeader>
          <DialogTitle className="text-[14px]">Keyboard shortcuts</DialogTitle>
          <DialogDescription className="text-[12px]">
            gratefulagents is keyboard-first. Press <Kbd>⌘</Kbd> <Kbd>/</Kbd> any
            time to reopen this sheet.
          </DialogDescription>
        </DialogHeader>
        <div className="grid grid-cols-1 gap-5 pt-1 sm:grid-cols-2">
          {APP_SHORTCUTS.map((group) => (
            <section key={group.group}>
              <h3 className="mb-2 text-[10.5px] font-medium uppercase tracking-[0.08em] text-muted-foreground">
                {group.group}
              </h3>
              <ul className="space-y-1.5">
                {group.items.map((item) => (
                  <li
                    key={item.label}
                    className="flex items-center justify-between gap-3 text-[12.5px]"
                  >
                    <span className="text-foreground/90">{item.label}</span>
                    <span className="flex items-center gap-0.5">
                      {item.keys.map((k, idx) => (
                        <React.Fragment key={`${item.label}-${k}`}>
                          {idx > 0 && (
                            <span className="mx-0.5 text-muted-foreground/60">
                              +
                            </span>
                          )}
                          <Kbd>{k}</Kbd>
                        </React.Fragment>
                      ))}
                    </span>
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  );
}
