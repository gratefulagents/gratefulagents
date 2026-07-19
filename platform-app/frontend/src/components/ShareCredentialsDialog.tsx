/* eslint-disable react-hooks/set-state-in-effect */
import { useEffect, useMemo, useRef, useState } from "react";
import { create } from "@bufbuild/protobuf";
import { Loader2, Send, X } from "lucide-react";

import { client } from "@/lib/client";
import { getAuthClient } from "@/lib/auth-client";
import { ShareMyCredentialsRequestSchema } from "@/rpc/platform/service_pb";
import { SearchUsersRequestSchema, type UserSummary } from "@/rpc/auth/service_pb";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toaster";

export interface ShareableCredential {
  /** Credential name sent to the server: "anthropic", "openai", "copilot", "github", or an integration name. */
  id: string;
  label: string;
  /** What is saved for this credential, e.g. "API key + OAuth". */
  detail: string;
}

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

// ShareCredentialsDialog copies some of the caller's saved credentials to
// another user: pick a person (same search-by-email flow as resource sharing),
// tick the credentials to send, and the server duplicates the secret material
// into their personal namespace.
export function ShareCredentialsDialog({
  open,
  onOpenChange,
  credentials,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  credentials: ShareableCredential[];
}) {
  const [query, setQuery] = useState("");
  const [suggestions, setSuggestions] = useState<UserSummary[]>([]);
  const [activeIndex, setActiveIndex] = useState(-1);
  const [selectedUser, setSelectedUser] = useState<UserSummary | null>(null);
  const [searching, setSearching] = useState(false);
  const [selected, setSelected] = useState<Record<string, boolean>>({});
  const [sharing, setSharing] = useState(false);
  const [error, setError] = useState("");
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined);
  const emailInputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (open) {
      setQuery("");
      setSuggestions([]);
      setActiveIndex(-1);
      setSelectedUser(null);
      setSelected({});
      setError("");
      setTimeout(() => emailInputRef.current?.focus(), 100);
    }
  }, [open]);

  // Debounced user search (same pattern as ShareDialog). The cleanup marks any
  // in-flight request stale so a slow response for an old query can never
  // repopulate (and let Enter select) suggestions for the current one — in this
  // flow a mis-selected recipient would receive copied secrets.
  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    if (!query || query.length < 2 || selectedUser) {
      setSuggestions([]);
      setActiveIndex(-1);
      return;
    }
    let stale = false;
    debounceRef.current = setTimeout(async () => {
      const authClient = getAuthClient();
      if (!authClient) return;
      setSearching(true);
      try {
        const resp = await authClient.searchUsers(
          create(SearchUsersRequestSchema, { query, limit: 8 }),
        );
        if (stale) return;
        setSuggestions(resp.users);
        setActiveIndex(resp.users.length > 0 ? 0 : -1);
      } catch {
        if (stale) return;
        setSuggestions([]);
        setActiveIndex(-1);
      } finally {
        // A stale request must not clear the spinner of a newer in-flight one.
        if (!stale) setSearching(false);
      }
    }, 300);
    return () => {
      stale = true;
      if (debounceRef.current) clearTimeout(debounceRef.current);
    };
  }, [query, selectedUser]);

  const chosen = useMemo(
    () => credentials.filter((c) => selected[c.id]).map((c) => c.id),
    [credentials, selected],
  );

  const recipientEmail = selectedUser?.email || query.trim();
  const recipientValid = EMAIL_RE.test(recipientEmail);

  const handleShare = async () => {
    const email = recipientEmail;
    if (!recipientValid || chosen.length === 0) return;
    setSharing(true);
    setError("");
    try {
      const resp = await client.shareMyCredentials(
        create(ShareMyCredentialsRequestSchema, {
          targetEmail: email,
          credentials: chosen,
        }),
      );
      toast.success(
        `Copied ${resp.shared.length} credential${resp.shared.length === 1 ? "" : "s"} to ${email}`,
      );
      onOpenChange(false);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : "Failed to share credentials");
    } finally {
      setSharing(false);
    }
  };

  const selectUser = (user: UserSummary) => {
    setSelectedUser(user);
    setQuery(user.email);
    setSuggestions([]);
    setActiveIndex(-1);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Share credentials</DialogTitle>
          <DialogDescription>
            Copy your saved provider credentials to a teammate. They get their own
            copy in their account, replacing any credential they already saved
            with the same name.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          {/* Recipient */}
          <div className="relative">
            <label
              htmlFor="share-creds-email"
              className="text-xs font-medium text-muted-foreground mb-1 block"
            >
              Send to
            </label>
            {selectedUser ? (
              <div className="flex items-center gap-2 rounded-md border px-3 py-2 text-sm">
                <Avatar className="h-5 w-5">
                  {selectedUser.picture && (
                    <AvatarImage src={selectedUser.picture} alt={selectedUser.name} />
                  )}
                  <AvatarFallback className="text-[9px]">
                    {(selectedUser.name || selectedUser.email).slice(0, 2).toUpperCase()}
                  </AvatarFallback>
                </Avatar>
                <span className="truncate">{selectedUser.email}</span>
                <button
                  type="button"
                  onClick={() => {
                    setSelectedUser(null);
                    setQuery("");
                    setActiveIndex(-1);
                  }}
                  aria-label="Remove selected user"
                  className="ml-auto rounded text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
                >
                  <X className="h-3 w-3" />
                </button>
              </div>
            ) : (
              <>
                <Input
                  id="share-creds-email"
                  ref={emailInputRef}
                  placeholder="Search by email or name…"
                  value={query}
                  onChange={(e) => {
                    setQuery(e.target.value);
                    setActiveIndex(-1);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "ArrowDown" && suggestions.length > 0) {
                      e.preventDefault();
                      setActiveIndex((current) => (current + 1) % suggestions.length);
                    } else if (e.key === "ArrowUp" && suggestions.length > 0) {
                      e.preventDefault();
                      setActiveIndex((current) =>
                        current <= 0 ? suggestions.length - 1 : current - 1,
                      );
                    } else if (e.key === "Enter") {
                      e.preventDefault();
                      if (activeIndex >= 0 && suggestions[activeIndex]) {
                        selectUser(suggestions[activeIndex]);
                      } else if (EMAIL_RE.test(query.trim())) {
                        // Confirm a typed-out email that has no matching
                        // suggestion selected: dismiss the list, keep the query.
                        setSuggestions([]);
                        setActiveIndex(-1);
                      }
                    } else if (e.key === "Escape" && suggestions.length > 0) {
                      e.preventDefault();
                      e.stopPropagation();
                      setSuggestions([]);
                      setActiveIndex(-1);
                    }
                  }}
                  aria-label="Email address to share credentials with"
                  role="combobox"
                  aria-expanded={suggestions.length > 0}
                  aria-autocomplete="list"
                  aria-controls="share-creds-suggestions"
                  aria-haspopup="listbox"
                />
                {searching && (
                  <div className="absolute right-2 top-[30px]">
                    <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />
                  </div>
                )}
                {suggestions.length > 0 && (
                  <div
                    id="share-creds-suggestions"
                    role="listbox"
                    className="absolute z-50 mt-1 w-full rounded-md border bg-popover shadow-md"
                  >
                    {suggestions.map((user, index) => (
                      <button
                        key={user.id}
                        type="button"
                        role="option"
                        aria-selected={index === activeIndex}
                        className={`flex w-full items-center gap-2 px-3 py-2 text-sm hover:bg-muted ${index === activeIndex ? "bg-muted" : ""}`}
                        onMouseEnter={() => setActiveIndex(index)}
                        onClick={() => selectUser(user)}
                      >
                        <Avatar className="h-5 w-5">
                          {user.picture && <AvatarImage src={user.picture} alt={user.name} />}
                          <AvatarFallback className="text-[9px]">
                            {(user.name || user.email).slice(0, 2).toUpperCase()}
                          </AvatarFallback>
                        </Avatar>
                        <div className="text-left min-w-0">
                          <div className="truncate font-medium">{user.name}</div>
                          <div className="truncate text-xs text-muted-foreground">
                            {user.email}
                          </div>
                        </div>
                      </button>
                    ))}
                  </div>
                )}
                {query.trim() !== "" && !recipientValid && (
                  <p className="mt-1 text-[11px] text-muted-foreground">
                    Enter the recipient&apos;s full email address
                  </p>
                )}
              </>
            )}
          </div>

          {/* Credential picker */}
          <div>
            <div className="text-xs font-medium text-muted-foreground mb-1">
              Credentials to copy
            </div>
            {credentials.length === 0 ? (
              <p className="text-xs text-muted-foreground py-1">
                No saved credentials yet — add them above first.
              </p>
            ) : (
              <div className="space-y-1 rounded-md border p-2">
                {credentials.map((cred) => (
                  <label
                    key={cred.id}
                    className="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm hover:bg-muted/60"
                  >
                    <input
                      type="checkbox"
                      className="size-3.5 accent-primary"
                      checked={!!selected[cred.id]}
                      onChange={(e) =>
                        setSelected((current) => ({ ...current, [cred.id]: e.target.checked }))
                      }
                    />
                    <span className="font-medium">{cred.label}</span>
                    <span className="ml-auto text-xs text-muted-foreground">{cred.detail}</span>
                  </label>
                ))}
              </div>
            )}
          </div>

          <p className="text-[11px] leading-relaxed text-muted-foreground">
            The recipient can use these credentials for their own runs and they are
            copied as-is — usage counts against the same provider account. Copies
            don't stay in sync afterwards.
          </p>

          {error && (
            <p className="text-xs text-destructive" role="alert">
              {error}
            </p>
          )}

          <div className="flex justify-end">
            <Button
              size="sm"
              onClick={() => void handleShare()}
              disabled={sharing || chosen.length === 0 || !recipientValid}
            >
              {sharing ? (
                <Loader2 className="mr-1 h-4 w-4 animate-spin" />
              ) : (
                <Send className="mr-1 h-4 w-4" />
              )}
              Copy {chosen.length > 0 ? `${chosen.length} ` : ""}credential
              {chosen.length === 1 ? "" : "s"}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
