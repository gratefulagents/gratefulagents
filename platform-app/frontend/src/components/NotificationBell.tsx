import { Bell, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { useNotifications } from "@/hooks/useNotifications";
import { useAuth } from "@/contexts/AuthContext";
import { useNavigate } from "react-router-dom";
import { useNow } from "@/hooks/useNow";
import { formatAge } from "@/lib/format";

export function NotificationBell() {
  const { isAuthenticated } = useAuth();
  const { notifications, unreadCount, loading, error, markRead, refresh } = useNotifications({
    enabled: isAuthenticated,
  });
  const navigate = useNavigate();
  const now = useNow();

  if (!isAuthenticated) return null;

  return (
    <Popover>
      <PopoverTrigger
        render={
          <Button
            variant="ghost"
            size="icon"
            className="relative size-10 md:size-8"
            aria-label={unreadCount > 0 ? `Notifications (${unreadCount} unread)` : "Notifications"}
          >
            <Bell className="size-4" />
            {unreadCount > 0 && (
              <span
                className="absolute -top-1 -right-1 flex h-4 w-4 items-center justify-center rounded-full bg-destructive text-[10px] text-destructive-foreground"
                aria-hidden="true"
              >
                {unreadCount > 9 ? "9+" : unreadCount}
              </span>
            )}
          </Button>
        }
      />
      <PopoverContent className="w-80 p-0" align="end" aria-label="Notifications">
        <div className="flex items-center justify-between border-b p-3">
          <h4 className="text-sm font-semibold">Notifications</h4>
          {unreadCount > 0 && (
            <Button
              variant="ghost"
              size="sm"
              className="text-xs"
              onClick={() => markRead()}
            >
              Mark all read
            </Button>
          )}
        </div>
        <div className="max-h-80 overflow-y-auto" role="group" aria-label="Notification list">
          {loading ? (
            <div className="flex items-center justify-center gap-2 p-6 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
              Loading…
            </div>
          ) : error ? (
            <div className="flex flex-col items-center gap-3 p-6 text-center">
              <p className="text-sm font-medium text-foreground">Couldn't load notifications</p>
              <Button variant="outline" size="sm" onClick={() => void refresh()}>
                Retry
              </Button>
            </div>
          ) : notifications.length === 0 ? (
            <div className="flex flex-col items-center gap-2 p-6 text-center">
              <Bell className="h-8 w-8 text-muted-foreground/30" />
              <p className="text-sm text-muted-foreground">
                No notifications
              </p>
            </div>
          ) : (
            notifications.map((n) => (
              <button
                key={n.id}
                type="button"
                className={`w-full text-left p-3 border-b last:border-0 transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/60 ${!n.read ? "bg-muted/30" : ""}`}
                aria-label={`${!n.read ? "Unread: " : ""}${n.title}`}
                onClick={() => {
                  if (!n.read) markRead(n.id);
                  if (n.resourceType === "agent_run" && n.resourceId) {
                    navigate(
                      `/runs/${n.resourceNamespace}/${n.resourceId}`,
                    );
                  }
                }}
              >
                <p className="text-sm font-medium">{n.title}</p>
                {n.body && (
                  <p className="text-xs text-muted-foreground mt-0.5">
                    {n.body}
                  </p>
                )}
                {n.createdAt && (
                  <p className="text-xs text-muted-foreground mt-1">
                    {formatAge(n.createdAt.seconds, now)} ago
                  </p>
                )}
              </button>
            ))
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
