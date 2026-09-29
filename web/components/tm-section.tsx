"use client";

import type { Piece, TMSuggestion } from "@/lib/api";
import { chipLabel } from "@/lib/pieces";
import { cn } from "@/lib/utils";
import { PiecesView, StateDot, stateLabel } from "@/components/pieces";

const wordRe = /([\p{L}\p{N}]+)/u;

/** Lower-cased words of the text pieces (same idea as tm.Words in Go). */
export function wordSet(ps: Piece[]): Set<string> {
  const out = new Set<string>();
  for (const p of ps) {
    if (p.c) continue;
    for (const w of p.t.toLowerCase().split(/[^\p{L}\p{N}]+/u)) if (w) out.add(w);
  }
  return out;
}

/** Renders a match's source with the words the current string lacks highlighted. */
function DiffSource({ pieces, words }: { pieces: Piece[]; words: Set<string> }) {
  return (
    <span className="whitespace-pre-wrap break-words">
      {pieces.map((p, i) =>
        p.c ? (
          <span key={i} className="bl-chip" title={p.t}>
            {chipLabel(p.t)}
          </span>
        ) : (
          <span key={i}>
            {p.t.split(wordRe).map((part, k) =>
              k % 2 === 1 && !words.has(part.toLowerCase()) ? (
                <mark key={k} className="rounded-sm bg-amber-400/25 text-inherit">
                  {part}
                </mark>
              ) : (
                part
              ),
            )}
          </span>
        ),
      )}
    </span>
  );
}

function scoreClass(score: number) {
  if (score === 100) return "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400";
  if (score >= 85) return "bg-amber-500/15 text-amber-700 dark:text-amber-400";
  return "bg-muted text-muted-foreground";
}

type Props = {
  source: Piece[] | undefined;
  matches: TMSuggestion[] | undefined;
  loading: boolean;
  error?: string;
  onApply: (m: TMSuggestion) => void;
};

/** The translation-memory matches (content of the panel section). */
export function TMList({ source, matches, loading, error, onApply }: Props) {
  if (error) return <p className="text-xs text-destructive">{error}</p>;
  if (!matches) return <p className="text-xs text-muted-foreground">{loading ? "Searching…" : ""}</p>;
  if (!matches.length) return <p className="text-xs text-muted-foreground">No similar translated strings yet.</p>;
  const words = source ? wordSet(source) : new Set<string>();
  return (
    <ul className="grid gap-1.5">
      {matches.map((m, i) => (
        <li key={m.unit}>
          <button
            type="button"
            onClick={() => onApply(m)}
            className="grid w-full grid-cols-1 gap-1 rounded-md border px-3 py-2 text-left text-sm hover:bg-muted"
            title={`Use this translation (Alt+${i + 1})`}
          >
            <span className="flex items-center gap-2 text-[11px] text-muted-foreground">
              <span className={cn("rounded px-1.5 py-px font-semibold tabular-nums", scoreClass(m.score))}>{m.score}%</span>
              <StateDot state={m.state} />
              {stateLabel[m.state]}
              <span className="min-w-0 flex-1 truncate font-mono" title={m.path}>
                {m.path}
              </span>
              {i < 5 && <kbd className="rounded border px-1 font-sans text-[10px] opacity-60">Alt+{i + 1}</kbd>}
            </span>
            {m.score < 100 && (
              <span className="text-xs text-muted-foreground">
                <DiffSource pieces={m.src} words={words} />
              </span>
            )}
            <PiecesView pieces={m.tgt} />
          </button>
        </li>
      ))}
    </ul>
  );
}
