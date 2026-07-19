import * as React from "react";
import { ShieldCheck, Trash2, UserRound } from "lucide-react";

import { ResourceListPage } from "@/components/list-page";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { TableRowSkeleton } from "@/components/ui/list-state";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCaption,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { toast } from "@/components/ui/toaster";
import { useAuth } from "@/contexts/AuthContext";
import { getAuthClient } from "@/lib/auth-client";
import { formatPollTime } from "@/lib/format";
import type { User } from "@/rpc/auth/service_pb";

const ROLES = ["admin", "member", "viewer"] as const;

/**
 * Admin-only user management (/admin/users): every registered user with their
 * last login, plus role changes (promote to admin) and account deletion.
 */
export default function AdminUsersPage() {
  const { user: me } = useAuth();
  const [users, setUsers] = React.useState<User[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState<string | null>(null);
  const [query, setQuery] = React.useState("");
  const [pendingDelete, setPendingDelete] = React.useState<User | null>(null);
  const [busyId, setBusyId] = React.useState<string | null>(null);

  const load = React.useCallback(async () => {
    try {
      const resp = await getAuthClient().listUsers({});
      setUsers(resp.users);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    let active = true;
    getAuthClient()
      .listUsers({})
      .then(
        (resp) => {
          if (!active) return;
          setUsers(resp.users);
          setError(null);
          setLoading(false);
        },
        (e: unknown) => {
          if (!active) return;
          setError(e instanceof Error ? e.message : String(e));
          setLoading(false);
        },
      );
    return () => {
      active = false;
    };
  }, []);

  const retry = () => {
    setLoading(true);
    setError(null);
    void load();
  };

  const changeRole = async (u: User, role: string) => {
    if (role === u.role) return;
    setBusyId(u.id);
    try {
      const updated = await getAuthClient().updateUserRole({ userId: u.id, role });
      setUsers((prev) => prev.map((x) => (x.id === u.id ? updated : x)));
      toast.success(
        role === "admin"
          ? `${displayName(u)} is now an admin`
          : `${displayName(u)} is now a ${role}`,
      );
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setBusyId(null);
    }
  };

  const deleteUser = async (u: User) => {
    setBusyId(u.id);
    try {
      await getAuthClient().deleteUser({ userId: u.id });
      setUsers((prev) => prev.filter((x) => x.id !== u.id));
      toast.success(`Deleted ${displayName(u)}`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setBusyId(null);
      setPendingDelete(null);
    }
  };

  const q = query.trim().toLowerCase();
  const filtered = q
    ? users.filter((u) =>
        [u.username, u.name, u.email].some((s) => s.toLowerCase().includes(q)),
      )
    : users;

  return (
    <ResourceListPage
      title="Users"
      description="Everyone with an account on this platform. Admin only."
      query={query}
      onQuery={setQuery}
      searchPlaceholder="Search users…"
      loading={loading}
      error={error}
      onRetry={retry}
      empty={filtered.length === 0}
      skeleton={<TableRowSkeleton rows={4} />}
      emptyIcon={<UserRound />}
      emptyTitle={q ? "No matching users" : "No users"}
      emptyDescription={q ? "Try a different search." : "Nobody has signed in yet."}
    >
      <Table>
        <TableCaption className="sr-only">Registered users</TableCaption>
        <TableHeader>
          <TableRow>
            <TableHead>User</TableHead>
            <TableHead className="hidden md:table-cell">Email</TableHead>
            <TableHead>Role</TableHead>
            <TableHead className="hidden md:table-cell">Last login</TableHead>
            <TableHead className="text-right">Actions</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {filtered.map((u) => {
            const isSelf = u.id === me?.id;
            const busy = busyId === u.id;
            return (
              <TableRow key={u.id}>
                <TableCell>
                  <div className="flex items-center gap-2.5 min-w-0">
                    <Avatar className="size-7">
                      <AvatarImage src={u.picture} alt="" />
                      <AvatarFallback>
                        {(u.name || u.username || "?").slice(0, 1).toUpperCase()}
                      </AvatarFallback>
                    </Avatar>
                    <div className="min-w-0">
                      <div className="truncate text-[13px] font-medium">
                        {displayName(u)}
                        {isSelf && (
                          <span className="ml-1.5 text-[11px] text-muted-foreground">(you)</span>
                        )}
                      </div>
                      <div className="truncate font-mono text-[11px] text-muted-foreground">
                        {u.username}
                      </div>
                    </div>
                  </div>
                </TableCell>
                <TableCell className="hidden md:table-cell font-mono text-sm text-muted-foreground">
                  {u.email || "—"}
                </TableCell>
                <TableCell>
                  {u.role === "admin" ? (
                    <Badge variant="secondary" className="gap-1">
                      <ShieldCheck className="size-3" /> admin
                    </Badge>
                  ) : (
                    <Badge variant="outline">{u.role}</Badge>
                  )}
                </TableCell>
                <TableCell className="hidden md:table-cell text-sm text-muted-foreground">
                  {formatPollTime(u.lastLoginAt)}
                </TableCell>
                <TableCell className="text-right">
                  <div className="flex items-center justify-end gap-2">
                    {u.role !== "admin" && (
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={busy}
                        onClick={() => void changeRole(u, "admin")}
                      >
                        <ShieldCheck className="size-3.5" />
                        Promote to admin
                      </Button>
                    )}
                    <Select
                      value={u.role}
                      onValueChange={(role) => {
                        if (typeof role === "string") void changeRole(u, role);
                      }}
                      disabled={busy || isSelf}
                    >
                      <SelectTrigger
                        size="sm"
                        className="w-[104px]"
                        aria-label={`Role for ${displayName(u)}`}
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {ROLES.map((r) => (
                          <SelectItem key={r} value={r}>
                            {r}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      className="text-muted-foreground hover:text-destructive"
                      disabled={busy || isSelf}
                      aria-label={`Delete ${displayName(u)}`}
                      title={isSelf ? "You cannot delete your own account" : "Delete user"}
                      onClick={() => setPendingDelete(u)}
                    >
                      <Trash2 className="size-3.5" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>

      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => !open && setPendingDelete(null)}
        title={`Delete ${pendingDelete ? displayName(pendingDelete) : "user"}?`}
        description="This permanently removes the account and signs them out everywhere. Their runs and projects are not deleted."
        confirmLabel="Delete user"
        destructive
        onConfirm={() => {
          if (pendingDelete) void deleteUser(pendingDelete);
        }}
      />
    </ResourceListPage>
  );
}

function displayName(u: User): string {
  return u.name || u.username || u.email || u.id;
}
