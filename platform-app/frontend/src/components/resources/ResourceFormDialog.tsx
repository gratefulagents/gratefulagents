import * as React from "react";
import { Loader2 } from "lucide-react";

import { cn } from "@/lib/utils";
import { toneText } from "@/lib/status";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";

/**
 * Shared chrome for every resource editor: icon + title header, a scrolling
 * body, and a pinned footer with the primary action. Closing with unsaved
 * edits asks first, so a long prompt is never lost to a stray Escape.
 *
 * `readOnly` turns the editor into a viewer: the body renders disabled
 * controls and the footer only offers Close.
 */
export function ResourceFormDialog({
  open,
  onClose,
  icon,
  title,
  description,
  dirty,
  saving,
  canSave,
  saveLabel,
  readOnly,
  error,
  onSave,
  footerStart,
  children,
}: {
  open: boolean;
  onClose: () => void;
  icon: React.ReactNode;
  title: string;
  description?: React.ReactNode;
  dirty: boolean;
  saving: boolean;
  canSave: boolean;
  saveLabel: string;
  readOnly?: boolean;
  error?: string | null;
  onSave: () => void;
  /** Optional element on the footer's left edge (validation summary, counts). */
  footerStart?: React.ReactNode;
  children: React.ReactNode;
}) {
  const [confirmDiscard, setConfirmDiscard] = React.useState(false);

  function requestClose() {
    if (saving) return;
    if (dirty && !readOnly) {
      setConfirmDiscard(true);
      return;
    }
    onClose();
  }

  return (
    <>
      <Dialog open={open} onOpenChange={(next) => !next && requestClose()}>
        <DialogContent
          className="flex w-full max-w-[calc(100%-1rem)] flex-col gap-0 overflow-hidden p-0 max-h-[92vh] sm:max-w-2xl"
          showCloseButton
        >
          <form
            className="flex min-h-0 flex-1 flex-col"
            onSubmit={(event) => {
              event.preventDefault();
              if (!readOnly && canSave && !saving) onSave();
            }}
          >
            <DialogHeader className="space-y-1 border-b px-4 py-4 pr-12 sm:px-6 sm:py-5">
              <div className="flex items-center gap-2.5">
                <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary [&_svg]:size-4">
                  {icon}
                </span>
                <DialogTitle className="min-w-0 truncate text-base">{title}</DialogTitle>
              </div>
              {description && <DialogDescription className="text-[12.5px]">{description}</DialogDescription>}
            </DialogHeader>

            {/* The scroll container is a div: a fieldset never shrinks below its
                content as a flex child, so it would push the footer off-screen. */}
            <div className="min-h-0 flex-1 overflow-y-auto px-4 py-5 sm:px-6">
              <fieldset disabled={readOnly || saving} className="min-w-0 space-y-6">
                {children}
              </fieldset>
            </div>

            <div className="flex flex-wrap items-center gap-2 border-t bg-muted/30 px-4 py-3 sm:px-6 sm:py-3.5">
              <div className="min-w-0 flex-1 text-[12px]">
                {error ? (
                  <p role="alert" className={cn("truncate", toneText.danger)} title={error}>
                    {error}
                  </p>
                ) : (
                  footerStart
                )}
              </div>
              <Button type="button" variant="ghost" size="sm" onClick={requestClose} disabled={saving}>
                {readOnly ? "Close" : "Cancel"}
              </Button>
              {!readOnly && (
                <Button type="submit" size="sm" disabled={!canSave || saving}>
                  {saving && <Loader2 className="size-3.5 animate-spin" />}
                  {saving ? "Saving…" : saveLabel}
                </Button>
              )}
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <ConfirmDialog
        open={confirmDiscard}
        onOpenChange={setConfirmDiscard}
        title="Discard changes?"
        description="Your edits have not been saved."
        confirmLabel="Discard"
        destructive
        onConfirm={() => {
          setConfirmDiscard(false);
          onClose();
        }}
      />
    </>
  );
}
