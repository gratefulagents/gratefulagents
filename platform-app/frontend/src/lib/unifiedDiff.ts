/**
 * Pure unified-diff parser for the run inspector. It turns `git diff` output
 * into files → hunks → rows with resolved old/new line numbers so the viewer
 * can virtualise and decorate rows without re-scanning the text. Tolerant of
 * CRLF, missing `diff --git` headers, rename/mode-only entries and binaries.
 */

export type DiffRowKind = "add" | "delete" | "context" | "meta";

export type DiffRow = {
  kind: DiffRowKind;
  oldNo?: number;
  newNo?: number;
  /** Row text without the leading `+`/`-`/space marker. */
  text: string;
};

export type DiffHunk = {
  header: string;
  oldStart: number;
  oldLines: number;
  newStart: number;
  newLines: number;
  lines: DiffRow[];
};

export type DiffFileStatus = "modified" | "added" | "deleted" | "renamed" | "binary";

export type DiffFile = {
  id: string;
  oldPath: string;
  newPath: string;
  displayPath: string;
  status: DiffFileStatus;
  additions: number;
  deletions: number;
  hunks: DiffHunk[];
  /** Raw header lines (`diff --git`, `index`, `---`, `+++`, mode/rename lines). */
  headerLines: string[];
};

export type DiffSummary = {
  files: number;
  additions: number;
  deletions: number;
};

export type TextRange = { start: number; end: number };

/** Changed character ranges keyed by row index within `hunk.lines`. */
export type RowHighlights = Map<number, TextRange[]>;

