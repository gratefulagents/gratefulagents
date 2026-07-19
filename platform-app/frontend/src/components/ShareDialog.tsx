/* eslint-disable react-hooks/set-state-in-effect */
import { useState, useEffect, useCallback, useRef } from "react";
import { create } from "@bufbuild/protobuf";
import { client } from "@/lib/client";
import { getAuthClient } from "@/lib/auth-client";
import {
  ShareResourceRequestSchema,
  ListSharesRequestSchema,
  RevokeShareRequestSchema,
  UpdateSharePermissionRequestSchema,
  type ResourceShareInfo,
} from "@/rpc/platform/service_pb";
import { SearchUsersRequestSchema, type UserSummary } from "@/rpc/auth/service_pb";
import { OwnerAvatar } from "@/components/OwnerAvatar";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { X, Loader2, UserPlus, Trash2, AlertTriangle } from "lucide-react";

interface ShareDialogProps {
  resourceType: string;
  resourceId: string;
  resourceNamespace: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function ShareDialog({
  resourceType,
  resourceId,
  resourceNamespace,
  open,
  onOpenChange,
}: ShareDialogProps) {
  const [shares, setShares] = useState<ResourceShareInfo[]>([]);
  const [loadingShares, setLoadingShares] = useState(false);
  const [query, setQuery] = useState("");
  const [suggestions, setSuggestions] = useState<UserSummary[]>([]);
  const [activeIndex, setActiveIndex] = useState(-1);
  const [selectedUser, setSelectedUser] = useState<UserSummary | null>(null);
  const [permission, setPermission] = useState("viewer");
  const [sharing, setSharing] = useState(false);
  const [error, setError] = useState("");
  const [revokeError, setRevokeError] = useState("");
  const [confirmingRevoke, setConfirmingRevoke] = useState<string | null>(null);
  const [searching, setSearching] = useState(false);
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined);
  const emailInputRef = useRef<HTMLInputElement>(null);

  const fetchShares = useCallback(async () => {
    setLoadingShares(true);
    try {
      const resp = await client.listShares(
        create(ListSharesRequestSchema, {
          resourceType,
          resourceId,
          resourceNamespace,
        }),
      );
      setShares(resp.shares);
    } catch {
      // ignore
    } finally {
      setLoadingShares(false);
    }
  }, [resourceType, resourceId, resourceNamespace]);

  useEffect(() => {
    if (open) {
      fetchShares();
      setQuery("");
      setSuggestions([]);
      setActiveIndex(-1);
      setSelectedUser(null);
      setPermission("viewer");
      setError("");
      setRevokeError("");
      setConfirmingRevoke(null);
      // Auto-focus the email input when dialog opens
      setTimeout(() => emailInputRef.current?.focus(), 100);
    }
  }, [open, fetchShares]);

