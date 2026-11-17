/** One run of a word-level diff, in the order of the new text. */
export type DiffSeg =
  | { kind: "same" | "added" | "removed"; text: string; old?: undefined }
  /** `text` is the new wording, `old` what it replaced. */
  | { kind: "replaced"; text: string; old: string };

// Words (apostrophes and hyphens inside them included), whitespace runs, and
// every other character on its own. Built from a string so the `u` flag and
// \p{…} don't depend on the TS target.
const TOKEN = new RegExp("[\\p{L}\\p{N}]+(?:['’ʼ-][\\p{L}\\p{N}]+)*|\\s+|[^\\p{L}\\p{N}\\s]", "gu");
const SPACE = /^\s+$/;

/** Above this many table cells the middle of a diff is reported as one change. */
const MAX_CELLS = 1_000_000;

type Op = { old: string; new: string } | string; // a change, or unchanged text

/**
 * diffWords compares two texts word by word. Joining the `text` of every
 * segment but the removed ones gives `b`; the same/removed texts and the
 * replaced `old`s give `a`.
 */
export function diffWords(a: string, b: string): DiffSeg[] {
  const x = a.match(TOKEN) ?? [];
  const y = b.match(TOKEN) ?? [];
  let pre = 0;
  while (pre < x.length && pre < y.length && x[pre] === y[pre]) pre++;
  let suf = 0;
  while (suf < x.length - pre && suf < y.length - pre && x[x.length - 1 - suf] === y[y.length - 1 - suf]) suf++;

  const ops: Op[] = [];
  if (pre > 0) ops.push(x.slice(0, pre).join(""));
  middle(x.slice(pre, x.length - suf), y.slice(pre, y.length - suf), ops);
  if (suf > 0) ops.push(x.slice(x.length - suf).join(""));
  return segments(ops);
}

/** middle appends the ops for two token lists with no common prefix or suffix. */
function middle(x: string[], y: string[], ops: Op[]) {
  const n = x.length;
  const m = y.length;
  if (n === 0 && m === 0) return;
  if (n === 0 || m === 0 || n * m > MAX_CELLS) {
    ops.push({ old: x.join(""), new: y.join("") });
    return;
  }
  // lcs[i*w+j] = length of the longest common subsequence of x[i:] and y[j:].
  const w = m + 1;
  const lcs = new Uint32Array((n + 1) * w);
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i * w + j] = x[i] === y[j] ? lcs[(i + 1) * w + j + 1] + 1 : Math.max(lcs[(i + 1) * w + j], lcs[i * w + j + 1]);
    }
  }
  let i = 0;
  let j = 0;
  let change: { old: string; new: string } | null = null;
  const flush = () => {
    if (change) ops.push(change);
    change = null;
  };
  while (i < n || j < m) {
    if (i < n && j < m && x[i] === y[j]) {
      flush();
      ops.push(x[i]);
      i++;
      j++;
    } else {
      change ??= { old: "", new: "" };
      if (j < m && (i === n || lcs[i * w + j + 1] >= lcs[(i + 1) * w + j])) change.new += y[j++];
      else change.old += x[i++];
    }
  }
  flush();
}

/** segments merges the ops into runs; whitespace between two changes joins them. */
function segments(ops: Op[]): DiffSeg[] {
  const merged: Op[] = [];
  for (let k = 0; k < ops.length; k++) {
    const op = ops[k];
    const last = merged[merged.length - 1];
    if (typeof op === "string") {
      if (typeof last === "string") merged[merged.length - 1] = last + op;
      else merged.push(op);
      continue;
    }
    const gap = merged.length >= 2 && typeof last === "string" && SPACE.test(last) ? merged[merged.length - 2] : undefined;
    if (gap !== undefined && typeof gap !== "string" && typeof last === "string") {
      // change, whitespace, change: one change that spans the whitespace.
      merged.length -= 2;
      merged.push({ old: gap.old + last + op.old, new: gap.new + last + op.new });
    } else if (last !== undefined && typeof last !== "string") {
      merged[merged.length - 1] = { old: last.old + op.old, new: last.new + op.new };
    } else {
      merged.push({ ...op });
    }
  }
  return merged.map((op): DiffSeg => {
    if (typeof op === "string") return { kind: "same", text: op };
    if (op.old === "") return { kind: "added", text: op.new };
    if (op.new === "") return { kind: "removed", text: op.old };
    return { kind: "replaced", text: op.new, old: op.old };
  });
}