const hunkHeaderPattern = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;
const diffGitPattern = /^diff --git (?:"?a\/)?(.+?)"? (?:"?b\/)?(.+?)"?$/;
const binaryPattern = /^Binary files? |^GIT binary patch/;

type MutableFile = DiffFile & { seenNewFileMode: boolean; seenDeletedFileMode: boolean };

function stripPathPrefix(raw: string): string {
  let path = raw.trim();
  // `--- a/path<TAB>timestamp` is legal in non-git patches.
  const tab = path.indexOf("\t");
  if (tab !== -1) path = path.slice(0, tab);
  if (path.startsWith('"') && path.endsWith('"')) path = path.slice(1, -1);
  if (path === "/dev/null") return "";
  if (/^[ab]\//.test(path)) return path.slice(2);
  return path;
}

function newFile(index: number, headerLine?: string): MutableFile {
  let oldPath = "";
  let newPath = "";
  if (headerLine) {
    const match = diffGitPattern.exec(headerLine);
    if (match) {
      oldPath = match[1];
      newPath = match[2];
    }
  }
  return {
    id: `${index}`,
    oldPath,
    newPath,
    displayPath: "",
    status: "modified",
    additions: 0,
    deletions: 0,
    hunks: [],
    headerLines: headerLine ? [headerLine] : [],
    seenNewFileMode: false,
    seenDeletedFileMode: false,
  };
}

function finalizeFile(file: MutableFile, index: number): DiffFile {
  const { seenNewFileMode, seenDeletedFileMode, ...rest } = file;
  let status = rest.status;
  if (status !== "binary") {
    const renamed = rest.oldPath && rest.newPath && rest.oldPath !== rest.newPath;
    if (seenNewFileMode || (!rest.oldPath && rest.newPath)) status = "added";
    else if (seenDeletedFileMode || (rest.oldPath && !rest.newPath)) status = "deleted";
    else if (renamed) status = "renamed";
    else status = "modified";
  }
  const displayPath =
    status === "renamed" && rest.oldPath !== rest.newPath
      ? `${rest.oldPath} → ${rest.newPath}`
      : rest.newPath || rest.oldPath || "(unknown file)";
  return { ...rest, status, displayPath, id: `${index}:${displayPath}` };
}

export function parseUnifiedDiff(diff: string): DiffFile[] {
  if (!diff) return [];
  const lines = diff.replace(/\r\n?/g, "\n").split("\n");
  if (lines[lines.length - 1] === "") lines.pop();

  const files: DiffFile[] = [];
  let current = null as MutableFile | null;
  let hunk = null as DiffHunk | null;
  let oldRemaining = 0;
  let newRemaining = 0;
  let oldCursor = 0;
  let newCursor = 0;

  const closeFile = (): void => {
    if (current) files.push(finalizeFile(current, files.length));
    current = null;
    hunk = null;
  };

  const startFile = (headerLine?: string): MutableFile => {
    closeFile();
    current = newFile(files.length, headerLine);
    return current;
  };

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];

    if (line.startsWith("diff --git ")) {
      startFile(line);
      continue;
    }

    const inHunk = hunk !== null && (oldRemaining > 0 || newRemaining > 0);

    if (inHunk && hunk) {
      const marker = line[0];
      if (marker === "\\") {
        hunk.lines.push({ kind: "meta", text: line.slice(1).trim() });
        continue;
      }
      if (marker === "+") {
        hunk.lines.push({ kind: "add", newNo: newCursor++, text: line.slice(1) });
        newRemaining--;
        if (current) current.additions++;
        continue;
      }
      if (marker === "-") {
        hunk.lines.push({ kind: "delete", oldNo: oldCursor++, text: line.slice(1) });
        oldRemaining--;
        if (current) current.deletions++;
        continue;
      }
      if (marker === " " || line === "") {
        hunk.lines.push({ kind: "context", oldNo: oldCursor++, newNo: newCursor++, text: line.slice(1) });
        oldRemaining--;
        newRemaining--;
        continue;
      }
      // Anything else means the hunk counts were wrong; fall through to header parsing.
      hunk = null;
    }

    if (hunk && line.startsWith("\\")) {
      hunk.lines.push({ kind: "meta", text: line.slice(1).trim() });
      continue;
    }

    const hunkMatch = hunkHeaderPattern.exec(line);
    if (hunkMatch) {
      const file: MutableFile = current ?? startFile();
      const oldStart = Number(hunkMatch[1]);
      const oldLines = hunkMatch[2] === undefined ? 1 : Number(hunkMatch[2]);
      const newStart = Number(hunkMatch[3]);
      const newLines = hunkMatch[4] === undefined ? 1 : Number(hunkMatch[4]);
      hunk = { header: line, oldStart, oldLines, newStart, newLines, lines: [] };
      file.hunks.push(hunk);
      oldRemaining = oldLines;
      newRemaining = newLines;
      oldCursor = oldStart;
      newCursor = newStart;
      continue;
    }

    if (line.startsWith("--- ")) {
      // A `---` header after hunks (or with no file at all) starts a new file
      // when the diff omits `diff --git` lines.
      const needsFile = !current || current.hunks.length > 0 || current.headerLines.some((h) => h.startsWith("--- "));
      const file: MutableFile = needsFile || !current ? startFile() : current;
      file.headerLines.push(line);
      file.oldPath = stripPathPrefix(line.slice(4));
      hunk = null;
      continue;
    }

    if (line.startsWith("+++ ")) {
      const file: MutableFile = current ?? startFile();
      file.headerLines.push(line);
      const path = stripPathPrefix(line.slice(4));
      file.newPath = path;
      hunk = null;
      continue;
    }

    if (!current) {
      // Preamble (commit message, `Only in …`, blank lines) is ignored.
      continue;
    }

    const file: MutableFile = current;
    file.headerLines.push(line);
    hunk = null;

    if (line.startsWith("new file mode ")) file.seenNewFileMode = true;
    else if (line.startsWith("deleted file mode ")) file.seenDeletedFileMode = true;
    else if (line.startsWith("rename from ")) file.oldPath = line.slice("rename from ".length);
    else if (line.startsWith("rename to ")) file.newPath = line.slice("rename to ".length);
    else if (line.startsWith("copy from ")) file.oldPath = line.slice("copy from ".length);
    else if (line.startsWith("copy to ")) file.newPath = line.slice("copy to ".length);
    else if (binaryPattern.test(line)) file.status = "binary";
  }

  closeFile();
  return files;
}

export function summarizeDiff(files: readonly DiffFile[]): DiffSummary {
  let additions = 0;
  let deletions = 0;
  for (const file of files) {
    additions += file.additions;
    deletions += file.deletions;
  }
  return { files: files.length, additions, deletions };
}

const WORD_DIFF_MAX_CHARS = 400;
const tokenPattern = /\w+|\s+|[^\w\s]/g;

type Token = { text: string; start: number; end: number };

function tokenize(text: string): Token[] {
  const tokens: Token[] = [];
  for (const match of text.matchAll(tokenPattern)) {
    tokens.push({ text: match[0], start: match.index, end: match.index + match[0].length });
  }
  return tokens;
}

function mergeRanges(ranges: TextRange[]): TextRange[] {
  const merged: TextRange[] = [];
  for (const range of ranges) {
    const last = merged[merged.length - 1];
    if (last && last.end === range.start) last.end = range.end;
    else merged.push({ ...range });
  }
  return merged;
}

/**
 * Word-level LCS between a deleted and an added line. Returns the character
 * ranges on each side that are *not* part of the common subsequence. Empty on
 * both sides when the lines are too long or share no meaningful token, in
 * which case the caller should fall back to whole-row tinting.
 */
export function diffWords(oldText: string, newText: string): { old: TextRange[]; new: TextRange[] } {
  const none = { old: [], new: [] };
  if (oldText.length > WORD_DIFF_MAX_CHARS || newText.length > WORD_DIFF_MAX_CHARS) return none;
  if (oldText === newText) return none;

  const a = tokenize(oldText);
  const b = tokenize(newText);
  const n = a.length;
  const m = b.length;
  if (n === 0 || m === 0) return none;

  // Standard O(n·m) LCS table; inputs are bounded to 400 chars per side.
  const table: Uint16Array[] = [];
  for (let i = 0; i <= n; i++) table.push(new Uint16Array(m + 1));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      table[i][j] =
        a[i].text === b[j].text
          ? table[i + 1][j + 1] + 1
          : Math.max(table[i + 1][j], table[i][j + 1]);
    }
  }

  const oldChanged: TextRange[] = [];
  const newChanged: TextRange[] = [];
  let commonNonSpace = 0;
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i].text === b[j].text) {
      if (a[i].text.trim()) commonNonSpace++;
      i++;
      j++;
    } else if (table[i + 1][j] >= table[i][j + 1]) {
      oldChanged.push({ start: a[i].start, end: a[i].end });
      i++;
    } else {
      newChanged.push({ start: b[j].start, end: b[j].end });
      j++;
    }
  }
  for (; i < n; i++) oldChanged.push({ start: a[i].start, end: a[i].end });
  for (; j < m; j++) newChanged.push({ start: b[j].start, end: b[j].end });

  if (commonNonSpace === 0) return none;
  return { old: mergeRanges(oldChanged), new: mergeRanges(newChanged) };
}

/**
 * Pairs each run of deletes with the following run of adds inside a hunk
 * (the i-th delete with the i-th add) and word-diffs each pair.
 */
export function pairChangedRows(hunk: DiffHunk): RowHighlights {
  const highlights: RowHighlights = new Map();
  const rows = hunk.lines;
  let i = 0;
  while (i < rows.length) {
    if (rows[i].kind !== "delete") {
      i++;
      continue;
    }
    const deleteStart = i;
    while (i < rows.length && rows[i].kind === "delete") i++;
    const addStart = i;
    while (i < rows.length && rows[i].kind === "add") i++;
    const pairs = Math.min(i - addStart, addStart - deleteStart);
    for (let k = 0; k < pairs; k++) {
      const deleteIndex = deleteStart + k;
      const addIndex = addStart + k;
      const result = diffWords(rows[deleteIndex].text, rows[addIndex].text);
      if (result.old.length > 0) highlights.set(deleteIndex, result.old);
      if (result.new.length > 0) highlights.set(addIndex, result.new);
    }
  }
  return highlights;
}
