"use client";

import { useInfiniteQuery, useMutation, useQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { ArrowLeft, BookMarked, ChevronDown, Search, X } from "lucide-react";
import { api, ApiError, SORTS, type Filter, type Piece, type Row, type Sort, type State, type Unit, type UnitsPage } from "@/lib/api";
import { splitTarget } from "@/lib/pieces";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { type Scope } from "@/components/file-tree";
import { FilesPane } from "@/components/files-pane";
import { UnitList } from "@/components/unit-list";
import { UnitPanel, type PanelActions } from "@/components/unit-panel";
import { RunningJobs } from "@/components/jobs";
import { FilterMenu, SortMenu } from "@/components/list-controls";
import { ThemeToggle } from "@/components/theme-toggle";
import { useConfirm } from "@/components/confirm";

const PAGE = 200;

/** URL parameters restored from the last session (the file/folder scope is not). */
const REMEMBERED = ["q", "in", "state", "qa", "changed", "sort"] as const;
const filtersKey = (pid: number) => `bellingua:filters:${pid}`;

function loadFilters(pid: number): Record<string, string> {
  try {
    const v = JSON.parse(localStorage.getItem(filtersKey(pid)) ?? "{}");
    return v && typeof v === "object" ? v : {};
  } catch {
    return {};
  }
}

function saveFilters(pid: number, f: Record<string, string>) {
  try {
    localStorage.setItem(filtersKey(pid), JSON.stringify(f));
  } catch {}
}
const ERR_MASK = 0b11; // qa.Errors: placeholders | empty

/** URL-backed state: survives reloads and can be bookmarked. Uses the native
 *  history API, which Next keeps in sync with useSearchParams without a
 *  navigation round trip. */
function useUrl() {
  const sp = useSearchParams();
  const set = useCallback((patch: Record<string, string | undefined>) => {
    const p = new URLSearchParams(window.location.search);
    for (const [k, v] of Object.entries(patch)) {
      if (v) p.set(k, v);
      else p.delete(k);
    }
    window.history.replaceState(null, "", `?${p}`);
  }, []);
  return [sp, set] as const;
}

export function Editor() {
  const [sp, setUrl] = useUrl();
  const qc = useQueryClient();
  const pid = Number(sp.get("project"));
  const langs = useQuery({ queryKey: ["langs", pid], queryFn: () => api.langs(pid), enabled: pid > 0 });
  const info = useQuery({
    queryKey: ["info"],
    queryFn: api.info,
    staleTime: Infinity,
    // Follow the MT server while it loads, then stop polling.
    refetchInterval: (q) => (q.state.data?.mtServer?.state === "starting" ? 3000 : false),
  });
  const project = useQuery({ queryKey: ["project", pid], queryFn: () => api.project(pid), enabled: pid > 0 });

  const filter: Filter = useMemo(
    () => ({
      file: Number(sp.get("file")) || undefined,
      dir: sp.get("dir") ?? undefined,
      q: sp.get("q") ?? undefined,
      in: (sp.get("in") as Filter["in"]) ?? undefined,
      states: (sp.get("state")?.split(",").filter(Boolean) as State[]) ?? undefined,
      qa: sp.get("qa") ?? undefined,
      changed: sp.get("changed") === "1",
      same: Number(sp.get("same")) || undefined,
    }),
    [sp],
  );
  const selectedId = Number(sp.get("unit")) || null;
  const sort: Sort = SORTS.some((x) => x.id === sp.get("sort")) ? (sp.get("sort") as Sort) : "file";

  // --- data ------------------------------------------------------------------
  const listKey = useMemo(() => ["units", pid, filter, sort] as const, [pid, filter, sort]);
  const units = useInfiniteQuery({
    queryKey: listKey,
    queryFn: ({ pageParam, signal }) => api.units(pid, filter, sort, pageParam, PAGE, signal),
    initialPageParam: null as UnitsPage["next"],
    getNextPageParam: (last) => last.next,
    enabled: pid > 0,
    staleTime: Infinity, // edits are patched into the cache; refetch only on filter change
  });
  const rows = useMemo(() => units.data?.pages.flatMap((p) => p.rows) ?? [], [units.data]);
  const count = useQuery({
    queryKey: ["count", pid, filter],
    queryFn: ({ signal }) => api.count(pid, filter, signal),
    enabled: pid > 0,
    placeholderData: (prev) => prev,
  });

  const indexOf = useCallback((id: number | null) => (id == null ? -1 : rows.findIndex((r) => r.id === id)), [rows]);
  const selectedIndex = indexOf(selectedId);
  const select = useCallback((id: number | null) => setUrl({ unit: id ? String(id) : undefined }), [setUrl]);

  // Select the first row when nothing is selected. A selected unit that is
  // not in the loaded pages (deep link) stays selected: the panel loads it by id.
  useEffect(() => {
    if (rows.length && selectedId == null) select(rows[0].id);
  }, [rows, selectedId, select]);

  const move = useCallback(
    (delta: number) => {
      const i = indexOf(selectedId);
      const next = rows[Math.max(0, Math.min(rows.length - 1, i + delta))];
      if (next) select(next.id);
      if (delta > 0 && i + delta >= rows.length - 5 && units.hasNextPage) units.fetchNextPage();
    },
    [indexOf, selectedId, rows, select, units],
  );

  // Prefetch the next units' details (and one MT suggestion) so moving on
  // never waits for the network.
  const mtEnabled = (info.data?.mtProviders?.length ?? 0) > 0;
  useEffect(() => {
    if (selectedIndex < 0) return;
    const ahead = rows.slice(selectedIndex + 1, selectedIndex + 4);
    for (const r of ahead) {
      qc.prefetchQuery({ queryKey: ["unit", r.id], queryFn: () => api.unit(r.id), staleTime: 60_000 });
      qc.prefetchQuery({ queryKey: ["tm", r.id], queryFn: () => api.unitTM(r.id), staleTime: 30_000 });
    }
    if (mtEnabled) {
      const nextUntranslated = rows.slice(selectedIndex + 1, selectedIndex + 6).find((r) => r.state === "untranslated");
      if (nextUntranslated)
        qc.prefetchQuery({ queryKey: ["mt", nextUntranslated.id], queryFn: () => api.unitMT(nextUntranslated.id), staleTime: Infinity });
    }
  }, [selectedIndex, rows, qc, mtEnabled]);

  // --- drafts (unsaved edits survive moving between units) --------------------
  const [drafts, setDrafts] = useState<Map<number, string>>(() => new Map());
  const onDraft = useCallback((id: number, text: string | undefined) => {
    setDrafts((d) => {
      if (text === undefined ? !d.has(id) : d.get(id) === text) return d;
      const n = new Map(d);
      if (text === undefined) n.delete(id);
      else n.set(id, text);
      return n;
    });
  }, []);
  const draftIds = useMemo(() => new Set(drafts.keys()), [drafts]);

  // --- saving (optimistic) ----------------------------------------------------
  const patchRow = useCallback(
    (id: number, patch: Partial<Row>) => {
      qc.setQueryData<InfiniteData<UnitsPage>>(listKey, (d) =>
        d && {
          ...d,
          pages: d.pages.map((p) => ({ ...p, rows: p.rows.map((r) => (r.id === id ? { ...r, ...patch } : r)) })),
        },
      );
    },
    [qc, listKey],
  );

  const propagate = useCallback(
    (id: number) =>
      api.propagate(id).then(
        (r) => {
          toast.success(`Applied to ${r.changed.toLocaleString()} identical ${r.changed === 1 ? "string" : "strings"}`);
          qc.invalidateQueries({ queryKey: ["units", pid] });
          qc.invalidateQueries({ queryKey: ["count", pid] });
          qc.invalidateQueries({ queryKey: ["tree", pid] });
          qc.invalidateQueries({ queryKey: ["tm"] });
          qc.invalidateQueries({ queryKey: ["unit"] });
        },
        (e) => toast.error(`Not applied: ${e.message}`),
      ),
    [qc, pid],
  );

  const onSaved = useCallback(
    (u: Unit) => {
      if (u.duplicates) {
        const n = u.duplicates;
        toast(`${n.toLocaleString()} identical ${n === 1 ? "string has" : "strings have"} no human translation yet`, {
          description: "Same source text elsewhere in the project.",
          action: { label: `Apply to ${n}`, onClick: () => propagate(u.id) },
          duration: 20_000,
        });
      }
      qc.invalidateQueries({ queryKey: ["tm"] });
      qc.setQueryData(["unit", u.id], u);
      patchRow(u.id, { qa: u.qa, state: u.state, changed: u.changed });
      // Counters: cheap to refetch (ETag + denormalised counts).
      qc.invalidateQueries({ queryKey: ["count", pid] });
      qc.invalidateQueries({ queryKey: ["tree", pid] });
      qc.invalidateQueries({ queryKey: ["project", pid] });
      qc.invalidateQueries({ queryKey: ["projects"] });
    },
    [qc, patchRow, pid, propagate],
  );

  const save = useCallback<PanelActions["save"]>(
    (unit, text, state) => {
      const target = text === "" ? null : text;
      let pieces: Piece[] | null = null;
      if (target !== null) {
        const r = splitTarget(target, unit.src);
        if (r.mismatch) {
          toast.error("Placeholders don't match the source", {
            description: [r.mismatch.missing.length && `missing ${r.mismatch.missing.length}`, r.mismatch.extra.length && `extra ${r.mismatch.extra.length}`]
              .filter(Boolean)
              .join(", "),
          });
          return;
        }
        pieces = r.pieces;
      }
      const newState: State = target === null ? "untranslated" : state;
      const prevRow = rows.find((r) => r.id === unit.id);
      const prevUnit = qc.getQueryData<Unit>(["unit", unit.id]);

      // Optimistic: show it saved and move on immediately.
      patchRow(unit.id, { tgt: pieces, state: newState, tgtLen: target?.length ?? 0 });
      if (prevUnit) qc.setQueryData<Unit>(["unit", unit.id], { ...prevUnit, tgt: pieces, state: newState });
      onDraft(unit.id, undefined);
      move(1);

      // Each save settles on its own (useMutation would only report the
      // latest of several quick saves).
      api.saveUnit(unit.id, { target, state: newState }).then(onSaved, (e) => {
        if (prevRow) patchRow(unit.id, prevRow);
        if (prevUnit) qc.setQueryData(["unit", unit.id], prevUnit);
        if (target !== null) onDraft(unit.id, target);
        toast.error(`Not saved: ${e instanceof ApiError ? e.message : String(e)}`, {
          action: { label: "Go to string", onClick: () => select(unit.id) },
        });
      });
    },
    [rows, qc, patchRow, onDraft, move, onSaved, select],
  );

  const actions = useMemo<PanelActions>(
    () => ({
      save,
      propagate,
      // Every string with this source, across files, so their contexts can be
      // compared before one translation is applied to all of them.
      showSame: (id: number) =>
        setUrl({
          same: String(id),
          unit: String(id),
          q: undefined,
          in: undefined,
          state: undefined,
          qa: undefined,
          changed: undefined,
          file: undefined,
          dir: undefined,
        }),
      next: () => move(1),
      prev: () => move(-1),
    }),
    [save, propagate, move, setUrl],
  );

  // Alt+↑/↓ also work when the editor is not focused.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!e.altKey || (e.key !== "ArrowDown" && e.key !== "ArrowUp")) return;
      if ((e.target as HTMLElement)?.closest?.(".cm-editor")) return; // handled by the editor keymap
      e.preventDefault();
      move(e.key === "ArrowDown" ? 1 : -1);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [move]);

  // --- filter bar -------------------------------------------------------------
  // Saved filters to restore, read once on open: only when the URL carries
  // none (a link with filters always wins).
  const [restoreFrom] = useState<Record<string, string> | null>(() => {
    if (typeof window === "undefined" || !pid) return null;
    const p = new URLSearchParams(window.location.search);
    if (REMEMBERED.some((k) => p.has(k)) || p.has("same")) return null;
    const saved = loadFilters(pid);
    return Object.keys(saved).length ? saved : null;
  });
  // The box keeps its own copy of the query (debounced into the URL).
  const [search, setSearch] = useState(filter.q ?? restoreFrom?.q ?? "");

  // Last filters and sort, remembered per project in this browser. They are
  // saved when the user changes them and restored when the editor opens
  // without any in the URL (see restoreFrom).
  const setFilters = useCallback(
    (patch: Record<string, string | undefined>) => {
      setUrl({ ...patch, unit: undefined });
      const saved = loadFilters(pid);
      for (const [k, v] of Object.entries(patch)) {
        if (!(REMEMBERED as readonly string[]).includes(k)) continue;
        if (v) saved[k] = v;
        else delete saved[k];
      }
      saveFilters(pid, saved);
    },
    [pid, setUrl],
  );
  useEffect(() => {
    if (restoreFrom) setUrl(restoreFrom);
  }, [restoreFrom, setUrl]);

  useEffect(() => {
    const t = setTimeout(() => {
      if ((filter.q ?? "") !== search) setFilters({ q: search || undefined });
    }, 150);
    return () => clearTimeout(t);
  }, [search, filter.q, setFilters]);


  const setScope = useCallback(
    (s: Scope) => setUrl({ file: s.file ? String(s.file) : undefined, dir: s.dir || undefined, unit: undefined }),
    [setUrl],
  );

  const confirm = useConfirm();
  const ask = async (title: string, description: string, run: () => void) => {
    if (await confirm({ title, description, action: "Apply" })) run();
  };
  const bulk = useMutation({
    mutationFn: (v: { state: State; noErrors: boolean }) => api.bulk(pid, filter, v),
    onSuccess: (r) => {
      toast.success(`${r.changed.toLocaleString()} strings updated`);
      qc.invalidateQueries({ queryKey: ["units", pid] });
      qc.invalidateQueries({ queryKey: ["count", pid] });
      qc.invalidateQueries({ queryKey: ["tree", pid] });
      qc.invalidateQueries({ queryKey: ["unit"] });
    },
    onError: (e) => toast.error(e.message),
  });
  const tmFill = useMutation({
    mutationFn: () => api.tmFill(pid, filter),
    onError: (e) => toast.error(e.message),
  });
  const mtBatch = useMutation({
    mutationFn: (v: { overwrite: boolean; glossary: boolean }) => api.mtBatch(pid, filter, v),
    onError: (e) => toast.error(e.message),
  });

  if (!pid) {
    return (
      <p className="p-8">
        No project selected. <Link href="/" className="underline">Back to projects</Link>
      </p>
    );
  }

  const selectedRow = selectedIndex >= 0 ? rows[selectedIndex] : undefined;
  const fileLabel = filter.file ? "file" : filter.dir ? `folder ${filter.dir}` : "project";

  return (
    <div className="flex h-dvh flex-col">
      {/* top bar */}
      <header className="flex items-center gap-2 border-b px-3 py-2">
        <Button asChild variant="ghost" size="icon-sm">
          <Link href="/" aria-label="Projects">
            <ArrowLeft />
          </Link>
        </Button>
        <span className="mr-2 font-semibold">{project.data?.name}</span>

        <div className="relative w-72">
          <Search className="pointer-events-none absolute left-2 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Search…" className="h-8 pl-8 pr-7" />
          {search && (
            <button type="button" className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground" onClick={() => setSearch("")}>
              <X className="size-3.5" />
            </button>
          )}
        </div>
        <FilterMenu
          filter={filter}
          qaChecks={info.data?.qaChecks ?? []}
          onChange={setFilters}
        />
        <SortMenu sort={sort} onChange={(v) => setFilters({ sort: v === "file" ? undefined : v })} />

        <span className="ml-1 text-xs tabular-nums text-muted-foreground">
          {count.data ? `${count.data.count.toLocaleString()} strings` : ""}
        </span>
        <div className="flex-1" />

        <Button asChild size="sm" variant="ghost">
          <Link href={`/glossary?project=${pid}`}>
            <BookMarked /> Glossary
          </Link>
        </Button>
        <DropdownMenu modal={false}>
          <DropdownMenuTrigger asChild>
            <Button size="sm" variant="outline">
              Bulk <ChevronDown />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-72">
            <DropdownMenuLabel className="text-xs font-normal text-muted-foreground">
              Applies to all {count.data?.count.toLocaleString()} strings matching the filter in this {fileLabel}
            </DropdownMenuLabel>
            <DropdownMenuItem onSelect={() => ask("Approve matching translations?", "Strings with QA errors are skipped.", () => bulk.mutate({ state: "approved", noErrors: true }))}>
              Approve (skip QA errors)
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => ask("Mark matching translations as translated?", "Every matching string that has a translation changes state.", () => bulk.mutate({ state: "translated", noErrors: false }))}>
              Mark as translated
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => ask("Reset matching translations to machine?", "Texts are kept; only the state changes, so they show up for review again.", () => bulk.mutate({ state: "mt", noErrors: false }))}>
              Reset to machine
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onSelect={() =>
                ask(
                  "Fill 100% matches from translation memory?",
                  "Untranslated and machine strings whose exact source has a human translation get it, as Translated.",
                  () => tmFill.mutate(),
                )
              }
            >
              Fill 100% matches from TM
            </DropdownMenuItem>
            {mtEnabled && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem onSelect={() => mtBatch.mutate({ overwrite: false, glossary: false })}>Machine-translate untranslated</DropdownMenuItem>
                <DropdownMenuItem
                  onSelect={() => ask("Re-translate machine strings?", "Untranslated and machine strings are translated again. Human translations are kept.", () => mtBatch.mutate({ overwrite: true, glossary: false }))}
                >
                  Re-translate machine strings
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem onSelect={() => mtBatch.mutate({ overwrite: false, glossary: true })}>Machine-translate untranslated + glossary</DropdownMenuItem>
                <DropdownMenuItem
                  onSelect={() =>
                    ask(
                      "Re-translate machine strings with the glossary?",
                      "Untranslated and machine strings are translated again, with glossary terms inserted in their agreed form. Human translations are kept.",
                      () => mtBatch.mutate({ overwrite: true, glossary: true }),
                    )
                  }
                >
                  Re-translate machine strings + glossary
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
        <ThemeToggle />
      </header>

      <div className="border-b px-3 empty:hidden [&:has(>*)]:py-2">
        <RunningJobs project={pid} />
      </div>

      {/* panes */}
      <div className="flex min-h-0 flex-1">
        <FilesPane project={pid} scope={{ file: filter.file, dir: filter.dir }} onScope={setScope} />
        <main className="min-w-0 flex-1">
          <UnitList
            rows={rows}
            selectedId={selectedId}
            errMask={ERR_MASK}
            drafts={draftIds}
            hasNext={!!units.hasNextPage}
            loading={units.isFetching}
            onSelect={select}
            onNeedMore={units.fetchNextPage}
          />
        </main>
        <aside className="w-[40%] min-w-[420px] max-w-[760px] shrink-0 border-l">
          {selectedId ? (
            <UnitPanel
              unitId={selectedId}
              row={selectedRow}
              draft={drafts.get(selectedId)}
              onDraft={onDraft}
              actions={actions}
              mtEnabled={mtEnabled}
              mtServer={info.data?.mtServer ?? undefined}
              extraLangs={langs.data}
            />
          ) : (
            <div className="p-8 text-sm text-muted-foreground">Select a string.</div>
          )}
        </aside>
      </div>
    </div>
  );
}
