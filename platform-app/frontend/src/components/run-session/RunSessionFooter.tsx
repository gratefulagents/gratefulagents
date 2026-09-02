import { useEffect, useId, useMemo, useRef, useState, type ClipboardEvent, type Dispatch, type RefObject, type SetStateAction } from "react";
import { AnimatePresence, motion } from "framer-motion";
import { CircleStop, Clock, Loader2, Paperclip, RotateCcw, Send } from "lucide-react";

import { ImageAttachmentStrip } from "@/components/ImageAttachmentStrip";
import { ContextUsageBar } from "@/components/run-session/ContextUsageBar";
import { FileMentionMenu } from "@/components/run-session/FileMentionMenu";
import { composerFocusRing, optionId } from "@/components/run-session/composer";
import { useRunActions } from "@/components/run-session/RunActionsContext";
import { RunModelSwitcher, type RuntimeConfigUpdate } from "@/components/run-session/RunModelSwitcher";
import { SlashCommandMenu } from "@/components/run-session/SlashCommandMenu";
import { filterSlashCommands, type SlashCommand } from "@/components/run-session/slashCommands";
import { Button } from "@/components/ui/button";
import { useAutosizeTextarea } from "@/hooks/useAutosizeTextarea";
import { useWorkspaceFiles } from "@/hooks/useWorkspaceFiles";
import type { ImageAttachment, VideoAttachment } from "@/hooks/useImageAttachments";
import { getMentionQuery, matchWorkspaceFiles, type FileMatch } from "@/lib/fileMentions";
import { fade, lift } from "@/lib/motion";
import { cn } from "@/lib/utils";
import { AgentRunMessageMode, type AgentRun } from "@/rpc/platform/service_pb";

const deliveryModes = [
  { value: AgentRunMessageMode.IMMEDIATE, label: "Steer" },
  { value: AgentRunMessageMode.ENQUEUE, label: "Queue" },
] as const;

export interface RunSessionFooterAttachments {
  images: ImageAttachment[];
  videos: VideoAttachment[];
  error: string | null;
  processing: boolean;
  remove: (id: string) => void;
  addFiles: (files: FileList | File[] | null | undefined) => Promise<void>;
  onPaste: (event: ClipboardEvent) => boolean;
}

/** The composer's text, delivery mode, and send/slash handlers. */
export interface RunSessionComposer {
  reply: string;
  setReply: Dispatch<SetStateAction<string>>;
  handleSend: () => void | Promise<void>;
  sendMode: AgentRunMessageMode;
  setSendMode: Dispatch<SetStateAction<AgentRunMessageMode>>;
  slashCommands: SlashCommand[];
  onRunSlashCommand: (command: SlashCommand) => void | Promise<void>;
  /** Steer vs. Queue only differs while the agent is mid-turn; hidden otherwise. */
  showSendMode?: boolean;
  textareaRef?: RefObject<HTMLTextAreaElement | null>;
  fileInputRef: RefObject<HTMLInputElement | null>;
  attachments: RunSessionFooterAttachments;
}

/** Provider/model readout + switcher; the switcher hides when the handler is omitted. */
export interface RunSessionRuntimeConfig {
  canUpdate: boolean;
  updating: boolean;
  onUpdate?: (update: RuntimeConfigUpdate) => void | Promise<void>;
}

/** Context-window meter data; the bar hides itself when unknown. */
export interface RunSessionContextWindow {
  tokens: number | null;
  triggerTokens: number;
  targetTokens: number;
}

interface RunSessionFooterProps {
  run: AgentRun;
  isActive: boolean;
  isViewer: boolean;
  sending: boolean;
  canSendMessage: boolean;
  startupCopy: string;
  composer: RunSessionComposer;
  /** Run identity for the "@" workspace file picker. When omitted the picker is disabled. */
  namespace?: string;
  name?: string;
  resourceType?: string;
  contextWindow?: RunSessionContextWindow;
  runtimeConfig?: RunSessionRuntimeConfig;
}

