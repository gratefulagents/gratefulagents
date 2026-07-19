import { useEffect, useMemo, useRef, useState, type ClipboardEvent, type Dispatch, type RefObject, type SetStateAction } from "react";
import { CircleStop, Clock, ImagePlus, RotateCcw, Send } from "lucide-react";

import { ImageAttachmentStrip } from "@/components/ImageAttachmentStrip";
import { ContextUsageBar } from "@/components/run-session/ContextUsageBar";
import { FileMentionMenu } from "@/components/run-session/FileMentionMenu";
import { RunModelSwitcher, type RuntimeConfigUpdate } from "@/components/run-session/RunModelSwitcher";
import { SlashCommandMenu } from "@/components/run-session/SlashCommandMenu";
import { filterSlashCommands, type SlashCommand } from "@/components/run-session/slashCommands";
import { Button } from "@/components/ui/button";
import { useAutosizeTextarea } from "@/hooks/useAutosizeTextarea";
import { useWorkspaceFiles } from "@/hooks/useWorkspaceFiles";
import type { ImageAttachment } from "@/hooks/useImageAttachments";
import { getMentionQuery, matchWorkspaceFiles, type FileMatch } from "@/lib/fileMentions";
import { cn } from "@/lib/utils";
import { AgentRunMessageMode, type AgentRun } from "@/rpc/platform/service_pb";

export interface RunSessionFooterAttachments {
  images: ImageAttachment[];
  error: string | null;
  remove: (id: string) => void;
  addFiles: (files: FileList | File[] | null | undefined) => Promise<void>;
  onPaste: (event: ClipboardEvent) => boolean;
}

interface RunSessionFooterProps {
  isActive: boolean;
  isViewer: boolean;
  sending: boolean;
  canSendMessage: boolean;
  startupCopy: string;
  attachments: RunSessionFooterAttachments;
  fileInputRef: RefObject<HTMLInputElement | null>;
  reply: string;
  setReply: Dispatch<SetStateAction<string>>;
  handleSend: () => void | Promise<void>;
  sendMode: AgentRunMessageMode;
  setSendMode: Dispatch<SetStateAction<AgentRunMessageMode>>;
  slashCommands: SlashCommand[];
  onRunSlashCommand: (command: SlashCommand) => void | Promise<void>;
  phase: string;
  blockedReason: string;
  canExtendRuntime: boolean;
  setExtendRuntimeOpen: Dispatch<SetStateAction<boolean>>;
  extendingRuntime: boolean;
  canRetry: boolean;
  handleRetry: () => void | Promise<void>;
  retrying: boolean;
  /** Turn is live: swap the send button for a stop-turn button while the
   * composer is empty. Stops the in-flight turn without killing the run. */
  canInterrupt?: boolean;
  interrupting?: boolean;
  onInterrupt?: () => void | Promise<void>;
  textareaRef?: RefObject<HTMLTextAreaElement | null>;
  /** Run identity for the "@" workspace file picker. When omitted the picker is disabled. */
  namespace?: string;
  name?: string;
  resourceType?: string;
  /** Context-window meter data; the bar hides itself when unknown. */
  contextTokens?: number | null;
  contextTriggerTokens?: number;
  contextTargetTokens?: number;
  /** Provider/model readout + switcher; hidden when the run is unknown. */
  run?: AgentRun;
  canUpdateRuntimeConfig?: boolean;
  updatingRuntimeConfig?: boolean;
  onUpdateRuntimeConfig?: (update: RuntimeConfigUpdate) => void | Promise<void>;
}

