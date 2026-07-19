/* eslint-disable react-hooks/set-state-in-effect */
import { useState, useEffect, useCallback } from "react";
import { client } from "@/lib/client";
import type { ResourceOwner } from "@/rpc/platform/service_pb";
import { create } from "@bufbuild/protobuf";
import {
  PresenceHeartbeatRequestSchema,
  GetPresenceRequestSchema,
} from "@/rpc/platform/service_pb";

export function usePresence(
  resourceType: string,
  resourceId: string,
  resourceNamespace: string,
) {
  const [viewers, setViewers] = useState<ResourceOwner[]>([]);

  const fetchViewers = useCallback(async () => {
    if (!resourceId) return;
    try {
      const resp = await client.getPresence(
        create(GetPresenceRequestSchema, {
          resourceType,
          resourceId,
          resourceNamespace,
        }),
      );
      setViewers(resp.viewers);
    } catch {
      // Presence failures are non-critical
    }
  }, [resourceType, resourceId, resourceNamespace]);

  const sendHeartbeat = useCallback(async () => {
    if (!resourceId) return;
    try {
      await client.sendPresenceHeartbeat(
        create(PresenceHeartbeatRequestSchema, {
          resourceType,
          resourceId,
          resourceNamespace,
        }),
      );
    } catch {
      // Heartbeat failures are non-critical
    }
  }, [resourceType, resourceId, resourceNamespace]);

  useEffect(() => {
    if (!resourceId) return;

    sendHeartbeat();
    fetchViewers();

    const heartbeatInterval = setInterval(sendHeartbeat, 30000);
    const viewerInterval = setInterval(fetchViewers, 30000);

    return () => {
      clearInterval(heartbeatInterval);
      clearInterval(viewerInterval);
    };
  }, [resourceId, sendHeartbeat, fetchViewers]);

  return { viewers };
}
