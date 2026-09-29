import type { Piece } from "./api";

export const plain = (ps: Piece[] | null | undefined) => (ps ? ps.map((p) => p.t).join("") : "");

/** Source codes in order (with repeats). */
export const codesOf = (ps: Piece[]) => ps.filter((p) => p.c).map((p) => p.t);

export type Mismatch = { missing: string[]; extra: string[] };

/**
 * Port of model.SplitTarget (Go): locate the source's codes in a plain target
 * string, longest code first at every position, each used as often as it
 * occurs in the source. Returns the pieces and the multiset difference.
 */
export function splitTarget(target: string, source: Piece[]): { pieces: Piece[]; mismatch: Mismatch | null } {
  const remaining = new Map<string, number>();
  for (const p of source) if (p.c && p.t) remaining.set(p.t, (remaining.get(p.t) ?? 0) + 1);
  if (remaining.size === 0) return { pieces: target ? [{ t: target }] : [], mismatch: null };
  const codes = [...remaining.keys()].sort((a, b) => b.length - a.length);

  const pieces: Piece[] = [];
  const extra = new Map<string, number>();
  let text = "";
  let i = 0;
  while (i < target.length) {
    let matched = "";
    for (const c of codes) {
      if (target.startsWith(c, i)) {
        matched = c;
        break;
      }
    }
    if (!matched) {
      text += target[i++];
      continue;
    }
    const left = remaining.get(matched)!;
    if (left === 0) {
      extra.set(matched, (extra.get(matched) ?? 0) + 1);
      text += matched;
    } else {
      remaining.set(matched, left - 1);
      if (text) pieces.push({ t: text });
      text = "";
      pieces.push({ t: matched, c: true });
    }
    i += matched.length;
  }
  if (text) pieces.push({ t: text });

  const missing: string[] = [];
  const extras: string[] = [];
  for (const c of codes) {
    for (let n = remaining.get(c)!; n > 0; n--) missing.push(c);
    for (let n = extra.get(c) ?? 0; n > 0; n--) extras.push(c);
  }
  return { pieces, mismatch: missing.length || extras.length ? { missing, extra: extras } : null };
}

/**
 * Short human label for a code chip. A code can hold several adjacent pieces
 * of markup ("</><Rich color=…>", "\n\n"): long tags shrink to their name,
 * line breaks become ↵.
 */
export function chipLabel(code: string): string {
  let s = code
    .replace(/<(\/?)\s*([A-Za-z][\w-]*)?[^>]*>/g, (m, slash: string, name: string | undefined) =>
      m.length > 14 ? `<${slash}${name ?? ""}…>` : m,
    )
    .replace(/\r?\n/g, "↵")
    .replace(/\t/g, "⇥");
  if (s.length > 28) s = s.slice(0, 26) + "…";
  return s;
}