export function RunSessionFooter({
  isActive,
  isViewer,
  sending,
  canSendMessage,
  startupCopy,
  attachments,
  fileInputRef,
  reply,
  setReply,
  handleSend,
  sendMode,
  setSendMode,
  slashCommands,
  onRunSlashCommand,
  phase,
  blockedReason,
  canExtendRuntime,
  setExtendRuntimeOpen,
  extendingRuntime,
  canRetry,
  handleRetry,
  retrying,
  canInterrupt = false,
  interrupting = false,
  onInterrupt,
  textareaRef,
  namespace,
  name,
  resourceType = "AgentRun",
  contextTokens,
  contextTriggerTokens,
  contextTargetTokens,
  run: agentRun,
  canUpdateRuntimeConfig = false,
  updatingRuntimeConfig = false,
  onUpdateRuntimeConfig,
}: RunSessionFooterProps) {
  const run = { phase, blockedReason };
  const internalTextareaRef = useRef<HTMLTextAreaElement>(null);
  const resolvedTextareaRef = textareaRef ?? internalTextareaRef;
  useAutosizeTextarea(resolvedTextareaRef, reply, 120);

  // Reactive slash-command palette. The menu opens the moment the composer
  // starts with "/", filters as the user types, and stays dismissed (per
  // Escape) until the input no longer begins with a slash.
  const [activeIndex, setActiveIndex] = useState(0);
  const [dismissed, setDismissed] = useState(false);
  const [prevReply, setPrevReply] = useState(reply);
  const filteredCommands = useMemo(
    () => filterSlashCommands(slashCommands, reply),
    [slashCommands, reply],
  );
  const slashActive = reply.trimStart().startsWith("/");

  // Adjust derived palette state during render when the input changes: reset
  // the highlight to the top match, and end any dismissal once the slash
  // session is over. (Avoids cascading setState-in-effect.)
  if (reply !== prevReply) {
    setPrevReply(reply);
    setActiveIndex(0);
    if (!slashActive) {
      setDismissed(false);
    }
  }

  const safeActiveIndex =
    filteredCommands.length === 0 ? 0 : Math.min(activeIndex, filteredCommands.length - 1);
  const menuOpen =
    canSendMessage && !sending && slashActive && !dismissed && filteredCommands.length > 0;

  function runCommand(command: SlashCommand) {
    setReply("");
    setDismissed(false);
    void onRunSlashCommand(command);
  }

  // Reactive "@" file picker. Mirrors the slash palette but is anchored to the
  // mention token under the caret (not the start of the input), filters a cached
  // workspace file list entirely on the client, and inserts "@<path>" on select.
  const MAX_FILE_MATCHES = 20;
  const fileMentionsEnabled = Boolean(namespace && name) && canSendMessage;
  const [caret, setCaret] = useState(reply.length);
  const [mentionIndex, setMentionIndex] = useState(0);
  const [mentionDismissed, setMentionDismissed] = useState(false);

  const {
    files: workspaceFiles,
    loading: filesLoading,
    error: filesError,
    loaded: filesLoaded,
    load: loadWorkspaceFiles,
  } = useWorkspaceFiles(namespace ?? "", name ?? "", resourceType, fileMentionsEnabled);

  const mention = useMemo(() => {
    if (!fileMentionsEnabled || sending || slashActive) {
      return null;
    }
    return getMentionQuery(reply, Math.min(caret, reply.length));
  }, [fileMentionsEnabled, sending, slashActive, reply, caret]);

  const fileMatches = useMemo<FileMatch[]>(
    () => (mention ? matchWorkspaceFiles(workspaceFiles, mention.query, MAX_FILE_MATCHES) : []),
    [mention, workspaceFiles],
  );

  // Kick off the one-time file fetch the first time a mention is opened.
  useEffect(() => {
    if (mention) {
      loadWorkspaceFiles();
    }
  }, [mention, loadWorkspaceFiles]);

  // Reset the highlight and dismissal whenever the mention token changes.
  const mentionKey = mention ? `${mention.start}:${mention.query}` : null;
  const [prevMentionKey, setPrevMentionKey] = useState<string | null>(null);
  if (mentionKey !== prevMentionKey) {
    setPrevMentionKey(mentionKey);
    setMentionIndex(0);
    setMentionDismissed(false);
  }

  const safeMentionIndex =
    fileMatches.length === 0 ? 0 : Math.min(mentionIndex, fileMatches.length - 1);
  const mentionMenuOpen =
    mention !== null && !menuOpen && !mentionDismissed && !filesError && (filesLoading || filesLoaded);

  function applyMention(match: FileMatch) {
    if (!mention) {
      return;
    }
    const before = reply.slice(0, mention.start);
    const after = reply.slice(mention.end);
    const inserted = `@${match.path} `;
    const next = `${before}${inserted}${after}`;
    const nextCaret = before.length + inserted.length;
    setReply(next);
    setMentionDismissed(false);
    requestAnimationFrame(() => {
      const el = resolvedTextareaRef.current;
      if (el) {
        el.focus();
        el.selectionStart = nextCaret;
        el.selectionEnd = nextCaret;
      }
      setCaret(nextCaret);
    });
  }

  return (
              <div className="shrink-0 border-t px-3 py-2 pb-safe md:px-4 md:py-3">
                {(isActive || isViewer) ? (
                  <div className="space-y-2">
                    <ImageAttachmentStrip
                      images={attachments.images}
                      onRemove={attachments.remove}
                      className="px-0"
                    />
                    {attachments.error && (
                      <p className="text-xs text-destructive">{attachments.error}</p>
                    )}
                    <input
                      ref={fileInputRef}
                      type="file"
                      accept="image/*"
                      multiple
                      className="hidden"
                      onChange={(e) => {
                        void attachments.addFiles(e.target.files);
                        e.target.value = "";
                      }}
                    />
                    <div className="relative flex items-end gap-2">
                      {menuOpen && (
                        <SlashCommandMenu
                          commands={filteredCommands}
                          activeIndex={safeActiveIndex}
                          onHover={setActiveIndex}
                          onSelect={runCommand}
                        />
                      )}
                      {mentionMenuOpen && (
                        <FileMentionMenu
                          matches={fileMatches}
                          activeIndex={safeMentionIndex}
                          loading={filesLoading}
                          hasQuery={(mention?.query.length ?? 0) > 0}
                          onHover={setMentionIndex}
                          onSelect={applyMention}
                        />
                      )}
                      <Button
                        size="icon"
                        variant="outline"
                        className="size-10 md:size-8"
                        onClick={() => fileInputRef.current?.click()}
                        disabled={sending || !canSendMessage}
                        aria-label="Attach image"
                      >
                        <ImagePlus className="size-4" />
                      </Button>
                      <textarea
                        ref={resolvedTextareaRef}
                        aria-label="Type your reply"
                        role="combobox"
                        aria-expanded={menuOpen || mentionMenuOpen}
                        aria-controls={mentionMenuOpen ? "file-mention-menu" : "slash-command-menu"}
                        className="min-h-[38px] max-h-[120px] flex-1 resize-none rounded-md border bg-background px-3 py-2 text-sm placeholder:text-muted-foreground/60 focus:outline-none focus:ring-1 focus:ring-ring"
                        placeholder={canSendMessage ? "Type your reply, / for commands, @ for files…" : startupCopy}
                        value={reply}
                        onChange={(e) => {
                          setReply(e.target.value);
                          setCaret(e.target.selectionStart ?? e.target.value.length);
                        }}
                        onSelect={(e) => setCaret(e.currentTarget.selectionStart ?? 0)}
                        onClick={(e) => setCaret(e.currentTarget.selectionStart ?? 0)}
                        onPaste={(e) => {
                          if (attachments.onPaste(e)) e.preventDefault();
                        }}
                        onKeyDown={(e) => {
                          if (mentionMenuOpen) {
                            if (fileMatches.length > 0) {
                              if (e.key === "ArrowDown") {
                                e.preventDefault();
                                setMentionIndex((i) => (i + 1) % fileMatches.length);
                                return;
                              }
                              if (e.key === "ArrowUp") {
                                e.preventDefault();
                                setMentionIndex(
                                  (i) => (i - 1 + fileMatches.length) % fileMatches.length,
                                );
                                return;
                              }
                              if (e.key === "Enter" || e.key === "Tab") {
                                e.preventDefault();
                                const match = fileMatches[safeMentionIndex];
                                if (match) applyMention(match);
                                return;
                              }
                            }
                            if (e.key === "Escape") {
                              e.preventDefault();
                              setMentionDismissed(true);
                              return;
                            }
                          }
                          if (menuOpen) {
                            if (e.key === "ArrowDown") {
                              e.preventDefault();
                              setActiveIndex((i) => (i + 1) % filteredCommands.length);
                              return;
                            }
                            if (e.key === "ArrowUp") {
                              e.preventDefault();
                              setActiveIndex(
                                (i) => (i - 1 + filteredCommands.length) % filteredCommands.length,
                              );
                              return;
                            }
                            if (e.key === "Enter" || e.key === "Tab") {
                              e.preventDefault();
                              const command = filteredCommands[safeActiveIndex];
                              if (command) runCommand(command);
                              return;
                            }
                            if (e.key === "Escape") {
                              e.preventDefault();
                              setDismissed(true);
                              return;
                            }
                          }
                          if (e.key === "Enter" && !e.shiftKey) {
                            e.preventDefault();
                            handleSend();
                          }
                        }}
                        disabled={sending || !canSendMessage}
                      />
                      {canInterrupt && onInterrupt && !reply.trim() && attachments.images.length === 0 ? (
                        <Button
                          size="icon"
                          variant="destructive"
                          className="size-10 md:size-8"
                          onClick={onInterrupt}
                          disabled={interrupting}
                          aria-label="Stop the current turn"
                          title="Stop the current turn without stopping the run"
                        >
                          <CircleStop className="size-4" />
                        </Button>
                      ) : (
                        <Button
                          size="icon"
                          className="size-10 md:size-8"
                          onClick={handleSend}
                          disabled={
                            (!reply.trim() && attachments.images.length === 0) ||
                            sending ||
                            !canSendMessage
                          }
                          aria-label="Send message"
                        >
                          <Send className="size-4" />
                        </Button>
                      )}
                    </div>
                    <div className="flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
                      <div className="flex items-center gap-1.5">
                        <span className="mr-0.5">Delivery</span>
                        <div className="flex items-center rounded-md bg-muted p-0.5">
                          <button
                            type="button"
                            onClick={() => setSendMode(AgentRunMessageMode.IMMEDIATE)}
                            className={cn(
                              "rounded-sm px-2.5 py-1.5 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 md:px-2 md:py-0.5",
                              sendMode === AgentRunMessageMode.IMMEDIATE
                                ? "bg-background text-foreground shadow-sm"
                                : "hover:text-foreground",
                            )}
                          >
                            Steer
                          </button>
                          <button
                            type="button"
                            onClick={() => setSendMode(AgentRunMessageMode.ENQUEUE)}
                            className={cn(
                              "rounded-sm px-2.5 py-1.5 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 md:px-2 md:py-0.5",
                              sendMode === AgentRunMessageMode.ENQUEUE
                                ? "bg-background text-foreground shadow-sm"
                                : "hover:text-foreground",
                            )}
                          >
                            Queue
                          </button>
                        </div>
                      </div>
                      <span className="hidden text-muted-foreground/70 md:inline">
                        <kbd className="rounded border bg-muted px-1 font-mono">/</kbd> for commands ·{" "}
                        <kbd className="rounded border bg-muted px-1 font-mono">@</kbd> for files
                      </span>
                      <span className="ml-auto flex flex-wrap items-center gap-2">
                        {agentRun && onUpdateRuntimeConfig && (
                          <RunModelSwitcher
                            run={agentRun}
                            canUpdate={canUpdateRuntimeConfig}
                            updating={updatingRuntimeConfig}
                            onUpdate={onUpdateRuntimeConfig}
                          />
                        )}
                        <ContextUsageBar
                          usedTokens={contextTokens ?? null}
                          triggerTokens={contextTriggerTokens ?? 0}
                          targetTokens={contextTargetTokens ?? 0}
                        />
                      </span>
                    </div>
                    {!canSendMessage && (
                      <p className="py-1 text-center text-xs text-muted-foreground">
                        {isViewer ? "You have view-only access to this run." : startupCopy}
                      </p>
                    )}
                  </div>
                ) : (
                  <div className="flex items-center justify-center gap-2 py-1 text-center text-xs text-muted-foreground">
                    <span>
                      {(() => {
                        if (run.phase === "Succeeded") {
                          return "Run completed.";
                        }
                        if (run.phase === "Failed") {
                          return `Failed: ${run.blockedReason || "Unknown error"}`;
                        }
                        if (run.phase === "Paused") {
                          return "Run paused.";
                        }
                        if (run.phase === "Cancelled") {
                          return "Run stopped.";
                        }
                        return startupCopy;
                      })()}
                    </span>
                    {run.phase === "Paused" && canExtendRuntime && (
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() => setExtendRuntimeOpen(true)}
                        disabled={extendingRuntime}
                        className="h-7 gap-1.5 px-2 text-xs"
                      >
                        <Clock className="size-3.5" />
                        Extend Runtime
                      </Button>
                    )}
                    {(run.phase === "Failed" || run.phase === "Cancelled") && canRetry && (
                      <Button
                        type="button"
                        size="sm"
                        onClick={handleRetry}
                        disabled={retrying}
                        className="h-7 gap-1.5 px-2 text-xs"
                      >
                        <RotateCcw className="size-3.5" />
                        {retrying ? "Retrying..." : "Retry"}
                      </Button>
                    )}
                  </div>
                )}
              </div>

  );
}
