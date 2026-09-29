"use client";

import { memo } from "react";
import type { Counts, Piece, State } from "@/lib/api";
import { chipLabel } from "@/lib/pieces";
import { cn } from "@/lib/utils";

/** A highlighted range of the plain string (UTF-16 offsets, like String.slice). */
export type Mark = { start: number; end: number; title?: string; onClick?: () => void };

/** Splits a text piece at the marks that fall inside it. */
function markText(text: string, base: number, marks: Mark[], key: number) {
  const out: React.ReactNode[] = [];
  let pos = 0;
  for (const m of marks) {
    const s = Math.max(m.start - base, 0);
    const e = Math.min(m.end - base, text.length);
    if (e <= 0 || s >= text.length || e <= s) continue;
    if (s > pos) out.push(text.slice(pos, s));
    out.push(
      <mark
        key={`${key}-${s}`}
        title={m.title}
        onClick={m.onClick}
        className={cn(
          "rounded-sm bg-transparent text-inherit underline decoration-sky-500 decoration-dotted decoration-2 underline-offset-4",
          m.onClick && "cursor-pointer hover:bg-sky-500/10",
        )}
      >
        {text.slice(s, e)}
      </mark>,
    );
    pos = e;
  }
  if (pos < text.length) out.push(text.slice(pos));
  return <span key={key}>{out}</span>;
}

/** Renders text with codes as locked chips. onCode makes chips clickable;
 *  marks underline ranges of the text (glossary terms). */
export const PiecesView = memo(function PiecesView({
  pieces,
  onCode,
  marks,
  className,
}: {
  pieces: Piece[];
  onCode?: (code: string) => void;
  marks?: Mark[];
  className?: string;
}) {
  const sorted = marks?.length ? [...marks].sort((a, b) => a.start - b.start) : undefined;
  // Start offset of each piece in the plain string.
  const starts = sorted ? pieces.reduce<number[]>((acc, p, i) => [...acc, i ? acc[i - 1] + pieces[i - 1].t.length : 0], []) : [];
  return (
    <span className={cn("whitespace-pre-wrap break-words", className)}>
      {pieces.map((p, i) => (!p.c && sorted ? markText(p.t, starts[i], sorted, i) : renderPiece(p, i, onCode)))}
    </span>
  );
});

function renderPiece(p: Piece, i: number, onCode?: (code: string) => void) {
  if (!p.c) return <span key={i}>{p.t}</span>;
  return onCode ? (
    <button key={i} type="button" className="bl-chip" title={p.t} onClick={() => onCode(p.t)}>
      {chipLabel(p.t)}
    </button>
  ) : (
    <span key={i} className="bl-chip" title={p.t}>
      {chipLabel(p.t)}
    </span>
  );
}

export const stateLabel: Record<State, string> = {
  untranslated: "Untranslated",
  mt: "Machine",
  translated: "Translated",
  approved: "Approved",
};

export function StateDot({ state, className }: { state: State; className?: string }) {
  return (
    <span
      className={cn("inline-block size-2 shrink-0 rounded-full", className)}
      style={{ background: `var(--state-${state})` }}
      title={stateLabel[state]}
    />
  );
}

export function StateBadge({ state }: { state: State }) {
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs text-muted-foreground">
      <StateDot state={state} />
      {stateLabel[state]}
    </span>
  );
}

/** Stacked bar: approved | translated | mt | untranslated. */
export function CountsBar({ counts, className }: { counts: Counts; className?: string }) {
  const total = counts.total || 1;
  const untranslated = counts.total - counts.mt - counts.translated - counts.approved;
  const seg = (n: number, state: State) =>
    n > 0 ? <span style={{ width: `${(n / total) * 100}%`, background: `var(--state-${state})` }} /> : null;
  return (
    <div className={cn("flex h-1.5 w-full overflow-hidden rounded-full bg-muted", className)}>
      {seg(counts.approved, "approved")}
      {seg(counts.translated, "translated")}
      {seg(counts.mt, "mt")}
      {seg(untranslated, "untranslated")}
    </div>
  );
}

/** "done" = human-translated or approved. */
export const donePct = (c: Counts) => (c.total ? Math.floor(((c.translated + c.approved) / c.total) * 100) : 0);

export function Legend() {
  return (
    <div className="flex flex-wrap gap-3 text-xs text-muted-foreground">
      {(["approved", "translated", "mt", "untranslated"] as State[]).map((s) => (
        <span key={s} className="inline-flex items-center gap-1.5">
          <StateDot state={s} /> {stateLabel[s]}
        </span>
      ))}
    </div>
  );
}