  // Debounced user search
  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    if (!query || query.length < 2 || selectedUser) {
      setSuggestions([]);
      setActiveIndex(-1);
      return;
    }
    debounceRef.current = setTimeout(async () => {
      const authClient = getAuthClient();
      if (!authClient) return;
      setSearching(true);
      try {
        const resp = await authClient.searchUsers(
          create(SearchUsersRequestSchema, {
            query,
            limit: 8,
          }),
        );
        setSuggestions(resp.users);
        setActiveIndex(resp.users.length > 0 ? 0 : -1);
      } catch {
        setSuggestions([]);
        setActiveIndex(-1);
      } finally {
        setSearching(false);
      }
    }, 300);
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    };
  }, [query, selectedUser]);

  const handleShare = async () => {
    const email = selectedUser?.email || query.trim();
    if (!email) return;
    setSharing(true);
    setError("");
    try {
      await client.shareResource(
        create(ShareResourceRequestSchema, {
          resourceType,
          resourceId,
          resourceNamespace,
          sharedWithEmail: email,
          permission,
        }),
      );
      setQuery("");
      setSelectedUser(null);
      setSuggestions([]);
      setActiveIndex(-1);
      await fetchShares();
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : "Failed to share");
    } finally {
      setSharing(false);
    }
  };

  const handleRevoke = async (shareId: string) => {
    setRevokeError("");
    try {
      await client.revokeShare(create(RevokeShareRequestSchema, { shareId }));
      setConfirmingRevoke(null);
      await fetchShares();
    } catch (e: unknown) {
      setRevokeError(e instanceof Error ? e.message : "Failed to revoke access");
    }
  };

  const handleUpdatePermission = async (shareId: string, newPerm: string) => {
    setRevokeError("");
    try {
      await client.updateSharePermission(
        create(UpdateSharePermissionRequestSchema, {
          shareId,
          permission: newPerm,
        }),
      );
      await fetchShares();
    } catch (e: unknown) {
      setRevokeError(e instanceof Error ? e.message : "Failed to update permission");
    }
  };

  const selectUser = (user: UserSummary) => {
    setSelectedUser(user);
    setQuery(user.email);
    setSuggestions([]);
    setActiveIndex(-1);
  };

  const clearSelectedUser = () => {
    setSelectedUser(null);
    setQuery("");
    setActiveIndex(-1);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Share {resourceType === "agent_run" ? "Run" : "Project"}</DialogTitle>
          <DialogDescription>
            Invite people to collaborate on this {resourceType === "agent_run" ? "run" : "project"}.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          {/* User search + permission + share button */}
          <div className="flex items-end gap-2">
            <div className="relative flex-1">
              <label htmlFor="share-email" className="text-xs font-medium text-muted-foreground mb-1 block">
                Email
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
                    onClick={clearSelectedUser}
                    aria-label="Remove selected user"
                    className="ml-auto rounded text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
                  >
                    <X className="h-3 w-3" />
                  </button>
                </div>
              ) : (
                <>
                  <Input
                    id="share-email"
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
                        setActiveIndex((current) => (current <= 0 ? suggestions.length - 1 : current - 1));
                      } else if (e.key === "Enter") {
                        e.preventDefault();
                        if (activeIndex >= 0 && suggestions[activeIndex]) {
                          selectUser(suggestions[activeIndex]);
                        } else {
                          handleShare();
                        }
                      } else if (e.key === "Escape" && suggestions.length > 0) {
                        // Dismiss only the listbox; without stopPropagation the
                        // dialog's document-level Escape handler closes everything.
                        e.preventDefault();
                        e.stopPropagation();
                        setSuggestions([]);
                        setActiveIndex(-1);
                      }
                    }}
                    aria-label="Email address to share with"
                    role="combobox"
                    aria-expanded={suggestions.length > 0}
                    aria-autocomplete="list"
                    aria-controls="share-suggestions"
                    aria-haspopup="listbox"
                  />
                  {searching && (
                    <div className="absolute right-2 top-1/2 -translate-y-1/2">
                      <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />
                    </div>
                  )}
                  {suggestions.length > 0 && (
                    <div id="share-suggestions" role="listbox" className="absolute z-50 mt-1 w-full rounded-md border bg-popover shadow-md">
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
                            {user.picture && (
                              <AvatarImage src={user.picture} alt={user.name} />
                            )}
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
                </>
              )}
            </div>
            <div>
              <label className="text-xs font-medium text-muted-foreground mb-1 block">
                Role
              </label>
              <Select value={permission} onValueChange={(val) => setPermission(val ?? "viewer")}>
                <SelectTrigger className="w-[130px]" size="default" aria-label="Role">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="viewer">Viewer</SelectItem>
                  <SelectItem value="collaborator">Collaborator</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          {error && <p className="text-xs text-destructive">{error}</p>}

          <div className="flex justify-start">
            <Button onClick={handleShare} disabled={sharing || (!query.trim() && !selectedUser)} size="sm">
              {sharing ? <Loader2 className="h-4 w-4 animate-spin mr-1" /> : <UserPlus className="h-4 w-4 mr-1" />}
              Share
            </Button>
          </div>

          {/* Current shares */}
          <div className="border-t pt-3">
            <h4 className="text-xs font-medium text-muted-foreground mb-2">People with access</h4>
            {loadingShares ? (
              <div className="flex items-center gap-2 text-xs text-muted-foreground py-2">
                <Loader2 className="h-3 w-3 animate-spin" /> Loading…
              </div>
            ) : shares.length === 0 ? (
              <p className="text-xs text-muted-foreground py-2">Not shared with anyone yet.</p>
            ) : (
              <div className="space-y-2 max-h-48 overflow-y-auto">
                {shares.map((share) => (
                  <div key={share.id} className="flex items-center gap-2 text-sm">
                    <OwnerAvatar owner={share.sharedWith} />
                    <div className="flex-1 min-w-0">
                      <div className="truncate font-medium text-xs">
                        {share.sharedWith?.name || share.sharedWith?.email || "Unknown"}
                      </div>
                      {share.sharedWith?.email && share.sharedWith.name && (
                        <div className="truncate text-xs text-muted-foreground">
                          {share.sharedWith.email}
                        </div>
                      )}
                    </div>
                    <Select
                      value={share.permission}
                      onValueChange={(val) => handleUpdatePermission(share.id, val ?? "viewer")}
                    >
                      <SelectTrigger className="w-[115px] h-7 text-xs" size="sm">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="viewer">Viewer</SelectItem>
                        <SelectItem value="collaborator">Collaborator</SelectItem>
                      </SelectContent>
                    </Select>
                    {confirmingRevoke === share.id ? (
                      <div className="flex items-center gap-1">
                        <Button
                          variant="destructive"
                          size="sm"
                          className="h-7 text-xs px-2"
                          onClick={() => handleRevoke(share.id)}
                        >
                          Revoke
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          className="h-7 text-xs px-2"
                          onClick={() => setConfirmingRevoke(null)}
                        >
                          Cancel
                        </Button>
                      </div>
                    ) : (
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-7 w-7 p-0 text-muted-foreground hover:text-destructive"
                        onClick={() => setConfirmingRevoke(share.id)}
                        aria-label={`Revoke access for ${share.sharedWith?.name || share.sharedWith?.email || "user"}`}
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    )}
                  </div>
                ))}
              </div>
            )}
            {revokeError && (
              <div className="flex items-center gap-1.5 text-xs text-destructive mt-2">
                <AlertTriangle className="h-3 w-3 shrink-0" />
                {revokeError}
              </div>
            )}
          </div>

          {/* Shared by info */}
          {shares.length > 0 && shares[0].sharedBy && (
            <div className="flex items-center gap-1 text-[10px] text-muted-foreground">
              Shared by
              <Badge variant="ghost" className="text-[10px] px-1 py-0">
                {shares[0].sharedBy.name || shares[0].sharedBy.email}
              </Badge>
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
