/* eslint-disable react-hooks/set-state-in-effect */
import { useState, useEffect, useCallback } from "react";
import { client } from "@/lib/client";
import type { NotificationInfo } from "@/rpc/platform/service_pb";
import { create } from "@bufbuild/protobuf";
import {
  ListNotificationsRequestSchema,
  MarkNotificationReadRequestSchema,
} from "@/rpc/platform/service_pb";
import { isAppFocused, notify } from "@/lib/native";
import { isTauri } from "@/lib/platform";

// Module-level so re-mounts / multiple consumers never re-notify for the
// same backend notification within a session.
const osNotifiedIds = new Set<string>();
let osPrimed = false;

/**
 * Fires OS-level notifications (Tauri only) for backend notifications that
 * are new since the last poll, but only while the app window is unfocused.
 * The first successful fetch just seeds the seen-set so the existing backlog
 * never produces a notification storm on startup.
 */
async function fireOsNotifications(notifications: NotificationInfo[]): Promise<void> {
  if (!isTauri) return;
  if (!osPrimed) {
    for (const n of notifications) osNotifiedIds.add(n.id);
    osPrimed = true;
    return;
  }
  const fresh = notifications.filter((n) => !n.read && !osNotifiedIds.has(n.id));
  for (const n of notifications) osNotifiedIds.add(n.id);
  if (fresh.length === 0) return;
  if (await isAppFocused()) return;
  if (fresh.length > 3) {
    void notify({
      title: "gratefulagents",
      body: `${fresh.length} new notifications`,
    });
  } else {
    for (const n of fresh) {
      void notify({ title: n.title || "gratefulagents", body: n.body });
    }
  }
}

interface UseNotificationsOptions {
  pollIntervalMs?: number;
  enabled?: boolean;
}

export function useNotifications({
  pollIntervalMs = 30000,
  enabled = true,
}: UseNotificationsOptions = {}) {
  const [notifications, setNotifications] = useState<NotificationInfo[]>([]);
  const [unreadCount, setUnreadCount] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchNotifications = useCallback(async () => {
    try {
      const resp = await client.listNotifications(
        create(ListNotificationsRequestSchema, { limit: 50 }),
      );
      setNotifications(resp.notifications);
      setUnreadCount(resp.unreadCount);
      setError(null);
      void fireOsNotifications(resp.notifications);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load notifications");
    } finally {
      // Background polls and mark-read refreshes stay silent — only the
      // initial fetch shows the loading state.
      setLoading(false);
    }
  }, []);

  const markRead = useCallback(
    async (notificationId?: string) => {
      try {
        await client.markNotificationRead(
          create(MarkNotificationReadRequestSchema, {
            notificationId: notificationId || "",
          }),
        );
        await fetchNotifications();
      } catch {
        // ignore
      }
    },
    [fetchNotifications],
  );

  useEffect(() => {
    if (!enabled) {
      setLoading(false);
      setError(null);
      return;
    }
    fetchNotifications();
    const interval = setInterval(fetchNotifications, pollIntervalMs);
    return () => clearInterval(interval);
  }, [fetchNotifications, pollIntervalMs, enabled]);

  return {
    notifications,
    unreadCount,
    loading,
    error,
    markRead,
    refresh: fetchNotifications,
  };
}
