const STORAGE_PREFIX = "gratefulagents.sidebar.hiddenProjects.v1";

function storageKey(workspaceId: string): string {
  return `${STORAGE_PREFIX}.${workspaceId || "default"}`;
}

export function sidebarProjectKey(project: { namespace: string; name: string }): string {
  return `${project.namespace}/${project.name}`;
}

export function readHiddenSidebarProjects(workspaceId: string): Set<string> {
  try {
    const parsed = JSON.parse(localStorage.getItem(storageKey(workspaceId)) ?? "[]") as unknown;
    if (!Array.isArray(parsed)) return new Set();
    return new Set(parsed.filter((value): value is string => typeof value === "string"));
  } catch {
    return new Set();
  }
}

export function writeHiddenSidebarProjects(workspaceId: string, projectKeys: Set<string>): void {
  try {
    localStorage.setItem(storageKey(workspaceId), JSON.stringify([...projectKeys].sort()));
  } catch {
    // Sidebar preferences are best-effort and must never block navigation.
  }
}
