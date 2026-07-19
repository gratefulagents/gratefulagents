/* eslint-disable react-hooks/set-state-in-effect */
import { useState, useEffect, useCallback } from "react";
import { Link } from "react-router-dom";
import { create } from "@bufbuild/protobuf";
import { FolderKanban, Bot, Users } from "lucide-react";

import { ResourceListPage } from "@/components/list-page";
import { OwnerAvatar } from "@/components/OwnerAvatar";
import { StatusBadge } from "@/components/StatusBadge";
import { ListRowSkeleton } from "@/components/ui/list-state";
import { filterByQuery } from "@/components/ui/list-search";
import { client } from "@/lib/client";
import {
  ListSharedWithMeRequestSchema,
  type SharedResource,
} from "@/rpc/platform/service_pb";

function resourceLink(r: SharedResource): string {
  const share = r.share;
  if (!share) return "/";
  if (share.resourceType === "agent_run") {
    return `/runs/${share.resourceNamespace}/${share.resourceId}`;
  }
  if (share.resourceType === "project") {
    return `/projects/${share.resourceNamespace}/${share.resourceId}`;
  }
  return "/";
}

function ResourceIcon({ type }: { type: string }) {
  if (type === "agent_run") return <Bot className="size-4 text-muted-foreground" />;
  return <FolderKanban className="size-4 text-muted-foreground" />;
}

export function SharedWithMeList() {
  const [resources, setResources] = useState<SharedResource[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");

  const fetchResources = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const resp = await client.listSharedWithMe(
        create(ListSharedWithMeRequestSchema, {}),
      );
      setResources(resp.resources);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : "Failed to load shared resources");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchResources();
  }, [fetchResources]);

  const filtered = filterByQuery(resources, query, (r) => [
    r.displayName,
    r.status,
    r.share?.resourceType,
    r.share?.resourceNamespace,
    r.share?.resourceId,
    r.share?.permission,
    r.share?.sharedBy?.name,
    r.share?.sharedBy?.email,
  ]);
  const runs = filtered.filter((r) => r.share?.resourceType === "agent_run");
  const projects = filtered.filter((r) => r.share?.resourceType === "project");

  const renderGroup = (label: string, items: SharedResource[]) => {
    if (items.length === 0) return null;
    return (
      <div className="space-y-2">
        <h2 className="text-[13px] font-medium text-muted-foreground">{label}</h2>
        <div className="overflow-hidden rounded-xl border border-border/60 bg-card/30 divide-y divide-border/40">
          {items.map((r) => (
            <Link
              key={r.share?.id ?? r.displayName}
              to={resourceLink(r)}
              className="flex items-center gap-3 px-4 py-3 transition-colors hover:bg-muted/40 focus-visible:outline-none focus-visible:bg-muted/40"
            >
              <ResourceIcon type={r.share?.resourceType ?? ""} />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="font-medium text-sm truncate">
                    {r.displayName || r.share?.resourceId}
                  </span>
                  <StatusBadge phase={r.status} />
                </div>
                {r.share?.sharedBy && (
                  <div className="flex items-center gap-1 mt-0.5 text-xs text-muted-foreground">
                    Shared by
                    <OwnerAvatar owner={r.share.sharedBy} size="sm" />
                    <span className="truncate">{r.share.sharedBy.name || r.share.sharedBy.email}</span>
                  </div>
                )}
              </div>
              <span className="shrink-0 text-xs capitalize text-muted-foreground">
                {r.share?.permission}
              </span>
            </Link>
          ))}
        </div>
      </div>
    );
  };

  return (
    <ResourceListPage
      title="Shared with me"
      description="Resources that others have shared with you."
      query={query}
      onQuery={setQuery}
      searchPlaceholder="Search shared resources…"
      loading={loading}
      error={error}
      onRetry={fetchResources}
      empty={!filtered.length}
      skeleton={<ListRowSkeleton rows={3} />}
      emptyIcon={<Users className="size-6" />}
      emptyTitle={query ? `No matches for "${query}"` : "No resources have been shared with you yet"}
      emptyDescription={
        query
          ? "Clear the search to see all shared resources."
          : "When someone shares a project or run with you, it will appear here."
      }
    >
      <div className="space-y-6">
        {renderGroup("Runs", runs)}
        {renderGroup("Projects", projects)}
      </div>
    </ResourceListPage>
  );
}
