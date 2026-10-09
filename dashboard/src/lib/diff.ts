/** A small unified-diff parser for `git diff` output. */

export type LineKind = "context" | "add" | "del";

export interface DiffLine {
  kind: LineKind;
  text: string;
  oldNo: number | null;
  newNo: number | null;
}

export interface Hunk {
  header: string;
  lines: DiffLine[];
}

export interface FileDiff {
  oldPath: string | null; // null: file was created
  newPath: string | null; // null: file was deleted
  hunks: Hunk[];
  additions: number;
  deletions: number;
  binary: boolean;
}

const HUNK = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/;

function stripPrefix(path: string): string | null {
  if (path === "/dev/null") return null;
  return path.replace(/^[ab]\//, "").replace(/\t.*$/, "");
}

function newFile(files: FileDiff[], oldPath: string | null, newPath: string | null): FileDiff {
  const file: FileDiff = { oldPath, newPath, hunks: [], additions: 0, deletions: 0, binary: false };
  files.push(file);
  return file;
}

export function parseDiff(text: string): FileDiff[] {
  const files: FileDiff[] = [];
  let file: FileDiff | null = null;
  let hunk: Hunk | null = null;
  let oldNo = 0;
  let newNo = 0;

  for (const raw of text.split("\n")) {
    if (raw.startsWith("diff --git ")) {
      const match = /^diff --git a\/(.*) b\/(.*)$/.exec(raw);
      file = newFile(files, match?.[1] ?? null, match?.[2] ?? null);
      hunk = null;
      continue;
    }
    // File headers come before the first hunk; inside a hunk "--- x" is a
    // removed line that happens to start with dashes.
    if (raw.startsWith("--- ") && hunk === null) {
      file ??= newFile(files, null, null);
      file.oldPath = stripPrefix(raw.slice(4));
      continue;
    }
    if (raw.startsWith("+++ ") && hunk === null && file !== null) {
      file.newPath = stripPrefix(raw.slice(4));
      continue;
    }
    if (file === null) continue;
    if (raw.startsWith("Binary files ")) {
      file.binary = true;
      continue;
    }
    const header = HUNK.exec(raw);
    if (header) {
      oldNo = Number(header[1]);
      newNo = Number(header[2]);
      hunk = { header: raw, lines: [] };
      file.hunks.push(hunk);
      continue;
    }
    if (hunk === null) continue; // index, mode and similarity lines
    if (raw.startsWith("+")) {
      hunk.lines.push({ kind: "add", text: raw.slice(1), oldNo: null, newNo: newNo++ });
      file.additions++;
    } else if (raw.startsWith("-")) {
      hunk.lines.push({ kind: "del", text: raw.slice(1), oldNo: oldNo++, newNo: null });
      file.deletions++;
    } else if (raw.startsWith(" ")) {
      hunk.lines.push({ kind: "context", text: raw.slice(1), oldNo: oldNo++, newNo: newNo++ });
    }
    // "\ No newline at end of file" and the trailing empty line are skipped.
  }
  return files;
}

export function displayPath(file: FileDiff): string {
  if (file.oldPath && file.newPath && file.oldPath !== file.newPath) {
    return `${file.oldPath} → ${file.newPath}`;
  }
  return file.newPath ?? file.oldPath ?? "(unknown)";
}
