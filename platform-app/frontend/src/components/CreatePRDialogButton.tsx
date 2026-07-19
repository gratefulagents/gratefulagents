import { useState } from "react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import { client } from "@/lib/client";

const defaultCreatePRPrompt = [
  "Create a pull request now using the current diff and run context.",
  "Use the current branch changes as the source of truth for the PR title and description.",
  "Do not create another plan or ask for approval again before opening the PR.",
].join(" ");

function buildCreatePRMessage(guidance: string): string {
  const trimmed = guidance.trim();
  return trimmed ? `${trimmed}

${defaultCreatePRPrompt}` : defaultCreatePRPrompt;
}

export function CreatePRDialog({
  namespace,
  name,
  open,
  onOpenChange,
}: {
  namespace: string;
  name: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [guidance, setGuidance] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (submitting) return;
    setSubmitting(true);
    setError(null);
    try {
      await client.sendAgentRunMessage({
        namespace,
        name,
        message: buildCreatePRMessage(guidance),
      });
      onOpenChange(false);
      setGuidance("");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to request PR creation");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        onOpenChange(nextOpen);
        if (!nextOpen) {
          setGuidance("");
          setError(null);
        }
      }}
    >
      <DialogContent className="max-w-lg p-0">
        <form onSubmit={handleSubmit} className="space-y-3 p-6">
          <DialogHeader>
            <DialogTitle>Create Pull Request</DialogTitle>
            <DialogDescription>
              Optional: anything to tell AI before PR creation?
            </DialogDescription>
          </DialogHeader>

          <Textarea
            className="min-h-[140px] resize-none"
            placeholder="Optional PR guidance for title, summary, reviewers, rollout notes, or anything else."
            value={guidance}
            onChange={(e) => setGuidance(e.target.value)}
            aria-describedby={error ? "create-pr-error" : undefined}
          />

          {error && (
            <p id="create-pr-error" className="text-sm text-destructive" role="alert">
              {error}
            </p>
          )}

          <DialogFooter className="-mx-6 -mb-6 mt-2 px-6">
            <DialogClose render={<Button variant="outline" size="sm" />}>Cancel</DialogClose>
            <Button type="submit" size="sm" disabled={submitting}>
              {submitting ? "Requesting..." : "Create PR"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function CreatePRDialogButton({ namespace, name }: { namespace: string; name: string }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button size="sm" onClick={() => setOpen(true)}>
        Create PR
      </Button>
      <CreatePRDialog namespace={namespace} name={name} open={open} onOpenChange={setOpen} />
    </>
  );
}
