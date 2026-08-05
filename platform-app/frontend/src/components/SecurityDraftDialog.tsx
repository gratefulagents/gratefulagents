import { useCallback, useEffect, useRef, useState } from "react";
import { Sparkles } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger,
} from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import { FlowField } from "@/components/create-flow/create-flow";
import { client } from "@/lib/client";
import {
  SecurityDraftKind,
  SecurityDraftStatus,
  type SecurityPostScriptResource,
  type SecurityWorkflowResource,
  type SecurityWorkflowValidationError,
} from "@/rpc/platform/service_pb";

/** Maximum request length accepted by GenerateSecurityDraft. */
export const DRAFT_REQUEST_MAX = 4000;

/** Poll interval while a draft-generation run is in flight. */
const POLL_MS = 3000;

export type SecurityDraft = {
  workflow?: SecurityWorkflowResource;
  postScript?: SecurityPostScriptResource;
  validationErrors: SecurityWorkflowValidationError[];
};

/**
 * GenerateSecurityDraftDialog collects a natural-language request, launches a
 * bounded draft-generation run, and hands the parsed draft to the normal
 * editor for review. Nothing is saved here: the draft only exists in the
 * client until the operator saves it through the regular create flow.
 */
export function GenerateSecurityDraftDialog({
  kind,
  onDraft,
}: {
  kind: "workflow" | "post-script";
  onDraft: (draft: SecurityDraft) => void;
}) {
  const [open, setOpen] = useState(false);
  const [requestText, setRequestText] = useState("");
  const [runName, setRunName] = useState<string | null>(null);
  const [phase, setPhase] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);
  // Cancelling stops polling; the bounded run expires on its own runtime cap.
  const cancelled = useRef(false);

  const generating = runName !== null;
  const tooLong = requestText.length > DRAFT_REQUEST_MAX;
  const blocked = requestText.trim() === "" || tooLong || generating || starting;

  const reset = useCallback(() => {
    cancelled.current = true;
    setRunName(null);
    setPhase("");
    setError(null);
    setStarting(false);
  }, []);

  useEffect(() => {
    if (!runName) return;
    cancelled.current = false;
    let timer: number | undefined;
    const poll = async () => {
      try {
        const resp = await client.getSecurityDraft({ namespace: "", runName });
        if (cancelled.current) return;
        setPhase(resp.phase);
        if (resp.status === SecurityDraftStatus.COMPLETED) {
          setRunName(null);
          setOpen(false);
          setRequestText("");
          onDraft({
            workflow: resp.workflow,
            postScript: resp.postScript,
            validationErrors: resp.validationErrors,
          });
          return;
        }
        if (resp.status === SecurityDraftStatus.FAILED) {
          setRunName(null);
          setError(resp.error || "Draft generation failed. Try again with a more specific request.");
          return;
        }
        timer = window.setTimeout(() => void poll(), POLL_MS);
      } catch (e: unknown) {
        if (cancelled.current) return;
        setRunName(null);
        setError(e instanceof Error ? e.message : "Failed to read the draft run");
      }
    };
    timer = window.setTimeout(() => void poll(), POLL_MS);
    return () => {
      cancelled.current = true;
      if (timer) window.clearTimeout(timer);
    };
  }, [runName, onDraft]);

  async function handleGenerate() {
    if (blocked) return;
    setStarting(true);
    setError(null);
    try {
      const resp = await client.generateSecurityDraft({
        namespace: "",
        kind: kind === "workflow" ? SecurityDraftKind.WORKFLOW : SecurityDraftKind.POST_SCRIPT,
        requestText: requestText.trim(),
      });
      setPhase("pending");
      setRunName(resp.runName);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : "Failed to start draft generation");
    } finally {
      setStarting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <DialogTrigger
        render={
          <Button size="sm" variant="outline" data-testid={`generate-${kind}-draft`}>
            <Sparkles className="size-3.5" /> Generate with AI
          </Button>
        }
      />
      <DialogContent className="w-full max-w-xl" showCloseButton>
        <DialogHeader>
          <DialogTitle className="text-base">
            Draft a security {kind === "workflow" ? "workflow" : "post-script"} with AI
          </DialogTitle>
          <DialogDescription>
            A bounded generation run drafts the {kind === "workflow" ? "workflow" : "post-script"} from your
            description. The draft opens in the normal editor for review and is only saved when you save it —
            generated content is never applied automatically.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <FlowField
            id="draft-request"
            label="What should it do?"
            required
            hint={`${requestText.length}/${DRAFT_REQUEST_MAX} characters`}
          >
            <Textarea
              id="draft-request"
              value={requestText}
              onChange={(event) => setRequestText(event.target.value)}
              disabled={generating}
              className="min-h-28"
              placeholder="Deep-dive the payments service: authorization boundaries, webhook signature handling, and stored card data paths."
            />
          </FlowField>
          {tooLong && (
            <p className="text-xs text-destructive">
              Shorten the request to {DRAFT_REQUEST_MAX} characters or fewer.
            </p>
          )}
          {generating && (
            <p className="text-sm text-muted-foreground" data-testid="draft-progress">
              Generating… run {runName} is {phase || "starting"}. This usually takes a minute.
            </p>
          )}
          {error && (
            <p className="text-sm text-destructive" data-testid="draft-error">
              {error}
            </p>
          )}
        </div>
        <div className="flex justify-end gap-2 pt-2">
          <Button
            type="button"
            variant="ghost"
            onClick={() => {
              if (generating) {
                reset();
              } else {
                setOpen(false);
              }
            }}
          >
            {generating ? "Stop watching" : "Cancel"}
          </Button>
          <Button type="button" onClick={() => void handleGenerate()} disabled={blocked}>
            {generating ? "Generating…" : "Generate draft"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
