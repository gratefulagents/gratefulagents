import { useEffect, useMemo, useRef, useState } from "react";
import { Download, File, FileAudio, FileImage, FileText, FileVideo, Folder, FolderPlus, History, Pencil, Plus, Upload } from "lucide-react";

import { MarkdownViewer } from "@/components/MarkdownViewer";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { downloadBlob } from "@/lib/download";
import { useProjectContent, projectContentAccept } from "@/hooks/useProjectContent";
import type { ProjectContent, ProjectContentVersion } from "@/rpc/platform/service_pb";

function textLike(item: ProjectContent): boolean {
  return item.kind === "document" || item.kind === "html" || item.mediaType.startsWith("text/") ||
    ["application/json", "application/javascript", "application/xml"].includes(item.mediaType) ||
    /\.(csv|json|txt|md|markdown|html?|svg)$/i.test(item.path);
}

function markdown(item: ProjectContent): boolean {
  return item.mediaType === "text/markdown" || /\.(md|markdown)$/i.test(item.path);
}

function basename(path: string): string {
  return path.split("/").filter(Boolean).pop() || path;
}

function duplicatePath(value: string): string {
  const slash = value.lastIndexOf("/");
  const dot = value.lastIndexOf(".");
  return dot > slash ? `${value.slice(0, dot)} copy${value.slice(dot)}` : `${value} copy`;
}

function fileIcon(item: ProjectContent) {
  if (item.kind === "folder") return <Folder className="size-4" />;
  if (item.mediaType.startsWith("image/") || /\.(png|jpe?g|gif|webp|svg)$/i.test(item.path)) return <FileImage className="size-4" />;
  if (item.mediaType.startsWith("audio/")) return <FileAudio className="size-4" />;
  if (item.mediaType.startsWith("video/")) return <FileVideo className="size-4" />;
  if (textLike(item)) return <FileText className="size-4" />;
  return <File className="size-4" />;
}

