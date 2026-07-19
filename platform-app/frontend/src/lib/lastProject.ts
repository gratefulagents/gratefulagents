/** Remembers the most recently used project so "New chat" can start instantly. */
const KEY = "gratefulagents.lastProject.v1";

export type ProjectRef = { namespace: string; name: string };

export function readLastProject(): ProjectRef | null {
  try {
    const raw = localStorage.getItem(KEY);
    if (!raw) return null;
    const [namespace, name] = raw.split("/");
    if (!namespace || !name) return null;
    return { namespace, name };
  } catch {
    return null;
  }
}

export function writeLastProject(ref: ProjectRef) {
  try {
    localStorage.setItem(KEY, `${ref.namespace}/${ref.name}`);
  } catch {
    /* ignore quota */
  }
}
