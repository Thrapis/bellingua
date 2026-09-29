"use client";

import { useQuery } from "@tanstack/react-query";
import {
  Check,
  CheckCheck,
  CircleAlert,
  Clipboard,
  Copy,
  History,
  Info,
  BookMarked,
  Database,
  Globe,
  Languages,
  Lock,
  ListFilter,
  ShieldCheck,
  LoaderCircle,
  Pencil,
  Plus,
  RotateCcw,
  Sparkles,
  TriangleAlert,
} from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { api, termTargets, type MTResult, type MTServerStatus, type ProjectLang, type Piece, type Row, type State, type Term, type TermInput, type TermMatch, type Unit } from "@/lib/api";
import { codesOf, plain, splitTarget } from "@/lib/pieces";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { PiecesView, StateBadge, stateLabel, type Mark } from "@/components/pieces";
import { TermDialog } from "@/components/term-dialog";
import { TMList } from "@/components/tm-section";
import { Empty, PanelSection, PanelSections, usePanelOpen } from "@/components/panel-sections";
import { TargetEditor, type EditorKeys, type TargetEditorHandle } from "@/components/target-editor";
import { chipLabel } from "@/lib/pieces";

export type PanelActions = {
  save: (unit: { id: number; src: Piece[] }, text: string, state: State) => void;
  /** Copy this unit's human translation to identical untranslated/MT strings. */
  propagate: (id: number) => void;
  /** Filter the list to every string with this unit's source. */
  showSame: (id: number) => void;
  next: () => void;
  prev: () => void;
};

type Props = {
  unitId: number;
  row: Row | undefined;
  draft: string | undefined;
  onDraft: (id: number, text: string | undefined) => void;
  actions: PanelActions;
  mtEnabled: boolean;
  mtServer?: MTServerStatus;
  /** The project's reference languages; the section is hidden without any. */
  extraLangs?: ProjectLang[];
};

/** A term's translations to offer: the forms used in this string first, then the base translations. */
const termChoices = (t: TermMatch) => [...new Set([...(t.inserts ?? []).filter(Boolean), ...termTargets(t)])];

/**
 * True when the user is working in another field (the search box, a filter,
 * a dialog). A newly loaded unit then must not steal focus: a search that
 * auto-selects its first result would otherwise interrupt typing. Clicking a
 * row, Alt+↑/↓ and Ctrl+Enter leave focus on the page or in the editor, so
 * those still jump straight into the translation.
 */
function focusIsElsewhere(): boolean {
  const el = document.activeElement as HTMLElement | null;
  if (!el || el === document.body || el.closest(".cm-editor")) return false;
  return el.matches("input, textarea, select, [contenteditable=true]") || !!el.closest("[role=dialog], [role=listbox], [role=menu]");
}

const fmtTime = (s: number) => (s ? new Date(s * 1000).toLocaleString() : "");

