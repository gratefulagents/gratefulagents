import { useActivityLog } from "@/hooks/useActivityLog";

export function useRunActivityLog(namespace: string, name: string, phase: string, refreshKey?: string) {
  return useActivityLog(namespace, name, phase, refreshKey);
}
