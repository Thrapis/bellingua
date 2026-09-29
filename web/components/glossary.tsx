"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useDeferredValue, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { ArrowLeft, BookMarked, Download, Pencil, Plus, Search, Trash2, Upload } from "lucide-react";
import { api, termTargets, type Term } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useConfirm } from "@/components/confirm";
import { TermDialog, invalidateGlossary } from "@/components/term-dialog";
import { ThemeToggle } from "@/components/theme-toggle";

const LIMIT = 500; // rows rendered at once; search narrows the rest

export function Glossary() {
  const sp = useSearchParams();
  const pid = Number(sp.get("project"));
  const qc = useQueryClient();
  const confirm = useConfirm();
  const project = useQuery({ queryKey: ["project", pid], queryFn: () => api.project(pid), enabled: pid > 0 });
  const terms = useQuery({ queryKey: ["terms", pid], queryFn: () => api.terms(pid), enabled: pid > 0 });
  const [search, setSearch] = useState("");
  const q = useDeferredValue(search.trim().toLowerCase());
  const [dialog, setDialog] = useState<{ term?: Term } | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);

  const filtered = useMemo(() => {
    const all = terms.data ?? [];
    if (!q) return all;
    return all.filter((t) => `${t.source}\n${t.target}\n${t.note}`.toLowerCase().includes(q));
  }, [terms.data, q]);

  const del = useMutation({
    mutationFn: (t: Term) => api.deleteTerm(t.id),
    onSuccess: () => invalidateGlossary(qc, pid),
    onError: (e) => toast.error(e.message),
  });
  const importCsv = useMutation({
    mutationFn: async (f: File) => api.importTerms(pid, await f.text()),
    onSuccess: (r) => {
      invalidateGlossary(qc, pid);
      toast.success(`Glossary imported: ${r.inserted} new, ${r.updated} updated`, {
        description: "Glossary QA is rechecked in the background.",
      });
    },
    onError: (e) => toast.error(`Import failed: ${e.message}`),
  });

  if (!pid) {
    return (
      <p className="p-8">
        No project selected. <Link href="/" className="underline">Back to projects</Link>
      </p>
    );
  }

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-4 px-6 py-6">
      <header className="flex items-center gap-2">
        <Button asChild variant="ghost" size="icon-sm">
          <Link href="/" aria-label="Projects">
            <ArrowLeft />
          </Link>
        </Button>
        <BookMarked className="size-5" />
        <h1 className="text-lg font-semibold">{project.data?.name} glossary</h1>
        <span className="text-sm text-muted-foreground">{terms.data ? `${terms.data.length.toLocaleString()} terms` : ""}</span>
        <div className="flex-1" />
        <Button asChild variant="ghost" size="sm">
          <Link href={`/editor?project=${pid}`}>Editor</Link>
        </Button>
        <ThemeToggle />
      </header>

      <div className="flex flex-wrap items-center gap-2">
        <div className="relative w-80">
          <Search className="pointer-events-none absolute left-2 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Search terms…" className="h-8 pl-8" />
        </div>
        <div className="flex-1" />
        <Button size="sm" onClick={() => setDialog({})}>
          <Plus /> Add term
        </Button>
        <Button size="sm" variant="outline" onClick={() => fileInput.current?.click()} disabled={importCsv.isPending}>
          <Upload /> Import CSV
        </Button>
        <input
          ref={fileInput}
          type="file"
          accept=".csv,text/csv"
          hidden
          onChange={(e) => {
            const f = e.target.files?.[0];
            if (f) importCsv.mutate(f);
            e.target.value = "";
          }}
        />
        <Button size="sm" variant="outline" asChild>
          <a href={api.exportTermsUrl(pid)} download>
            <Download /> Export CSV
          </a>
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        CSV columns: <code>source,target,note,dnt,case_sensitive,forms</code> (forms as <code>Баумана=Баўмана; Бауману=Баўману</code>). Import updates terms with the same source and adds the
        rest. Russian source terms match every word form; separate alternative translations with <code>|</code>.
      </p>

      <div className="overflow-hidden rounded-lg border">
        <table className="w-full text-sm">
          <thead className="bg-muted/50 text-left text-xs text-muted-foreground">
            <tr>
              <th className="px-3 py-2 font-medium">Source</th>
              <th className="px-3 py-2 font-medium">Translation</th>
              <th className="px-3 py-2 font-medium">Note</th>
              <th className="w-20 px-3 py-2" />
            </tr>
          </thead>
          <tbody>
            {filtered.slice(0, LIMIT).map((t) => (
              <tr key={t.id} className="group border-t hover:bg-muted/40">
                <td className="px-3 py-1.5 align-top font-medium">
                  {t.source}
                  {t.caseSensitive && <span className="ml-1.5 text-[10px] font-normal text-muted-foreground">Aa</span>}
                </td>
                <td className="px-3 py-1.5 align-top">
                  {t.dnt ? (
                    <span className="text-xs text-muted-foreground">do not translate</span>
                  ) : termTargets(t).length ? (
                    <>
                      {termTargets(t).join(" · ")}
                      {t.forms.length > 0 && (
                        <span
                          className="ml-1.5 text-[10px] text-muted-foreground"
                          title={t.forms.map((f) => `${f.src} → ${f.tgt}`).join("\n")}
                        >
                          +{t.forms.length} {t.forms.length === 1 ? "form" : "forms"}
                        </span>
                      )}
                    </>
                  ) : (
                    <span className="italic text-muted-foreground">—</span>
                  )}
                </td>
                <td className="px-3 py-1.5 align-top text-muted-foreground">{t.note}</td>
                <td className="px-2 py-1 text-right align-top">
                  <span className="inline-flex opacity-0 group-hover:opacity-100">
                    <Button size="icon-xs" variant="ghost" aria-label="Edit" onClick={() => setDialog({ term: t })}>
                      <Pencil />
                    </Button>
                    <Button
                      size="icon-xs"
                      variant="ghost"
                      aria-label="Delete"
                      onClick={async () => {
                        if (await confirm({ title: `Delete «${t.source}»?`, action: "Delete", destructive: true })) del.mutate(t);
                      }}
                    >
                      <Trash2 />
                    </Button>
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {terms.data && filtered.length === 0 && (
          <p className="p-8 text-center text-sm text-muted-foreground">
            {terms.data.length ? "No terms match." : "No terms yet. Add one, import a CSV, or select a word in the editor's source and press Ctrl+Shift+G."}
          </p>
        )}
        {filtered.length > LIMIT && (
          <p className="border-t p-2 text-center text-xs text-muted-foreground">
            Showing {LIMIT} of {filtered.length.toLocaleString()} — search to narrow down.
          </p>
        )}
      </div>

      {dialog && <TermDialog project={pid} open onOpenChange={(o) => !o && setDialog(null)} term={dialog.term} />}
    </div>
  );
}