export function RunSessionFooter({
  run,
  isActive,
  isViewer,
  sending,
  canSendMessage,
  startupCopy,
  composer: {
    reply,
    setReply,
    handleSend,
    sendMode,
    setSendMode,
    showSendMode = true,
    slashCommands,
    onRunSlashCommand,
    textareaRef,
    fileInputRef,
    attachments,
  },
  namespace,
  name,
  resourceType = "AgentRun",
  contextWindow,
  runtimeConfig,
}: RunSessionFooterProps) {
  const { retry, interrupt, extendRuntime } = useRunActions();
  const internalTextareaRef = useRef<HTMLTextAreaElement>(null);
  const resolvedTextareaRef = textareaRef ?? internalTextareaRef;
  useAutosizeTextarea(resolvedTextareaRef, reply, 120);
  const menuId = useId();
  const slashMenuId = `${menuId}-slash`;
  const fileMenuId = `${menuId}-files`;
  const deliveryLabelId = `${menuId}-delivery`;

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
  const anyMenuOpen = menuOpen || mentionMenuOpen;
  // The textarea is only a combobox while a listbox is actually mounted;
  // otherwise screen readers would announce a popup that never opens.
  const activeOptionId = menuOpen
    ? optionId(slashMenuId, safeActiveIndex)
    : mentionMenuOpen && fileMatches.length > 0
      ? optionId(fileMenuId, safeMentionIndex)
      : undefined;
  const showInterrupt =
    interrupt.can &&
    !reply.trim() &&
    attachments.images.length === 0 &&
    attachments.videos.length === 0 &&
    !attachments.processing;

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

  // Bottom padding: safe-area inset when present (iPhone home indicator),
  // but never less than the base padding — a plain `pb-safe` collapsed to 0
  // in browsers and pressed the footer against the viewport edge.
  return (
              <div className="shrink-0 border-t px-3 py-2 pb-[max(env(safe-area-inset-bottom),0.5rem)] md:px-4 md:py-3 md:pb-[max(env(safe-area-inset-bottom),0.75rem)]">
                {(isActive || isViewer) ? (
                  <div className="space-y-2">
                    <ImageAttachmentStrip
                      images={attachments.images}
                      videos={attachments.videos}
                      onRemove={attachments.remove}
                      className="px-0"
                    />
                    {attachments.error && (
                      <p className="text-xs text-destructive">{attachments.error}</p>
                    )}
                    <input
                      ref={fileInputRef}
                      type="file"
                      accept="image/*,video/mp4,video/quicktime,video/webm"
                      multiple
                      className="hidden"
                      onChange={(e) => {
                        void attachments.addFiles(e.target.files);
                        e.target.value = "";
                      }}
                    />
                    <div className="relative flex items-end gap-2">
                      <AnimatePresence>
                        {menuOpen && (
                          <motion.div key="slash" className="absolute bottom-full left-0 z-50 mb-2 w-full" {...lift}>
                            <SlashCommandMenu
                              id={slashMenuId}
                              commands={filteredCommands}
                              activeIndex={safeActiveIndex}
                              onHover={setActiveIndex}
                              onSelect={runCommand}
                            />
                          </motion.div>
                        )}
                        {mentionMenuOpen && (
                          <motion.div key="files" className="absolute bottom-full left-0 z-50 mb-2 w-full" {...lift}>
                            <FileMentionMenu
                              id={fileMenuId}
                              matches={fileMatches}
                              activeIndex={safeMentionIndex}
                              loading={filesLoading}
                              hasQuery={(mention?.query.length ?? 0) > 0}
                              onHover={setMentionIndex}
                              onSelect={applyMention}
                            />
                          </motion.div>
                        )}
                      </AnimatePresence>
                      <Button
                        size="icon"
                        variant="outline"
                        className={cn("size-10 md:size-8", composerFocusRing)}
                        onClick={() => fileInputRef.current?.click()}
                        disabled={sending || attachments.processing || !canSendMessage}
                        aria-label="Attach image or video"
                        title="Attach image or video"
                      >
                        <Paperclip className="size-4" />
                      </Button>
                      <textarea
                        ref={resolvedTextareaRef}
                        aria-label="Type your reply"
                        role={anyMenuOpen ? "combobox" : undefined}
                        aria-expanded={anyMenuOpen ? true : undefined}
                        aria-haspopup={anyMenuOpen ? "listbox" : undefined}
                        aria-controls={menuOpen ? slashMenuId : mentionMenuOpen ? fileMenuId : undefined}
                        aria-activedescendant={activeOptionId}
                        aria-autocomplete={anyMenuOpen ? "list" : undefined}
                        className={cn(
                          "min-h-[38px] max-h-[120px] flex-1 resize-none rounded-md border bg-background px-3 py-2 text-sm placeholder:text-muted-foreground/60",
                          composerFocusRing,
                        )}
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
                            } else if (filesLoading && e.key === "Enter" && !e.shiftKey) {
                              // The picker is still fetching; Enter here means
                              // "pick the file", not "send the message".
                              e.preventDefault();
                              return;
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
                      {/* While a turn is live and the composer is empty the
                          send slot becomes "stop this turn". Cross-fade the
                          swap so the button does not pop. */}
                      <AnimatePresence mode="wait" initial={false}>
                        {showInterrupt ? (
                          <motion.span key="interrupt" className="inline-flex" {...fade}>
                            {interrupt.pending ? (
                              <Button
                                size="sm"
                                variant="destructive"
                                className={cn("h-10 gap-1.5 md:h-8", composerFocusRing)}
                                disabled
                                aria-label="Stopping the current turn"
                                title="Waiting for the agent to stop the current turn"
                              >
                                <Loader2 className="size-4 animate-spin" />
                                Stopping…
                              </Button>
                            ) : (
                              <Button
                                size="icon"
                                variant="destructive"
                                className={cn("size-10 md:size-8", composerFocusRing)}
                                onClick={interrupt.run}
                                disabled={interrupt.busy}
                                aria-label="Stop the current turn"
                                title="Stop the current turn without stopping the run"
                              >
                                <CircleStop className="size-4" />
                              </Button>
                            )}
                          </motion.span>
                        ) : (
                          <motion.span key="send" className="inline-flex" {...fade}>
                            <Button
                              size="icon"
                              className={cn("size-10 md:size-8", composerFocusRing)}
                              onClick={handleSend}
                              disabled={
                                (!reply.trim() &&
                                  attachments.images.length === 0 &&
                                  attachments.videos.length === 0) ||
                                sending ||
                                attachments.processing ||
                                !canSendMessage
                              }
                              aria-label="Send message"
                            >
                              <Send className="size-4" />
                            </Button>
                          </motion.span>
                        )}
                      </AnimatePresence>
                    </div>
                    {interrupt.stalled && (
                      <p className="text-2xs text-muted-foreground" role="status">
                        Still running — the stop request hasn't taken effect yet. Use Stop run from the
                        header to end the run, or try again.
                      </p>
                    )}
                    <div className="flex flex-wrap items-center gap-2 text-2xs text-muted-foreground">
                      {showSendMode && (
                        <div className="flex items-center gap-1.5">
                        <span id={deliveryLabelId} className="mr-0.5">Delivery</span>
                        <div
                          role="radiogroup"
                          aria-labelledby={deliveryLabelId}
                          className="flex items-center rounded-md bg-muted p-0.5"
                        >
                          {deliveryModes.map((mode) => {
                            const checked = sendMode === mode.value;
                            return (
                              <button
                                key={mode.label}
                                type="button"
                                role="radio"
                                aria-checked={checked}
                                tabIndex={checked ? 0 : -1}
                                onClick={() => setSendMode(mode.value)}
                                onKeyDown={(e) => {
                                  if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
                                    e.preventDefault();
                                    const other = deliveryModes.find((m) => m.value !== mode.value);
                                    if (other) setSendMode(other.value);
                                  }
                                }}
                                className={cn(
                                  "rounded-sm px-2.5 py-1.5 transition-colors md:px-2 md:py-0.5",
                                  composerFocusRing,
                                  checked ? "bg-background text-foreground shadow-sm" : "hover:text-foreground",
                                )}
                              >
                                {mode.label}
                              </button>
                            );
                          })}
                          </div>
                        </div>
                      )}
                      <span className="hidden text-muted-foreground/70 md:inline">
                        <kbd className="rounded border bg-muted px-1 font-mono">/</kbd> for commands ·{" "}
                        <kbd className="rounded border bg-muted px-1 font-mono">@</kbd> for files
                      </span>
                      <span className="ml-auto flex flex-wrap items-center gap-2">
                        {runtimeConfig?.onUpdate && (
                          <RunModelSwitcher
                            run={run}
                            canUpdate={runtimeConfig.canUpdate}
                            updating={runtimeConfig.updating}
                            onUpdate={runtimeConfig.onUpdate}
                          />
                        )}
                        <ContextUsageBar
                          usedTokens={contextWindow?.tokens ?? null}
                          triggerTokens={contextWindow?.triggerTokens ?? 0}
                          targetTokens={contextWindow?.targetTokens ?? 0}
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
                    {run.phase === "Paused" && extendRuntime.can && (
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() => extendRuntime.setOpen(true)}
                        disabled={extendRuntime.busy}
                        className="h-7 gap-1.5 px-2 text-xs"
                      >
                        <Clock className="size-3.5" />
                        Extend Runtime
                      </Button>
                    )}
                    {(run.phase === "Failed" || run.phase === "Cancelled") && retry.can && (
                      <Button
                        type="button"
                        size="sm"
                        onClick={retry.run}
                        disabled={retry.busy}
                        className="h-7 gap-1.5 px-2 text-xs"
                      >
                        <RotateCcw className="size-3.5" />
                        {retry.busy ? "Retrying..." : "Retry"}
                      </Button>
                    )}
                  </div>
                )}
              </div>

  );
}
