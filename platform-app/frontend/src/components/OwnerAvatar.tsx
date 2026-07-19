import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { ResourceOwner } from "@/rpc/platform/service_pb";

export function OwnerAvatar({ owner, size = "sm" }: { owner?: ResourceOwner; size?: "sm" | "md" }) {
  if (!owner) return null;
  const initials = (owner.name || owner.email || "?").slice(0, 2).toUpperCase();
  const sizeClass = size === "sm" ? "size-6 text-xs" : "size-8 text-sm";
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Avatar className={sizeClass}>
            {owner.picture && <AvatarImage src={owner.picture} alt={owner.name || owner.email} />}
            <AvatarFallback className="text-[10px]">{initials}</AvatarFallback>
          </Avatar>
        }
      />
      <TooltipContent>
        <p className="font-medium">{owner.name || "Unknown"}</p>
        {owner.email && <p className="text-xs text-muted-foreground">{owner.email}</p>}
      </TooltipContent>
    </Tooltip>
  );
}
