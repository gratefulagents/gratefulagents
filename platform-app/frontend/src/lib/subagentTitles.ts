/**
 * Title disambiguation for sibling subagent tasks.
 *
 * Delegation bursts frequently share one prompt template, so every task's
 * derived title truncates to the same generic prefix ("You are auditing
 * mobile/tablet screenshots of the…"). This helper finds the shared prefix
 * across duplicate titles and surfaces the part that actually differs, so a
 * roster of six parallel tasks reads as six distinct pieces of work.
 */

export interface SubagentTitleSource {
  /** Display title, possibly truncated and identical across siblings. */
  title: string;
  /** Longer text (full prompt / description) used to find the distinguishing part. */
  detail?: string;
}

/** Shared prefixes shorter than this are kept: the titles differ early enough. */
const MIN_SHARED_PREFIX = 12;
/** Maximum characters of distinguishing suffix to surface. */
const MAX_SUFFIX = 80;

function normalize(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

function clipToWord(text: string, max: number): string {
  if (text.length <= max) return text;
  const cut = text.lastIndexOf(" ", max);
  return `${text.slice(0, cut > max / 2 ? cut : max).trimEnd()}…`;
}

/** Length of the longest common prefix across all strings. */
function commonPrefixLength(values: string[]): number {
  if (values.length === 0) return 0;
  const first = values[0];
  let len = 0;
  outer: for (; len < first.length; len++) {
    for (const v of values) {
      if (len >= v.length || v[len] !== first[len]) break outer;
    }
  }
  return len;
}

/**
 * Returns one display title per source. Unique titles pass through unchanged.
 * Groups of identical titles are rewritten to `…<distinguishing suffix>` using
 * their `detail` text; when even the details match, a numeric suffix keeps the
 * entries tellable apart.
 */
export function disambiguateSubagentTitles(sources: SubagentTitleSource[]): string[] {
  const out = sources.map((s) => s.title);

  const byTitle = new Map<string, number[]>();
  sources.forEach((s, i) => {
    const key = normalize(s.title).replace(/…+$/, "").trim();
    if (!key) return;
    const bucket = byTitle.get(key);
    if (bucket) bucket.push(i);
    else byTitle.set(key, [i]);
  });

  for (const idxs of byTitle.values()) {
    if (idxs.length < 2) continue;

    const details = idxs.map((i) => normalize(sources[i].detail || sources[i].title));
    const lcp = commonPrefixLength(details);
    // Snap back to a word boundary so every suffix starts at a full word.
    const boundary = lcp > 0 ? details[0].lastIndexOf(" ", Math.max(lcp - 1, 0)) + 1 : 0;
    const useSuffix = lcp >= MIN_SHARED_PREFIX && boundary > 0;

    const seen = new Map<string, number>();
    idxs.forEach((i, k) => {
      const rest = details[k].slice(boundary).trim();
      let next =
        useSuffix && rest && rest.length < details[k].length
          ? `…${clipToWord(rest, MAX_SUFFIX)}`
          : sources[i].title;
      const n = (seen.get(next) ?? 0) + 1;
      seen.set(next, n);
      if (n > 1) next = `${next} (${n})`;
      out[i] = next;
    });
  }

  return out;
}
