import { useActivityLog, type UseActivityLogOptions } from "@/hooks/useActivityLog";

export function useRunActivityLog(
  namespace: string,
  name: string,
  phase: string,
  refreshKey?: string,
  options?: UseActivityLogOptions,
) {
  return useActivityLog(namespace, name, phase, refreshKey, options);
}
