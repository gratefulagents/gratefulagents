import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Users } from "lucide-react";

const permissionDescriptions: Record<string, string> = {
  viewer: "You have view-only access to this resource",
  collaborator: "You can view and collaborate on this resource",
};

export function SharedBadge({ permission }: { permission: string }) {
  if (!permission || permission === "owner" || permission === "admin")
    return null;
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Badge variant="outline" className="text-xs gap-1" aria-label={`Shared with you as ${permission}`}>
            <Users className="h-3 w-3" />
            Shared · {permission}
          </Badge>
        }
      />
      <TooltipContent>
        <p>{permissionDescriptions[permission] || `You have ${permission} access to this resource`}</p>
      </TooltipContent>
    </Tooltip>
  );
}
