// Kubernetes-style resource names used for MCP servers and Skills:
// lowercase alphanumerics separated by single hyphens, max 63 chars.
export const RESOURCE_NAME_RE = /^[a-z0-9]+(-[a-z0-9]+)*$/;

export const RESOURCE_NAME_MAX_LENGTH = 63;

/** Returns an error message for an invalid resource name, or null when valid. */
export function resourceNameError(name: string): string | null {
  if (!name) return null;
  if (name.length > RESOURCE_NAME_MAX_LENGTH) {
    return `Name must be at most ${RESOURCE_NAME_MAX_LENGTH} characters.`;
  }
  if (!RESOURCE_NAME_RE.test(name)) {
    return "Name must be lowercase letters, digits, and single hyphens (e.g. my-tool).";
  }
  return null;
}

/**
 * True when a configured default-deny MCPPolicy would block one of the
 * selected MCP servers (its name is missing from the policy's allow list).
 */
export function mcpPolicyBlocksServers(
  configurePolicy: boolean,
  defaultAction: string,
  allowedServers: string[],
  selectedServers: string[],
): boolean {
  return (
    configurePolicy &&
    defaultAction === "Deny" &&
    selectedServers.some((name) => !allowedServers.includes(name))
  );
}
