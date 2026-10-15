"use client";

import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useMemo, useState } from "react";
import { ArrowLeft, ArrowRight, ChevronDown, ChevronRight, History as HistoryIcon } from "lucide-react";
import { api, type HistoryBatch, type HistoryDay, type HistoryOrigin, type HistoryRow } from "@/lib/api";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { StateDot, stateLabel } from "@/components/pieces";
import { ThemeToggle } from "@/components/theme-toggle";

/** Who made a change. An AI agent (MCP) and machine translation are different actors. */
const ORIGINS: { id: HistoryOrigin; label: string; tone: string }[] = [
  { id: "ai", label: "AI agent", tone: "border-violet-500/40 bg-violet-500/10 text-violet-700 dark:text-violet-300" },
  { id: "mt", label: "Machine translation", tone: "border-sky-500/40 bg-sky-500/10 text-sky-700 dark:text-sky-300" },
  { id: "tm", label: "Translation memory", tone: "border-teal-500/40 bg-teal-500/10 text-teal-700 dark:text-teal-300" },
  { id: "bulk", label: "Bulk change", tone: "border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-300" },
  { id: "edit", label: "Manual edit", tone: "border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300" },
  { id: "import", label: "Import", tone: "border-slate-500/40 bg-slate-500/10 text-slate-700 dark:text-slate-300" },
];
const ORIGIN = Object.fromEntries(ORIGINS.map((o) => [o.id, o])) as Record<HistoryOrigin, (typeof ORIGINS)[number]>;

function OriginChip({ origin }: { origin: HistoryOrigin }) {
  const o = ORIGIN[origin];
  return <span className={cn("inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium", o?.tone)}>{o?.label ?? origin}</span>;
}

const time = (t: number) => new Date(t * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
const dayTitle = (start: number) =>
  new Date(start * 1000).toLocaleDateString([], { weekday: "long", year: "numeric", month: "long", day: "numeric" });

export function History() {
  const sp = useSearchParams();
  const pid = Number(sp.get("project"));
  const project = useQuery({ queryKey: ["project", pid], queryFn: () => api.project(pid), enabled: pid > 0 });
  const [hidden, setHidden] = useState<Set<HistoryOrigin>>(new Set());
  // The viewer's offset east of UTC, in seconds: days are local calendar days.
  const tz = useMemo(() => -new Date().getTimezoneOffset() * 60, []);

  const days = useInfiniteQuery({
    queryKey: ["history-days", pid, tz],
    queryFn: ({ pageParam }) => api.historyDays(pid, tz, pageParam),
    initialPageParam: 0,
    getNextPageParam: (last) => (last.next > 0 ? last.next : undefined),
    enabled: pid > 0,
    staleTime: 0,
  });

  if (!pid) {
    return (
      <p className="p-8">
        No project selected. <Link href="/" className="underline">Back to projects</Link>
      </p>
    );
  }

  const all: HistoryDay[] = days.data?.pages.flatMap((p) => p.days) ?? [];
  const shown = all
    .map((d) => {
      const batches = d.batches.filter((b) => !hidden.has(b.origin));
      return { ...d, batches, count: batches.reduce((n, b) => n + b.count, 0) };
    })
    .filter((d) => d.batches.length > 0);
  const present = new Set(all.flatMap((d) => d.batches.map((b) => b.origin)));

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-4 px-6 py-6">
      <header className="flex items-center gap-2">
        <Button asChild variant="ghost" size="icon-sm">
          <Link href="/" aria-label="Projects">
            <ArrowLeft />
          </Link>
        </Button>
        <HistoryIcon className="size-5" />
        <h1 className="text-lg font-semibold">{project.data?.name} history</h1>
        <div className="flex-1" />
        <Button asChild variant="ghost" size="sm">
          <Link href={`/editor?project=${pid}`}>Editor</Link>
        </Button>
        <ThemeToggle />
      </header>

      <div className="flex flex-wrap items-center gap-2">
        {ORIGINS.filter((o) => present.has(o.id)).map((o) => {
          const off = hidden.has(o.id);
          return (
            <button
              key={o.id}
              type="button"
              aria-pressed={!off}
              onClick={() =>
                setHidden((h) => {
                  const n = new Set(h);
                  if (off) n.delete(o.id);
                  else n.add(o.id);
                  return n;
                })
              }
              className={cn("rounded-full border px-2.5 py-1 text-xs font-medium transition-opacity", off ? "opacity-40" : o.tone)}
            >
              {o.label}
            </button>
          );
        })}
      </div>

      {days.isPending ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : shown.length === 0 && !days.hasNextPage ? (
        <p className="text-sm text-muted-foreground">No changes recorded yet.</p>
      ) : (
        shown.map((d) => <Day key={d.start} pid={pid} day={d} />)
      )}

      {days.hasNextPage && (
        <Button variant="outline" size="sm" className="self-center" onClick={() => days.fetchNextPage()} disabled={days.isFetchingNextPage}>
          {days.isFetchingNextPage ? "Loading…" : "Older days"}
        </Button>
      )}
    </div>
  );
}

