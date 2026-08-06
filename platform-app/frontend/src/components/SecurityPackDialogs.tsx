import { useRef, useState } from "react";
import { Download, Upload } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger,
} from "@/components/ui/dialog";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { FlowField } from "@/components/create-flow/create-flow";
import { downloadBlob } from "@/lib/download";
import { client } from "@/lib/client";
import {
  SecurityPackCollisionPolicy,
  type SecurityPackItemResult,
} from "@/rpc/platform/service_pb";

/** Largest pack the server accepts (1 MiB), checked before upload. */
const PACK_MAX_BYTES = 1 << 20;

const selectClass = "h-8 rounded-md border border-input bg-background px-2 text-sm w-full";

function CheckboxList({
  label,
  names,
  selected,
  onToggle,
  testid,
}: {
  label: string;
  names: string[];
  selected: Set<string>;
  onToggle: (name: string) => void;
  testid: string;
}) {
  if (names.length === 0) return null;
  return (
    <div className="space-y-1.5">
      <p className="text-xs font-medium text-muted-foreground">{label}</p>
      <div className="flex flex-wrap gap-2" data-testid={testid}>
        {names.map((name) => (
          <label key={name} className="flex items-center gap-1.5 rounded-md border px-2 py-1 text-sm">
            <input
              type="checkbox"
              checked={selected.has(name)}
              onChange={() => onToggle(name)}
              aria-label={`${label}: ${name}`}
            />
            <span className="font-mono text-[13px]">{name}</span>
          </label>
        ))}
      </div>
    </div>
  );
}

/**
 * ExportSecurityPackDialog exports selected library resources and scan
 * configurations as a portable JSON pack. The server strips every credential
 * and secret reference before the document is produced.
 */
