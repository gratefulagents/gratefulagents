/* eslint-disable react-hooks/set-state-in-effect */
import { useEffect, useState } from "react";
import { OwnerAvatar } from "@/components/OwnerAvatar";
import type { ResourceOwner } from "@/rpc/platform/service_pb";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";

export function PresenceAvatars({ viewers }: { viewers: ResourceOwner[] }) {
  // Keep a stable previous list to prevent flicker during polling updates
  const [previousViewers, setPreviousViewers] = useState<ResourceOwner[]>([]);
  useEffect(() => {
    if (viewers.length > 0) {
      setPreviousViewers(viewers);
    }
  }, [viewers]);
  const displayViewers = viewers.length > 0 ? viewers : previousViewers;

  if (!displayViewers.length) return null;

  const maxShow = 3;
  const shown = displayViewers.slice(0, maxShow);
  const overflow = displayViewers.length - maxShow;

  return (
    <div className="flex items-center -space-x-2" aria-label={`${displayViewers.length} viewer${displayViewers.length !== 1 ? "s" : ""} online`}>
      {shown.map((v) => (
        <OwnerAvatar key={v.userId} owner={v} size="sm" />
      ))}
      {overflow > 0 && (
        <Tooltip>
          <TooltipTrigger
            render={
              <span className="flex h-6 w-6 items-center justify-center rounded-full bg-muted text-[10px] font-medium border-2 border-background">
                +{overflow}
              </span>
            }
          />
          <TooltipContent>
            <p>
              {overflow} more viewer{overflow > 1 ? "s" : ""}
            </p>
          </TooltipContent>
        </Tooltip>
      )}
    </div>
  );
}
