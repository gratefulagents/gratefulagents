import { useState, type FormEvent } from "react";
import { Loader2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { toast } from "@/components/ui/toaster";
import { client } from "@/lib/client";

interface CloneRepoDialogProps {
  namespace: string;
  name: string;
  resourceType: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCloned: () => void;
}

// CloneRepoDialog clones an additional git repository into the running run's
// sandbox. The repo URL is cloned with the pod's existing git credentials.
export function CloneRepoDialog({
  namespace,
  name,
  resourceType,
  open,
  onOpenChange,
  onCloned,
}: CloneRepoDialogProps) {
  const [repoUrl, setRepoUrl] = useState("");
  const [baseBranch, setBaseBranch] = useState("");
  const [submitting, setSubmitting] = useState(false);

  function reset() {
    setRepoUrl("");
    setBaseBranch("");
    setSubmitting(false);
  }

  async function handleSubmit(event: FormEvent) {
    event.preventDefault();
    const url = repoUrl.trim();
    if (!url || submitting) {
      return;
    }
    setSubmitting(true);
    try {
      const resp = await client.cloneRepository({
        namespace,
        name,
        resourceType,
        repoUrl: url,
        baseBranch: baseBranch.trim(),
      });
      toast.success(`Cloned ${resp.repository?.name ?? "repository"}`);
      onCloned();
      reset();
      onOpenChange(false);
    } catch (err) {
      toast.error("Couldn't clone repository", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (!nextOpen) reset();
        onOpenChange(nextOpen);
      }}
    >
      <DialogContent className="max-w-lg" showCloseButton>
        <form onSubmit={handleSubmit} className="space-y-5">
          <DialogHeader>
            <DialogTitle>Clone a repository</DialogTitle>
            <DialogDescription>
              Clone an additional git repository into this run's workspace. It uses the run's existing
              git credentials, so you can clone any repository they can access.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-2">
            <Label htmlFor="clone-repo-url">Repository URL</Label>
            <Input
              id="clone-repo-url"
              value={repoUrl}
              onChange={(event) => setRepoUrl(event.target.value)}
              placeholder="https://github.com/org/repo"
              autoFocus
              required
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="clone-base-branch">Branch (optional)</Label>
            <Input
              id="clone-base-branch"
              value={baseBranch}
              onChange={(event) => setBaseBranch(event.target.value)}
              placeholder="default branch"
            />
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={submitting}>
              Cancel
            </Button>
            <Button type="submit" disabled={!repoUrl.trim() || submitting}>
              {submitting && <Loader2 className="size-4 animate-spin" />}
              Clone
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