export function UnitPanel({ unitId, row, draft, onDraft, actions, mtEnabled, mtServer, extraLangs }: Props) {
  const mtReady = !mtServer || mtServer.state === "ready" || mtServer.state === "external";
  const unit = useQuery({ queryKey: ["unit", unitId], queryFn: () => api.unit(unitId), staleTime: 60_000 });
  const u = unit.data;

  // A list row that was not truncated already carries the full texts, so the
  // editor can open before the details arrive.
  const src: Piece[] | undefined = u?.src ?? (row && !row.trunc ? row.src : undefined);
  const savedTgt: Piece[] | null | undefined = u ? u.tgt : row && !row.trunc ? row.tgt : undefined;
  const state: State | undefined = u?.state ?? row?.state;
  const ready = src !== undefined && savedTgt !== undefined;

  const [panelOpen, setPanelOpen] = usePanelOpen();
  const mtOpen = panelOpen.includes("mt");
  const [settled, setSettled] = useState<number | null>(null);
  useEffect(() => {
    const t = setTimeout(() => setSettled(unitId), 300);
    return () => clearTimeout(t);
  }, [unitId]);

  const mt = useQuery({
    queryKey: ["mt", unitId],
    queryFn: ({ signal }) => api.unitMT(unitId, signal),
    // Translates on its own while the section is open, once the unit has been
    // shown for a moment: stepping through strings with Alt+↓ must not queue
    // a model run per string on the server.
    enabled: mtEnabled && mtReady && ready && mtOpen && settled === unitId,
    staleTime: Infinity,
    retry: false,
  });

  // Reference languages load only while their section is open.
  const hasLangs = !!extraLangs?.length;
  const langs = useQuery({
    queryKey: ["unit-langs", unitId],
    queryFn: ({ signal }) => api.unitLangs(unitId, signal),
    enabled: hasLangs && panelOpen.includes("langs"),
    staleTime: 60_000,
    retry: false,
  });

  const tm = useQuery({
    queryKey: ["tm", unitId],
    queryFn: ({ signal }) => api.unitTM(unitId, signal),
    staleTime: 30_000,
    retry: false,
  });

  const editor = useRef<TargetEditorHandle>(null);
  const sourceBox = useRef<HTMLDivElement>(null);
  const [termDialog, setTermDialog] = useState<{ term?: Term; initial?: Partial<TermInput> } | null>(null);
  const [text, setText] = useState("");
  const loadedFor = useRef<number | null>(null);

  // Load the editor once per unit (not on every refetch, which would wipe
  // what the user is typing).
  useEffect(() => {
    if (!ready || loadedFor.current === unitId) return;
    loadedFor.current = unitId;
    const initial = draft ?? plain(savedTgt);
    editor.current?.load(initial, codesOf(src));
    setText(initial);
    if (!focusIsElsewhere()) editor.current?.focus();
  }, [ready, unitId, draft, savedTgt, src]);

  const check = useMemo(() => (src ? splitTarget(text, src) : null), [text, src]);
  const dirty = ready && text !== plain(savedTgt);

  const apply = (t: string) => {
    editor.current?.load(t, codesOf(src ?? []));
    setText(t);
    onDraft(unitId, t === plain(savedTgt) ? undefined : t);
    editor.current?.focus();
  };

  // Text selected inside the source view (for "Add term").
  const sourceSelection = () => {
    const sel = window.getSelection();
    if (!sel || sel.isCollapsed || !sourceBox.current?.contains(sel.anchorNode)) return "";
    return sel.toString().trim();
  };
  const addTerm = () => {
    if (!u) return;
    setTermDialog({ initial: { source: sourceSelection(), target: editor.current?.selection().trim() ?? "" } });
  };

  // Glossary matches: underline in the source; click inserts the translation.
  const insertText = (v: string) => editor.current?.insert(v);
  const insertTerm = (t: Term) => insertText(t.dnt ? t.source : (termTargets(t)[0] ?? t.source));
  // A highlighted occurrence inserts the translation of its own form.
  const marks: Mark[] =
    u?.terms.flatMap((t) =>
      t.spans.map(([start, end], i) => {
        const ins = t.inserts?.[i];
        const occ = src ? plain(src).slice(start, end) : "";
        const form = ins && !t.dnt && !termTargets(t).includes(ins) ? `«${occ}» → «${ins}»\n` : "";
        return {
          start,
          end,
          title: `${form}${t.source} → ${t.dnt ? "(do not translate)" : termTargets(t).join(" / ") || "—"}${t.note ? `\n${t.note}` : ""}`,
          onClick: () => (ins ? insertText(ins) : insertTerm(t)),
        };
      }),
    ) ?? [];

  const doSave = (st: State) => {
    if (!src) return;
    actions.save({ id: unitId, src }, editor.current?.value() ?? text, st);
  };

  const keys = useRef<EditorKeys>({} as EditorKeys);
  useEffect(() => {
    keys.current = {
      save: () => doSave("translated"),
      approve: () => doSave("approved"),
      mt: (glossary) => {
        const pick = (d: MTResult) => apply(plain(glossary && d.glossary ? d.glossary : d.tgt));
        if (mt.data) pick(mt.data);
        else mt.refetch().then((r) => r.data && pick(r.data));
      },
      copySource: () => src && apply(plain(src)),
      next: actions.next,
      prev: actions.prev,
      insertCode: (n) => {
        const c = src ? codesOf(src)[n] : undefined;
        if (c) editor.current?.insert(c);
      },
      addTerm,
      applyTM: (n) => {
        const m = tm.data?.matches[n];
        if (m) apply(plain(m.tgt));
      },
    };
  });

  // Alt+M / Alt+Shift+M, in the editor or not (Alt, not Ctrl: Firefox mutes
  // the tab on Ctrl+M). Matched on the physical key, so any layout works.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!e.altKey || e.ctrlKey || e.metaKey || e.code !== "KeyM") return;
      e.preventDefault();
      keys.current.mt(e.shiftKey);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const errors = u?.issues.filter((i) => i.severity === "error") ?? [];
  const warnings = u?.issues.filter((i) => i.severity === "warning") ?? [];

  return (
    <div className="flex h-full flex-col">
      {/* header */}
      <div className="flex items-center gap-2 border-b px-4 py-2 text-xs">
        {state && <StateBadge state={state} />}
        {(u?.changed || row?.changed) && (
          <span className="rounded-full border border-sky-500/40 px-2 py-0.5 text-sky-600">source changed</span>
        )}
        <span className="min-w-0 flex-1 truncate font-mono text-muted-foreground" title={u?.path}>
          {u?.path} · {row?.key ?? u?.key}
        </span>
      </div>

      <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
        {/* source */}
        <section className="border-b px-4 py-3">
          <div className="mb-1 flex items-center gap-2 text-xs text-muted-foreground">
            <span className="font-medium uppercase tracking-wide">Source</span>
            {src && codesOf(src).length > 0 && <span>· click a tag to insert it (Ctrl+1…9)</span>}
            <div className="flex-1" />
            {src && <span className="tabular-nums">{plain(src).length.toLocaleString()} chars</span>}
            <IconButton label="Add glossary term from the selection (Ctrl+Shift+G)" onClick={addTerm}>
              <BookMarked />
            </IconButton>
            <IconButton label="Copy source to translation (Ctrl+Shift+C)" onClick={() => src && apply(plain(src))}>
              <Copy />
            </IconButton>
          </div>
          <div ref={sourceBox} className="max-h-72 overflow-y-auto text-[0.95rem] leading-relaxed">
            {src ? (
              <PiecesView pieces={src} onCode={(c) => editor.current?.insert(c)} marks={marks} />
            ) : (
              <LoaderCircle className="size-4 animate-spin text-muted-foreground" />
            )}
          </div>
          {u?.context && (
            <p className="mt-2 flex items-start gap-1.5 text-xs text-muted-foreground">
              <Info className="mt-0.5 size-3.5 shrink-0" /> <span className="break-all">{u.context}</span>
            </p>
          )}
        </section>

        {/* target */}
        <section className="border-b">
          <div className="flex items-center gap-2 px-4 pt-3 text-xs text-muted-foreground">
            <span className="font-medium uppercase tracking-wide">Translation</span>
            {dirty && <span className="text-amber-600">edited</span>}
            <span className="flex-1" />
            <span className="tabular-nums">{text.length.toLocaleString()} chars</span>
          </div>
          <TargetEditor
            ref={editor}
            keys={keys}
            placeholder="Type the translation…"
            className="bl-editor max-h-[45vh] min-h-24 overflow-y-auto"
            onChange={(t) => {
              setText(t);
              onDraft(unitId, t === plain(savedTgt) ? undefined : t);
            }}
          />
          {check?.mismatch && (
            <div className="flex flex-wrap items-center gap-1 px-4 pb-2 text-xs text-destructive">
              <CircleAlert className="size-3.5" />
              {check.mismatch.missing.length > 0 && (
                <>
                  missing:
                  {check.mismatch.missing.map((c, i) => (
                    <button key={i} type="button" className="bl-chip bl-chip-missing" title={c} onClick={() => editor.current?.insert(c)}>
                      {chipLabel(c)}
                    </button>
                  ))}
                </>
              )}
              {check.mismatch.extra.length > 0 && <> extra: {check.mismatch.extra.map(chipLabel).join(" ")}</>}
            </div>
          )}
          <div className="flex items-center gap-2 px-4 pb-3">
            <Button size="sm" onClick={() => doSave("translated")} disabled={!ready || !!check?.mismatch}>
              <Check /> Save <Kbd>Ctrl+Enter</Kbd>
            </Button>
            <Button size="sm" variant="secondary" onClick={() => doSave("approved")} disabled={!ready || !!check?.mismatch}>
              <CheckCheck /> Approve <Kbd>Ctrl+Shift+Enter</Kbd>
            </Button>
            <div className="flex-1" />
            {dirty && (
              <IconButton label="Revert to saved" onClick={() => apply(plain(savedTgt))}>
                <RotateCcw />
              </IconButton>
            )}
          </div>
        </section>

        {/* identical strings still waiting for this translation */}
        {tm.data && tm.data.duplicates > 0 && u?.tgt && (u.state === "translated" || u.state === "approved") && !dirty && (
          <section className="flex items-center gap-2 border-b bg-sky-500/5 px-4 py-2 text-sm">
            <Copy className="size-3.5 shrink-0 text-sky-600" />
            <span className="flex-1">
              {tm.data.duplicates.toLocaleString()} identical {tm.data.duplicates === 1 ? "string has" : "strings have"} no human
              translation yet
            </span>
            <Button size="xs" variant="ghost" onClick={() => actions.showSame(unitId)} title="List every string with this source, with its context">
              <ListFilter /> Show all
            </Button>
            <Button size="xs" variant="outline" onClick={() => actions.propagate(unitId)}>
              Apply to {tm.data.duplicates.toLocaleString()}
            </Button>
          </section>
        )}

        <PanelSections open={panelOpen} onOpenChange={setPanelOpen}>
          <PanelSection
            value="tm"
            icon={Database}
            title="Translation memory"
            count={tm.data?.matches.length}
            tone={tm.data?.matches.some((m) => m.score === 100) ? "good" : "default"}
          >
            <TMList
              source={src}
              matches={tm.data?.matches}
              loading={tm.isFetching}
              error={tm.error?.message}
              onApply={(m) => apply(plain(m.tgt))}
            />
          </PanelSection>

          {mtEnabled && (
            <PanelSection
              value="mt"
              icon={Languages}
              title="Machine translation"
              actions={
                <Button size="xs" variant="ghost" onClick={() => mt.refetch()} disabled={mt.isFetching}>
                  <Sparkles /> {mt.data ? "Retranslate" : "Translate"} <Kbd>Alt+M</Kbd>
                </Button>
              }
            >
              {mt.isFetching ? (
                <LoaderCircle className="size-4 animate-spin text-muted-foreground" />
              ) : mt.error ? (
                <p className="text-xs text-destructive">{mt.error.message}</p>
              ) : mt.data ? (
                <div className="flex flex-col gap-2">
                  <button
                    type="button"
                    onClick={() => apply(plain(mt.data.tgt))}
                    className="w-full rounded-md border px-3 py-2 text-left text-sm hover:bg-muted"
                    title="Use this suggestion (Alt+M)"
                  >
                    <PiecesView pieces={mt.data.tgt} />
                    <span className="mt-1 block text-[10px] uppercase text-muted-foreground">{mt.data.provider}</span>
                  </button>
                  {mt.data.glossary &&
                    (plain(mt.data.glossary) === plain(mt.data.tgt) ? (
                      <p className="text-xs text-muted-foreground">The translation already uses the glossary terms.</p>
                    ) : (
                      <button
                        type="button"
                        onClick={() => apply(plain(mt.data.glossary!))}
                        className="w-full rounded-md border px-3 py-2 text-left text-sm hover:bg-muted"
                        title="Use this suggestion (Alt+Shift+M). Glossary terms were kept out of the model and inserted in their dictionary form; check the word endings"
                      >
                        <PiecesView pieces={mt.data.glossary} />
                        <span className="mt-1 block text-[10px] uppercase text-muted-foreground">
                          {mt.data.provider} + glossary
                        </span>
                      </button>
                    ))}
                </div>
              ) : mtServer?.state === "starting" ? (
                <p className="flex items-center gap-2 text-xs text-muted-foreground">
                  <LoaderCircle className="size-3.5 animate-spin" /> Translation server is starting (loading the model)…
                </p>
              ) : mtServer && !mtReady ? (
                <p className="whitespace-pre-wrap break-words text-xs text-destructive">
                  Translation server is not running{mtServer.message ? `: ${mtServer.message}` : "."}
                </p>
              ) : (
                <Empty>Press Translate (Alt+M) for a machine suggestion.</Empty>
              )}
            </PanelSection>
          )}

          {hasLangs && (
            <PanelSection value="langs" icon={Globe} title="Other languages" count={langs.data?.length}>
              {langs.isPending && langs.fetchStatus === "fetching" ? (
                <LoaderCircle className="size-4 animate-spin text-muted-foreground" />
              ) : langs.error ? (
                <p className="text-xs text-destructive">{langs.error.message}</p>
              ) : !langs.data?.length ? (
                <Empty>No other language has this string.</Empty>
              ) : (
                <div className="flex flex-col gap-2">
                  {langs.data.map((l) => (
                    <div key={l.lang} className="group relative">
                      <button
                        type="button"
                        onClick={() => apply(plain(l.tgt))}
                        className="w-full rounded-md border px-3 py-2 pr-9 text-left text-sm hover:bg-muted"
                        title="Copy into the editor"
                      >
                        <PiecesView pieces={l.tgt} />
                        <span className="mt-1 flex items-center gap-1.5 text-[10px] uppercase text-muted-foreground">
                          {l.locked && <Lock className="size-2.5" />}
                          {l.lang}
                          {l.outdated && (
                            <span className="normal-case text-amber-600 dark:text-amber-400" title="Made from a different source text">
                              · outdated
                            </span>
                          )}
                        </span>
                      </button>
                      <div className="absolute right-1.5 top-1.5 opacity-0 focus-within:opacity-100 group-hover:opacity-100">
                        <IconButton label="Copy to clipboard" onClick={() => copyText(plain(l.tgt))}>
                          <Clipboard />
                        </IconButton>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </PanelSection>
          )}

          <PanelSection
            value="glossary"
            icon={BookMarked}
            title="Glossary"
            count={u?.terms.length}
            actions={
              <Button size="xs" variant="ghost" onClick={addTerm} title="Add a term from the selection (Ctrl+Shift+G)">
                <Plus /> Term
              </Button>
            }
          >
            {!u?.terms.length ? (
              <Empty>No glossary terms in this string.</Empty>
            ) : (
              <ul className="grid gap-1.5">
                {u.terms.map((t) => (
                  <li key={t.id} className="group flex items-start gap-2">
                    <span className="min-w-0 flex-1">
                      <span className="font-medium">{t.source}</span>
                      <span className="text-muted-foreground"> → </span>
                      {t.dnt ? (
                        <button type="button" className="rounded border px-1.5 text-xs hover:bg-muted" onClick={() => insertTerm(t)}>
                          do not translate
                        </button>
                      ) : termChoices(t).length ? (
                        termChoices(t).map((v) => (
                          <button
                            key={v}
                            type="button"
                            className="mr-1 rounded border px-1.5 hover:bg-muted"
                            title="Insert"
                            onClick={() => insertText(v)}
                          >
                            {v}
                          </button>
                        ))
                      ) : (
                        <span className="italic text-muted-foreground">no translation yet</span>
                      )}
                      {t.note && <span className="block text-xs text-muted-foreground">{t.note}</span>}
                    </span>
                    <Button
                      size="icon-xs"
                      variant="ghost"
                      className="opacity-0 group-hover:opacity-100"
                      aria-label="Edit term"
                      onClick={() => setTermDialog({ term: t })}
                    >
                      <Pencil />
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </PanelSection>

          <PanelSection
            value="qa"
            icon={ShieldCheck}
            title="QA"
            count={u ? errors.length + warnings.length : undefined}
            tone={errors.length ? "error" : "warn"}
          >
            {!errors.length && !warnings.length ? (
              <Empty>No issues.</Empty>
            ) : (
              <ul className="grid gap-1">
                {[...errors, ...warnings].map((i, k) => (
                  <li key={k} className="flex items-start gap-2">
                    {i.severity === "error" ? (
                      <CircleAlert className="mt-0.5 size-3.5 shrink-0 text-destructive" />
                    ) : (
                      <TriangleAlert className="mt-0.5 size-3.5 shrink-0 text-amber-500" />
                    )}
                    <span className="break-words">{i.message}</span>
                  </li>
                ))}
              </ul>
            )}
          </PanelSection>

          <PanelSection value="history" icon={History} title="History" count={u?.history?.length ?? (u ? 0 : undefined)}>
            {!u?.history?.length ? (
              <Empty>Not edited yet.</Empty>
            ) : (
              <ul className="grid gap-2">
                {u.history.map((h, k) => (
                  <li key={k} className="group rounded-md border px-3 py-2">
                    <div className="mb-0.5 flex items-center gap-2 text-[11px] text-muted-foreground">
                      <span>{stateLabel[h.state]}</span>·<span>{h.origin}</span>·<span>{fmtTime(h.at)}</span>
                      <span className="flex-1" />
                      {h.target !== null && k > 0 && (
                        <button type="button" className="opacity-0 group-hover:opacity-100 hover:underline" onClick={() => apply(h.target!)}>
                          restore
                        </button>
                      )}
                    </div>
                    <div className="line-clamp-4 whitespace-pre-wrap break-words">
                      {h.target ?? <i className="text-muted-foreground">empty</i>}
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </PanelSection>
        </PanelSections>
      </div>
      {termDialog && u && (
        <TermDialog
          project={u.project}
          open
          onOpenChange={(o) => !o && setTermDialog(null)}
          term={termDialog.term}
          initial={termDialog.initial}
        />
      )}
    </div>
  );
}

function Kbd({ children }: { children: React.ReactNode }) {
  return <kbd className="ml-1 hidden rounded border px-1 font-sans text-[10px] opacity-60 xl:inline">{children}</kbd>;
}

function copyText(t: string) {
  navigator.clipboard.writeText(t).then(
    () => toast.success("Copied"),
    (e: Error) => toast.error(`Copy failed: ${e.message}`),
  );
}

function IconButton({ label, onClick, children }: { label: string; onClick: () => void; children: React.ReactNode }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button variant="ghost" size="icon-xs" onClick={onClick} aria-label={label}>
          {children}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

export type { Unit };
