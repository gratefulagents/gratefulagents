// Utilities for the chat composer "@" file picker: detecting the mention token
// under the caret and fuzzy-ranking workspace file paths against a query.

export interface MentionQuery {
  /** Text typed after "@" up to the caret (may be empty right after "@"). */
  query: string;
  /** Index of the "@" character in the source text. */
  start: number;
  /** Caret index (exclusive end of the mention token). */
  end: number;
}

export interface FileMatch {
  path: string;
  score: number;
  /** Indices into `path` that matched the query, for highlighting. */
  positions: number[];
}

// Characters that may appear immediately before "@" for it to start a mention.
// This stops us from treating the "@" in "user@host" as a file mention.
const BOUNDARY_BEFORE = /[\s([{<"'`]/;

/**
 * getMentionQuery returns the active "@" mention token at the caret, or null.
 * The "@" must begin a word (start of input or after whitespace/bracket), and
 * the span between it and the caret must not contain whitespace.
 */
export function getMentionQuery(text: string, caret: number): MentionQuery | null {
  if (caret < 0 || caret > text.length) {
    return null;
  }
  for (let i = caret - 1; i >= 0; i--) {
    const ch = text[i];
    if (ch === "@") {
      const prev = i > 0 ? text[i - 1] : "";
      if (prev === "" || BOUNDARY_BEFORE.test(prev)) {
        return { query: text.slice(i + 1, caret), start: i, end: caret };
      }
      return null;
    }
    if (/\s/.test(ch)) {
      return null;
    }
  }
  return null;
}

function basename(path: string): string {
  const i = path.lastIndexOf("/");
  return i === -1 ? path : path.slice(i + 1);
}

function depth(path: string): number {
  let count = 0;
  for (let i = 0; i < path.length; i++) {
    if (path[i] === "/") count++;
  }
  return count;
}

// fuzzyMatch performs a greedy subsequence match of query against text,
// returning a heuristic score and the matched character positions, or null
// when the query is not a subsequence of the text.
function fuzzyMatch(text: string, query: string): { score: number; positions: number[] } | null {
  const lowerText = text.toLowerCase();
  const lowerQuery = query.toLowerCase();
  const positions: number[] = [];
  let score = 0;
  let from = 0;
  let prevIdx = -2;

  for (let qi = 0; qi < lowerQuery.length; qi++) {
    const idx = lowerText.indexOf(lowerQuery[qi], from);
    if (idx === -1) {
      return null;
    }
    positions.push(idx);

    if (idx === prevIdx + 1) {
      score += 8; // consecutive characters
    }
    const before = idx > 0 ? lowerText[idx - 1] : "/";
    if (idx === 0 || before === "/" || before === "." || before === "_" || before === "-") {
      score += 10; // match at a path/word boundary
    }
    score -= Math.min(idx - from, 4); // penalise gaps, but cap it

    prevIdx = idx;
    from = idx + 1;
  }
  return { score, positions };
}

/**
 * matchWorkspaceFiles ranks file paths against a fuzzy query. An empty query
 * returns the shallowest paths first (a useful default right after "@").
 */
export function matchWorkspaceFiles(files: string[], query: string, limit = 20): FileMatch[] {
  const trimmed = query.trim();

  if (trimmed === "") {
    return [...files]
      .sort((a, b) => depth(a) - depth(b) || a.length - b.length || (a < b ? -1 : 1))
      .slice(0, limit)
      .map((path) => ({ path, score: 0, positions: [] }));
  }

  const lowerQuery = trimmed.toLowerCase();
  const matches: FileMatch[] = [];

  for (const path of files) {
    const result = fuzzyMatch(path, trimmed);
    if (!result) {
      continue;
    }
    let score = result.score;

    const base = basename(path);
    const baseStart = path.length - base.length;
    const baseIdx = base.toLowerCase().indexOf(lowerQuery);
    if (baseIdx !== -1) {
      score += 40; // query is a contiguous substring of the filename
      if (baseIdx === 0) {
        score += 20; // …and a prefix of the filename
      }
    }
    if (result.positions.length > 0 && result.positions[0] >= baseStart) {
      score += 15; // first match lands in the filename, not a parent dir
    }
    score -= path.length * 0.05; // gentle preference for shorter paths

    matches.push({ path, score, positions: result.positions });
  }

  matches.sort((a, b) => b.score - a.score || a.path.length - b.path.length || (a.path < b.path ? -1 : 1));
  return matches.slice(0, limit);
}
