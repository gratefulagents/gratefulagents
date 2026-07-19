export const BACKOFF_BASE_MS = 1000;
export const BACKOFF_CAP_MS = 30_000;

/**
 * Full-jitter exponential backoff: delay = random(0, min(cap, base * 2^attempt)).
 * Callers should reset `attempt` to 0 after a successfully received message.
 */
export function backoffDelayMs(attempt: number, random: () => number = Math.random): number {
  const ceiling = Math.min(BACKOFF_CAP_MS, BACKOFF_BASE_MS * 2 ** Math.max(0, attempt));
  return Math.floor(random() * ceiling);
}