export function ExportSecurityPackDialog({
  workflows,
  rankers,
  postScripts,
  scanConfigs,
}: {
  workflows: string[];
  rankers: string[];
  postScripts: string[];
  scanConfigs: string[];
}) {
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState<Record<string, Set<string>>>({
    workflows: new Set(),
    rankers: new Set(),
    postScripts: new Set(),
    scanConfigs: new Set(),
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const toggle = (group: string) => (name: string) => {
    setSelected((prev) => {
      const next = new Set(prev[group]);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return { ...prev, [group]: next };
    });
  };
  const total =
    selected.workflows.size + selected.rankers.size + selected.postScripts.size + selected.scanConfigs.size;

  async function handleExport() {
    setBusy(true);
    setError(null);
    try {
      const resp = await client.exportSecurityPack({
        namespace: "",
        workflows: [...selected.workflows],
        rankers: [...selected.rankers],
        postScripts: [...selected.postScripts],
        scanConfigs: [...selected.scanConfigs],
      });
      downloadBlob(resp.filename || "security-pack.json", resp.data, "application/json");
      setOpen(false);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : "Failed to export the pack");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger
        render={
          <Button size="sm" variant="outline" data-testid="export-pack">
            <Download className="size-3.5" /> Export pack
          </Button>
        }
      />
      <DialogContent className="w-full max-w-xl" showCloseButton>
        <DialogHeader>
          <DialogTitle className="text-base">Export a security pack</DialogTitle>
          <DialogDescription>
            Packs carry workflow, ranker, post-script, and scan configuration definitions. Credentials and
            secret references are always stripped, so a pack is safe to share or check into version control.
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-[52vh] space-y-3 overflow-y-auto">
          <CheckboxList label="Workflows" names={workflows} selected={selected.workflows} onToggle={toggle("workflows")} testid="export-workflows" />
          <CheckboxList label="Rankers" names={rankers} selected={selected.rankers} onToggle={toggle("rankers")} testid="export-rankers" />
          <CheckboxList label="Post-scripts" names={postScripts} selected={selected.postScripts} onToggle={toggle("postScripts")} testid="export-post-scripts" />
          <CheckboxList label="Scan configurations" names={scanConfigs} selected={selected.scanConfigs} onToggle={toggle("scanConfigs")} testid="export-scans" />
          {workflows.length + rankers.length + postScripts.length + scanConfigs.length === 0 && (
            <p className="text-sm text-muted-foreground">There is nothing to export yet.</p>
          )}
          {error && <p className="text-sm text-destructive">{error}</p>}
        </div>
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button type="button" onClick={() => void handleExport()} disabled={busy || total === 0}>
            {busy ? "Exporting…" : `Export ${total || ""}`.trim()}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

/**
 * readFileBytes reads a selected file as bytes. `File.arrayBuffer` is not
 * available in every environment (notably jsdom), so FileReader is used as a
 * fallback.
 */
async function readFileBytes(file: File): Promise<Uint8Array> {
  if (typeof file.arrayBuffer === "function") {
    return new Uint8Array(await file.arrayBuffer());
  }
  const text = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(reader.error ?? new Error("failed to read file"));
    reader.onload = () => resolve(String(reader.result ?? ""));
    reader.readAsText(file);
  });
  return new TextEncoder().encode(text);
}

function actionBadge(item: SecurityPackItemResult) {
  const variant =
    item.action === "failed" ? "destructive" : item.action === "skipped" ? "outline" : "secondary";
  return <Badge variant={variant}>{item.action}</Badge>;
}

/**
 * ImportSecurityPackDialog validates a pack (dry run) and shows per-item
 * outcomes before anything is created. Applying uses the same validation the
 * manual editors use.
 */
export function ImportSecurityPackDialog({ onImported }: { onImported: () => void }) {
  const [open, setOpen] = useState(false);
  const [data, setData] = useState<Uint8Array | null>(null);
  const [filename, setFilename] = useState("");
  const [policy, setPolicy] = useState<SecurityPackCollisionPolicy>(SecurityPackCollisionPolicy.FAIL);
  const [items, setItems] = useState<SecurityPackItemResult[] | null>(null);
  const [applied, setApplied] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  function reset() {
    setData(null);
    setFilename("");
    setItems(null);
    setApplied(false);
    setError(null);
    setPolicy(SecurityPackCollisionPolicy.FAIL);
    if (fileRef.current) fileRef.current.value = "";
  }

  async function handleFile(event: React.ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    setItems(null);
    setApplied(false);
    setError(null);
    if (!file) {
      setData(null);
      return;
    }
    if (file.size > PACK_MAX_BYTES) {
      setData(null);
      setError(`Packs are limited to ${PACK_MAX_BYTES} bytes.`);
      return;
    }
    setFilename(file.name);
    try {
      setData(await readFileBytes(file));
    } catch {
      setData(null);
      setError("Could not read the selected file.");
    }
  }

  async function run(apply: boolean) {
    if (!data) return;
    setBusy(true);
    setError(null);
    try {
      const resp = await client.importSecurityPack({ namespace: "", data, apply, collisionPolicy: policy });
      setItems(resp.items);
      setApplied(resp.applied);
      if (resp.applied) onImported();
    } catch (e: unknown) {
      setItems(null);
      setError(e instanceof Error ? e.message : "Failed to import the pack");
    } finally {
      setBusy(false);
    }
  }

  const hasFailures = (items ?? []).some((item) => item.action === "failed");

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
          <Button size="sm" variant="outline" data-testid="import-pack">
            <Upload className="size-3.5" /> Import pack
          </Button>
        }
      />
      <DialogContent className="w-full max-w-2xl" showCloseButton>
        <DialogHeader>
          <DialogTitle className="text-base">Import a security pack</DialogTitle>
          <DialogDescription>
            Every item is validated exactly like manual authoring. Review the dry run before applying;
            credentials are never carried by a pack, so imported scans need their own credentials.
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-[52vh] space-y-3 overflow-y-auto">
          <FlowField id="pack-file" label="Pack file" required>
            <input
              id="pack-file"
              ref={fileRef}
              type="file"
              accept="application/json,.json"
              onChange={(event) => void handleFile(event)}
              className="text-sm"
            />
          </FlowField>
          <FlowField id="pack-policy" label="If a name already exists">
            <select
              id="pack-policy"
              className={selectClass}
              value={String(policy)}
              onChange={(event) => setPolicy(Number(event.target.value) as SecurityPackCollisionPolicy)}
            >
              <option value={String(SecurityPackCollisionPolicy.FAIL)}>Fail the item</option>
              <option value={String(SecurityPackCollisionPolicy.SKIP)}>Skip the item</option>
              <option value={String(SecurityPackCollisionPolicy.RENAME)}>Import under a new name</option>
            </select>
          </FlowField>
          {error && <p className="text-sm text-destructive">{error}</p>}
          {items && (
            <div className="space-y-2" data-testid="import-results">
              <p className="text-sm text-muted-foreground">
                {applied ? "Import applied." : "Dry run — nothing has been created yet."}
              </p>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Kind</TableHead>
                    <TableHead>Name</TableHead>
                    <TableHead>Result</TableHead>
                    <TableHead>Detail</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {items.map((item, index) => (
                    <TableRow key={`${item.kind}-${item.name}-${index}`}>
                      <TableCell className="text-[13px]">{item.kind}</TableCell>
                      <TableCell className="font-mono text-[13px]">
                        {item.finalName && item.finalName !== item.name ? `${item.name} → ${item.finalName}` : item.name}
                      </TableCell>
                      <TableCell>{actionBadge(item)}</TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {item.error}
                        {item.validationErrors.map((v) => (
                          <span key={v.field} className="block">
                            {v.field}: {v.message}
                          </span>
                        ))}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </div>
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
            Close
          </Button>
          <Button type="button" variant="outline" onClick={() => void run(false)} disabled={!data || busy}>
            {busy ? "Checking…" : "Dry run"}
          </Button>
          <Button
            type="button"
            onClick={() => void run(true)}
            disabled={!data || busy || items === null || hasFailures || applied}
            title={hasFailures ? "Fix the failing items before applying" : undefined}
          >
            Apply import
          </Button>
        </div>
        {filename && <p className="pt-1 text-xs text-muted-foreground">Selected: {filename}</p>}
      </DialogContent>
    </Dialog>
  );
}