function bytes(size: bigint): string {
  const value = Number(size);
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MiB`;
}

function time(unix: bigint): string {
  return unix ? new Date(Number(unix) * 1000).toLocaleString() : "";
}

export function ProjectContentSection({
  namespace,
  projectName,
  canEdit,
}: {
  namespace: string;
  projectName: string;
  canEdit: boolean;
}) {
  const content = useProjectContent(namespace, projectName);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const folderInputRef = useRef<HTMLInputElement>(null);
  const [selected, setSelected] = useState<ProjectContent | null>(null);
  const [selectedBytes, setSelectedBytes] = useState<Uint8Array | null>(null);
  const [selectedVersion, setSelectedVersion] = useState(0);
  const [editorValue, setEditorValue] = useState("");
  const [createKind, setCreateKind] = useState<"folder" | "document" | "html" | null>(null);
  const [createPath, setCreatePath] = useState("");
  const [pathAction, setPathAction] = useState<"move" | "duplicate" | null>(null);
  const [destinationPath, setDestinationPath] = useState("");
  const [versions, setVersions] = useState<ProjectContentVersion[]>([]);
  const [showHistory, setShowHistory] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<ProjectContent | null>(null);
  const [savePending, setSavePending] = useState(false);
  const [dragging, setDragging] = useState(false);

  useEffect(() => {
    folderInputRef.current?.setAttribute("webkitdirectory", "");
  }, []);

  const getContent = content.get;
  useEffect(() => {
    if (!selected) return;
    let active = true;
    void getContent(selected.id).then((response) => {
      if (!active) return;
      setSelectedBytes(response.content);
      setSelectedVersion(response.version);
      setEditorValue(textLike(selected) ? new TextDecoder().decode(response.content) : "");
    }).catch(() => {
      if (active) setSelectedBytes(null);
    });
    return () => {
      active = false;
    };
  }, [getContent, selected]);

  const objectUrl = useMemo(
    () => selected && selectedBytes && selected.kind !== "html" && !textLike(selected)
      ? URL.createObjectURL(new Blob([selectedBytes as BlobPart], { type: selected.mediaType || "application/octet-stream" }))
      : null,
    [selected, selectedBytes],
  );
  useEffect(() => () => {
    if (objectUrl) URL.revokeObjectURL(objectUrl);
  }, [objectUrl]);

  const openItem = (item: ProjectContent) => {
    setSelected(item);
    setShowHistory(false);
  };

  const uploadFiles = (files: FileList | null) => {
    if (files?.length) void content.upload(files);
  };

  const createItem = async () => {
    if (!createKind || !createPath.trim()) return;
    const path = createKind === "document" && !/\.(md|markdown)$/i.test(createPath) ? `${createPath}.md` :
      createKind === "html" && !/\.html?$/i.test(createPath) ? `${createPath}.html` : createPath;
    await content.create({
      kind: createKind,
      path,
      mediaType: createKind === "document" ? "text/markdown" : createKind === "html" ? "text/html" : "",
      content: new TextEncoder().encode(createKind === "document" ? "# New document\n" : ""),
    });
    setCreateKind(null);
    setCreatePath("");
  };

  const saveText = async () => {
    if (!selected) return;
    const updated = await content.update({
      item: selected,
      content: new TextEncoder().encode(editorValue),
      confirmOverwrite: true,
    });
    setSelected(updated);
    setSavePending(false);
  };

  const openHistory = async (item: ProjectContent) => {
    setSelected(item);
    setShowHistory(true);
    setVersions(await content.listVersions(item));
  };

  return (
    <section aria-labelledby="project-content-heading" className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b">
        <div className="flex items-center gap-3">
          <h2 id="project-content-heading" className="border-b-2 border-foreground px-1 pb-2 text-sm font-semibold">Files &amp; artifacts</h2>
          <span className="pb-2 text-xs text-muted-foreground">{content.items.length} items</span>
        </div>
        {canEdit && (
          <div className="flex flex-wrap gap-1 pb-2">
            <Button size="sm" variant="outline" onClick={() => fileInputRef.current?.click()}>
              <Upload data-icon="inline-start" /> Upload
            </Button>
            <Button size="sm" variant="outline" onClick={() => folderInputRef.current?.click()}>Upload folder</Button>
            <Button size="sm" variant="outline" onClick={() => setCreateKind("folder")}><FolderPlus data-icon="inline-start" /> Folder</Button>
            <Button size="sm" variant="outline" onClick={() => setCreateKind("document")}><Plus data-icon="inline-start" /> Markdown</Button>
            <Button size="sm" variant="outline" onClick={() => setCreateKind("html")}>HTML</Button>
          </div>
        )}
      </div>

      <input ref={fileInputRef} className="hidden" type="file" multiple accept={projectContentAccept} onChange={(event) => { uploadFiles(event.target.files); event.target.value = ""; }} />
      <input ref={folderInputRef} className="hidden" type="file" multiple accept={projectContentAccept} onChange={(event) => { uploadFiles(event.target.files); event.target.value = ""; }} />

      {canEdit && (
        <div
          className={`rounded-lg border border-dashed px-4 py-3 text-center text-xs text-muted-foreground ${dragging ? "border-primary bg-primary/5" : ""}`}
          onDragEnter={(event) => { event.preventDefault(); setDragging(true); }}
          onDragOver={(event) => event.preventDefault()}
          onDragLeave={() => setDragging(false)}
          onDrop={(event) => { event.preventDefault(); setDragging(false); uploadFiles(event.dataTransfer.files); }}
        >
          Drop files here, or upload files/folders. Accepted: PDF, Office, CSV, JSON, text, images, audio, video, archives, and HTML. Maximum 25 MiB per file.
        </div>
      )}

      {content.uploadProgress && (
        <p role="status" className="text-xs text-muted-foreground">
          Uploading {content.uploadProgress.completed + 1} of {content.uploadProgress.total}: {content.uploadProgress.current}
        </p>
      )}
      {content.error && <p role="alert" className="text-sm text-destructive">{content.error}</p>}

      <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_minmax(280px,0.9fr)]">
        <div className="overflow-hidden rounded-lg border">
          {content.loading ? <p className="p-4 text-sm text-muted-foreground">Loading files…</p> : content.items.length === 0 ? (
            <p className="p-4 text-sm text-muted-foreground">No durable files or artifacts yet.</p>
          ) : (
            <ul aria-label="Project content" className="divide-y">
              {content.items.map((item) => (
                <li key={item.id} className={`flex items-center gap-2 px-3 py-2 ${selected?.id === item.id ? "bg-muted/50" : ""}`}>
                  <button type="button" className="flex min-w-0 flex-1 items-center gap-2 text-left" onClick={() => openItem(item)}>
                    <span className="text-muted-foreground">{fileIcon(item)}</span>
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm font-medium">{item.path}</span>
                      <span className="block text-[11px] text-muted-foreground">{item.kind} · {item.kind === "folder" ? "folder" : bytes(item.sizeBytes)} · v{item.currentVersion}</span>
                    </span>
                  </button>
                  <Button size="sm" variant="ghost" onClick={() => void openHistory(item)} aria-label={`History for ${item.path}`}><History /></Button>
                  {canEdit && (
                    <Button size="sm" variant="ghost" onClick={() => { setSelected(item); setPathAction("move"); setDestinationPath(item.path); }} aria-label={`Rename or move ${item.path}`}><Pencil /></Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="min-h-52 rounded-lg border p-3">
          {!selected ? <p className="text-sm text-muted-foreground">Select a file or artifact to preview it.</p> : showHistory ? (
            <VersionHistory item={selected} versions={versions} canEdit={canEdit} onRestore={(version) => void content.restore(selected, version).then((updated) => { setSelected(updated); void openHistory(updated); })} />
          ) : (
            <ContentViewer
              item={selected}
              content={selectedBytes}
              objectUrl={objectUrl}
              version={selectedVersion}
              canEdit={canEdit}
              editorValue={editorValue}
              onEditorChange={setEditorValue}
              onSave={() => setSavePending(true)}
              onDownload={() => { if (selectedBytes) downloadBlob(basename(selected.path), selectedBytes, selected.mediaType); }}
              onMove={() => { setPathAction("move"); setDestinationPath(selected.path); }}
              onDuplicate={() => { setPathAction("duplicate"); setDestinationPath(duplicatePath(selected.path)); }}
              onDelete={() => setDeleteTarget(selected)}
            />
          )}
        </div>
      </div>

      <PathDialog
        action={createKind ? "create" : pathAction}
        kind={createKind}
        value={createKind ? createPath : destinationPath}
        onChange={createKind ? setCreatePath : setDestinationPath}
        onClose={() => { setCreateKind(null); setPathAction(null); }}
        onSubmit={() => void (createKind ? createItem() : selected && pathAction === "move" ? content.update({ item: selected, path: destinationPath }).then((updated) => { setSelected(updated); setPathAction(null); }) : selected && content.duplicate(selected, destinationPath).then(() => setPathAction(null)))}
      />
      <ConfirmDialog
        open={savePending}
        onOpenChange={setSavePending}
        title="Save a new revision?"
        description="Saving replaces the current text and creates a new version."
        confirmLabel="Save"
        onConfirm={saveText}
      />
      <ConfirmDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title={`Delete ${deleteTarget ? basename(deleteTarget.path) : "item"}?`}
        description="This permanently deletes the file's contents and version history, freeing project storage."
        confirmLabel="Delete"
        destructive
        onConfirm={async () => { if (deleteTarget) { await content.remove(deleteTarget); if (selected?.id === deleteTarget.id) setSelected(null); } }}
      />
    </section>
  );
}

function ContentViewer({ item, content, objectUrl, version, canEdit, editorValue, onEditorChange, onSave, onDownload, onMove, onDuplicate, onDelete }: {
  item: ProjectContent;
  content: Uint8Array | null;
  objectUrl: string | null;
  version: number;
  canEdit: boolean;
  editorValue: string;
  onEditorChange: (value: string) => void;
  onSave: () => void;
  onDownload: () => void;
  onMove: () => void;
  onDuplicate: () => void;
  onDelete: () => void;
}) {
  const editable = canEdit && textLike(item) && item.kind !== "folder";
  const source = content ? new TextDecoder().decode(content) : "";
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0"><p className="truncate text-sm font-medium">{item.path}</p><p className="text-[11px] text-muted-foreground">Version {version || item.currentVersion} · {item.scanStatus || "clean"}</p></div>
        <div className="flex gap-1">
          {item.kind !== "folder" && <Button size="sm" variant="outline" onClick={onDownload}><Download data-icon="inline-start" /> Download</Button>}
          {canEdit && <Button size="sm" variant="outline" onClick={onDuplicate}>Duplicate</Button>}
          {canEdit && <Button size="sm" variant="outline" onClick={onMove}>Rename / move</Button>}
          {canEdit && <Button size="sm" variant="ghost" onClick={onDelete}>Delete</Button>}
        </div>
      </div>
      {item.kind === "folder" ? <p className="text-sm text-muted-foreground">Folder</p> : !content ? <p className="text-sm text-muted-foreground">Loading preview…</p> : editable ? (
        <div className="space-y-2">
          <Textarea aria-label={`Edit ${item.path}`} value={editorValue} onChange={(event) => onEditorChange(event.target.value)} className="min-h-52 font-mono text-xs" />
          <div className="flex justify-end"><Button size="sm" onClick={onSave}>Save</Button></div>
          {markdown(item) && <div className="border-t pt-3"><MarkdownViewer content={editorValue} /></div>}
        </div>
      ) : markdown(item) ? <MarkdownViewer content={source} /> : item.kind === "html" || /\.html?$/i.test(item.path) ? (
        <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded bg-muted p-3 text-xs">{source}</pre>
      ) : (item.mediaType.startsWith("image/") || /\.(png|jpe?g|gif|webp|svg)$/i.test(item.path)) && objectUrl ? <img src={objectUrl} alt={basename(item.path)} className="max-h-96 max-w-full object-contain" /> :
        item.mediaType.startsWith("audio/") && objectUrl ? <audio controls src={objectUrl} className="w-full" /> :
        item.mediaType.startsWith("video/") && objectUrl ? <video controls src={objectUrl} className="max-h-96 w-full" /> :
        item.mediaType === "application/pdf" || /\.pdf$/i.test(item.path) ? objectUrl && (
          <object data={objectUrl} type="application/pdf" aria-label={`Preview ${item.path}`} className="h-96 w-full border">
            <p className="p-3 text-sm text-muted-foreground">The browser cannot preview this PDF inline. Download it to open.</p>
          </object>
        ) :
        textLike(item) ? <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded bg-muted p-3 text-xs">{source}</pre> : <p className="text-sm text-muted-foreground">Preview is unavailable for this file type. Download it to open.</p>}
    </div>
  );
}

function VersionHistory({ item, versions, canEdit, onRestore }: { item: ProjectContent; versions: ProjectContentVersion[]; canEdit: boolean; onRestore: (version: number) => void }) {
  return <div className="space-y-2"><div><p className="text-sm font-medium">Version history</p><p className="text-xs text-muted-foreground">Restoring a version creates a new revision.</p></div>{versions.length === 0 ? <p className="text-sm text-muted-foreground">No versions found.</p> : <ul className="space-y-1">{versions.map((version) => <li key={version.version} className="flex items-center justify-between gap-2 rounded border px-2 py-1.5 text-xs"><span>v{version.version} · {bytes(version.sizeBytes)} · {time(version.createdAtUnix)}</span>{canEdit && version.version !== item.currentVersion && <Button size="sm" variant="outline" onClick={() => onRestore(version.version)}>Restore</Button>}</li>)}</ul>}</div>;
}

function PathDialog({ action, kind, value, onChange, onClose, onSubmit }: { action: "create" | "move" | "duplicate" | null; kind: "folder" | "document" | "html" | null; value: string; onChange: (value: string) => void; onClose: () => void; onSubmit: () => void }) {
  const title = action === "create" ? `New ${kind === "document" ? "Markdown document" : kind === "html" ? "HTML artifact" : "folder"}` : action === "move" ? "Rename or move" : "Duplicate";
  return <Dialog open={action !== null} onOpenChange={(open) => !open && onClose()}><DialogContent><DialogHeader><DialogTitle>{title}</DialogTitle><DialogDescription>Enter a project-relative path.</DialogDescription></DialogHeader><Input aria-label="Project-relative path" value={value} onChange={(event) => onChange(event.target.value)} autoFocus /><DialogFooter><Button variant="outline" onClick={onClose}>Cancel</Button><Button disabled={!value.trim()} onClick={onSubmit}>{action === "create" ? "Create" : action === "duplicate" ? "Duplicate" : "Save"}</Button></DialogFooter></DialogContent></Dialog>;
}