function Day({ pid, day }: { pid: number; day: HistoryDay }) {
  return (
    <section className="overflow-hidden rounded-lg border">
      <h2 className="sticky top-0 z-10 flex items-baseline gap-2 border-b bg-muted px-3 py-2 text-sm font-semibold">
        {dayTitle(day.start)}
        <span className="text-xs font-normal text-muted-foreground">{day.count.toLocaleString()} changes</span>
      </h2>
      <div className="divide-y">
        {day.batches.map((b) => (
          <Batch key={`${b.key}|${b.origin}`} pid={pid} day={day} batch={b} />
        ))}
      </div>
    </section>
  );
}

function Batch({ pid, day, batch }: { pid: number; day: HistoryDay; batch: HistoryBatch }) {
  const [open, setOpen] = useState(false);
  return (
    <div>
      <button type="button" onClick={() => setOpen(!open)} className="flex w-full items-start gap-2 px-3 py-2 text-left hover:bg-muted/40">
        {open ? <ChevronDown className="mt-0.5 size-4 shrink-0" /> : <ChevronRight className="mt-0.5 size-4 shrink-0" />}
        <span className="w-12 shrink-0 pt-0.5 text-xs tabular-nums text-muted-foreground">{time(batch.last)}</span>
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1">
          <OriginChip origin={batch.origin} />
          <span className="text-sm font-medium tabular-nums">
            {batch.count.toLocaleString()} {batch.count === 1 ? "string" : "strings"}
          </span>
          {batch.transitions.map((t) => (
            <span key={`${t.old}>${t.new}`} className="inline-flex items-center gap-1 text-xs text-muted-foreground">
              {t.old === t.new ? (
                <>
                  <StateDot state={t.new} />
                  {stateLabel[t.new]}, text changed
                </>
              ) : (
                <>
                  <StateDot state={t.old} />
                  {stateLabel[t.old]}
                  <ArrowRight className="size-3" />
                  <StateDot state={t.new} />
                  {stateLabel[t.new]}
                </>
              )}
              <span className="tabular-nums">· {t.count.toLocaleString()}</span>
            </span>
          ))}
        </div>
      </button>
      {open && <Entries pid={pid} day={day} batch={batch} />}
    </div>
  );
}

function Entries({ pid, day, batch }: { pid: number; day: HistoryDay; batch: HistoryBatch }) {
  const q = useInfiniteQuery({
    queryKey: ["history", pid, day.start, batch.key, batch.origin],
    queryFn: ({ pageParam }) =>
      api.history(pid, { from: day.start, to: day.start + 86400 + 3600, origin: batch.origin, batch: batch.key }, pageParam),
    initialPageParam: null as { a: number; b: number } | null,
    getNextPageParam: (last) => last.next ?? undefined,
    staleTime: 0,
  });
  const rows: HistoryRow[] = q.data?.pages.flatMap((p) => p.rows) ?? [];
  return (
    <div className="border-t bg-muted/20">
      {q.isPending && <p className="px-3 py-2 text-xs text-muted-foreground">Loading…</p>}
      {rows.map((r) => (
        <Entry key={r.id} pid={pid} row={r} />
      ))}
      {q.hasNextPage && (
        <div className="px-3 py-2">
          <Button variant="ghost" size="sm" onClick={() => q.fetchNextPage()} disabled={q.isFetchingNextPage}>
            {q.isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        </div>
      )}
    </div>
  );
}

function Entry({ pid, row }: { pid: number; row: HistoryRow }) {
  return (
    <Link
      href={`/editor?project=${pid}&file=${row.file}&unit=${row.unit}`}
      className="grid grid-cols-[minmax(0,14rem)_1fr] gap-x-4 gap-y-0.5 border-b px-3 py-1.5 text-sm last:border-b-0 hover:bg-muted/50"
    >
      <div className="min-w-0">
        <div className="truncate font-mono text-xs" title={row.key}>{row.key}</div>
        <div className="truncate text-xs text-muted-foreground" title={row.path}>{row.path}</div>
        <div className="mt-0.5 flex items-center gap-1 text-xs text-muted-foreground">
          <StateDot state={row.oldState} />
          <ArrowRight className="size-3" />
          <StateDot state={row.state} />
          <span>{stateLabel[row.state]}</span>
        </div>
      </div>
      <div className="min-w-0 space-y-0.5">
        {row.old !== row.new && row.old !== null && (
          <div className="line-clamp-2 break-words text-muted-foreground line-through decoration-muted-foreground/40">{row.old}</div>
        )}
        <div className="line-clamp-2 break-words">{row.new ?? <span className="italic text-muted-foreground">cleared</span>}</div>
      </div>
    </Link>
  );
}
