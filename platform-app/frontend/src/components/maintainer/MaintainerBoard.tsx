import { useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { toneSoft, toneText } from "@/lib/status";
import { COLUMNS, itemColumn } from "@/components/maintainer/phases";
import { WorkItemCard } from "@/components/maintainer/WorkItemCard";
import { WorkItemDrawer } from "@/components/maintainer/WorkItemDrawer";
import type { MaintainerBoardCapacity, MaintainerWorkItem } from "@/rpc/platform/service_pb";

export type MaintainerBoardProps = {
  /** All work items for this repository. */
  items: MaintainerWorkItem[];
  /** Board capacity from the API response. */
  capacity?: MaintainerBoardCapacity | undefined;
  /** Namespace for building run links. */
  namespace: string;
  /** Called after any successful command so the parent can refetch. */
  onRefetch: () => void;
};

export function MaintainerBoard({
  items,
  capacity,
  namespace,
  onRefetch,
}: MaintainerBoardProps) {
  const [openItemName, setOpenItemName] = useState<string | null>(null);
  const [shippedOpen, setShippedOpen] = useState(false);
  const openItem = items.find((item) => item.name === openItemName) ?? null;

  const byColumn = new Map(COLUMNS.map((column) => [column.id, [] as MaintainerWorkItem[]]));
  for (const item of items) byColumn.get(itemColumn(item))?.push(item);

  const activeColumns = COLUMNS.filter((column) => column.id !== "shipped");
  const shippedColumn = COLUMNS.find((column) => column.id === "shipped")!;
  const shippedItems = byColumn.get("shipped") ?? [];

  return (
    <>
      <section role="region" aria-label="Maintainer work board">
        {/* Active work is the board. Empty lanes compress so attention stays on
            actual work; when every lane is busy this becomes horizontally
            scrollable rather than crushing cards below a usable width. */}
        <div
          className={cn(
            "flex flex-col gap-3",
            "min-[900px]:flex-row min-[900px]:overflow-x-auto min-[900px]:gap-0 min-[900px]:pb-2",
          )}
        >
          {activeColumns.map((column, index) => {
            const columnItems = byColumn.get(column.id) ?? [];
            return (
              <div
                key={column.id}
                className={cn(
                  columnItems.length === 0
                    ? "min-[900px]:min-w-[116px] min-[900px]:max-w-[148px] min-[900px]:flex-[0.55]"
                    : "min-[900px]:min-w-[220px] min-[900px]:max-w-[290px] min-[900px]:flex-1",
                  index > 0 && "min-[900px]:border-l min-[900px]:border-border/40",
                )}
              >
                <div className="flex items-center gap-2 border-b border-border/40 bg-background px-3 py-2.5 min-[900px]:sticky min-[900px]:top-0 min-[900px]:z-10">
                  <h3 className={cn("flex-1 text-[12px] font-semibold", toneText[column.tone])}>
                    {column.label}
                  </h3>
                  {column.id === "in-flight" && capacity ? (
                    <span className="text-[11px] tabular-nums text-muted-foreground">
                      {capacity.activeDispatches}/{capacity.concurrencyCap}
                    </span>
                  ) : null}
                  <Badge variant="secondary" className={cn("text-[10px]", toneSoft[column.tone])}>
                    {columnItems.length}
                  </Badge>
                </div>

                <div className="space-y-2 p-3">
                  {columnItems.length === 0 ? (
                    column.id === "needs-you" ? (
                      <p className="py-2 text-center text-[11.5px] italic text-muted-foreground">
                        Nothing needs your attention
                      </p>
                    ) : null
                  ) : (
                    columnItems.map((item) => (
                      <WorkItemCard
                        key={item.name}
                        item={item}
                        namespace={namespace}
                        onAction={onRefetch}
                        onOpen={setOpenItemName}
                      />
                    ))
                  )}
                </div>
              </div>
            );
          })}
        </div>

        {/* Closed work is an archive, not another active lane. Keeping it as a
            full-width collapsed row preserves the complete lifecycle without
            squeezing the action-oriented columns or hiding it off-screen. */}
        <div className="border-t border-border/50">
          <button
            type="button"
            className="flex w-full items-center gap-2 px-3 py-2.5 text-left transition-colors hover:bg-muted/25"
            onClick={() => setShippedOpen((open) => !open)}
            aria-expanded={shippedOpen}
            aria-label={`${shippedColumn.label} (${shippedItems.length} items, ${shippedOpen ? "collapse" : "expand"})`}
          >
            {shippedOpen ? (
              <ChevronDown className="size-3.5 text-muted-foreground" />
            ) : (
              <ChevronRight className="size-3.5 text-muted-foreground" />
            )}
            <span className={cn("text-[12px] font-semibold", toneText[shippedColumn.tone])}>
              {shippedColumn.label}
            </span>
            <Badge variant="secondary" className={cn("text-[10px]", toneSoft[shippedColumn.tone])}>
              {shippedItems.length}
            </Badge>
            <span className="ml-auto text-[11px] text-muted-foreground">
              {shippedOpen ? "Hide archive" : "Show delivered and closed work"}
            </span>
          </button>
          {shippedOpen ? (
            <div className="grid gap-2 border-t border-border/40 p-3 sm:grid-cols-2 xl:grid-cols-3">
              {shippedItems.length === 0 ? (
                <p className="py-2 text-[11.5px] text-muted-foreground">No shipped work yet.</p>
              ) : (
                shippedItems.map((item) => (
                  <WorkItemCard
                    key={item.name}
                    item={item}
                    namespace={namespace}
                    onAction={onRefetch}
                    onOpen={setOpenItemName}
                  />
                ))
              )}
            </div>
          ) : null}
        </div>
      </section>

      <WorkItemDrawer
        item={openItem}
        allItems={items}
        namespace={namespace}
        onClose={() => setOpenItemName(null)}
        onAction={onRefetch}
      />
    </>
  );
}
