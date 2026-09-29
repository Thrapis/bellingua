"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { LoaderCircle, Plus, Search, X } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { api, termTargets, type Term, type TermInput } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { cn } from "cn";

type Props = {
  project: number;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  /** Existing term to edit; otherwise a new term prefilled with `initial`. */
  term?: Term;
  initial?: Partial<TermInput>;
  onSaved?: (t: Term) => void;
};

/** A word-form row; `count` is set for forms found in the texts, `suggested` until the translation is edited. */
type FormRow = { src: string; tgt: string; count?: number; suggested?: boolean };

/** Everything that shows glossary data must refetch after a term changes. */
export function invalidateGlossary(qc: ReturnType<typeof useQueryClient>, project: number) {
  qc.invalidateQueries({ queryKey: ["terms", project] });
  qc.invalidateQueries({ queryKey: ["unit"] });
}

/**
 * Guesses the translation of a source form from the base pair: the source
 * form's ending replaces the base's (when the translation ends the same way),
 * with the usual Russian → Belarusian spelling of endings (и → і, unstressed
 * о → а). «Бауман»/«Баўман» + «Баумана» → «Баўмана»; «Орбита»/«Орбіта» +
 * «Орбиты» → «Орбіты». Only a suggestion: the dialog marks it for review.
 */
function suggestForm(base: string, target: string, form: string): string {
  if (!base || !target) return "";
  const b = base.toLowerCase();
  const f = form.toLowerCase();
  let p = 0;
  while (p < b.length && p < f.length && b[p] === f[p]) p++;
  const dropped = b.slice(p);
  if (dropped.length > 2 || !target.toLowerCase().endsWith(dropped)) return "";
  // The ending in lower case: insertion copies the occurrence's case anyway
  // («БАУМАНА» gets «БАЎМАНА»), so a mixed «баўманА» would only be wrong.
  const ending = f.slice(p).replace(/и/g, "і").replace(/о/g, "а");
  return target.slice(0, target.length - dropped.length) + ending;
}

export function TermDialog({ project, open, onOpenChange, term, initial, onSaved }: Props) {
  const qc = useQueryClient();
  const start = term ?? initial ?? {};
  const [source, setSource] = useState(start.source ?? "");
  const [target, setTarget] = useState(start.target ?? "");
  const [note, setNote] = useState(start.note ?? "");
  const [dnt, setDnt] = useState(start.dnt ?? false);
  const [caseSensitive, setCaseSensitive] = useState(start.caseSensitive ?? false);
  const [forms, setForms] = useState<FormRow[]>(() => (start.forms ?? []).map((f) => ({ ...f })));

  const setRow = (i: number, patch: Partial<FormRow>) => setForms((fs) => fs.map((f, j) => (j === i ? { ...f, ...patch } : f)));

  const find = useMutation({
    mutationFn: () => api.termForms(project, source.trim()),
    onSuccess: (found) => {
      const have = new Set(forms.map((f) => f.src.trim().toLowerCase()));
      const base = termTargets({ target })[0] ?? "";
      const fresh = found
        .filter((f) => !have.has(f.form.toLowerCase()))
        .map((f): FormRow => {
          const tgt = suggestForm(source.trim(), base, f.form);
          return { src: f.form, tgt, count: f.count, suggested: tgt !== "" };
        });
      // Keep counts fresh on rows that already exist.
      const counts = new Map(found.map((f) => [f.form.toLowerCase(), f.count]));
      setForms((fs) => [...fs.map((f) => ({ ...f, count: counts.get(f.src.trim().toLowerCase()) ?? f.count })), ...fresh]);
      if (!found.length) toast.info("No other forms of this term in the texts.");
      else if (!fresh.length) toast.info("All forms found in the texts are listed already.");
    },
    onError: (e) => toast.error(e.message),
  });

  const save = useMutation({
    mutationFn: () => {
      const body: TermInput = {
        source: source.trim(),
        target: target.trim(),
        note: note.trim(),
        dnt,
        caseSensitive,
        forms: dnt ? [] : forms.map((f) => ({ src: f.src.trim(), tgt: f.tgt.trim() })).filter((f) => f.src && f.tgt),
      };
      return term ? api.updateTerm(term.id, body) : api.createTerm(project, body);
    },
    onSuccess: (t) => {
      invalidateGlossary(qc, project);
      toast.success(term ? "Term updated" : `Term «${t.source}» added`, {
        description: "Glossary QA is rechecked in the background.",
      });
      onOpenChange(false);
      onSaved?.(t);
    },
    onError: (e) => toast.error(e.message),
  });

  const anySuggested = forms.some((f) => f.suggested);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>{term ? "Edit term" : "Add term"}</DialogTitle>
            <DialogDescription>
              Source terms match all word forms in Russian («Орбита» also finds «Орбиты»). Separate alternative translations
              with <code>|</code>.
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="term-source">Source</Label>
            <Input id="term-source" autoFocus value={source} onChange={(e) => setSource(e.target.value)} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="term-target">Translation</Label>
            <Input
              id="term-target"
              value={target}
              onChange={(e) => setTarget(e.target.value)}
              disabled={dnt}
              placeholder={dnt ? "kept as in the source" : "e.g. нетранер | хакер"}
            />
          </div>

          {!dnt && (
            <div className="grid gap-1.5">
              <div className="flex items-center justify-between gap-2">
                <Label>Word forms</Label>
                <span className="flex gap-1">
                  <Button
                    type="button"
                    size="xs"
                    variant="ghost"
                    disabled={!source.trim() || find.isPending}
                    onClick={() => find.mutate()}
                    title="List the forms of the source term that occur in this project's strings"
                  >
                    {find.isPending ? <LoaderCircle className="animate-spin" /> : <Search />} Find forms in the texts
                  </Button>
                  <Button type="button" size="xs" variant="ghost" onClick={() => setForms((fs) => [...fs, { src: "", tgt: "" }])}>
                    <Plus /> Add form
                  </Button>
                </span>
              </div>
              {forms.length === 0 ? (
                <p className="text-xs text-muted-foreground">
                  Optional. Give a translation per form («Баумана» → «Баўмана») and it is used when inserting the term and in
                  glossary MT; other forms get the base translation.
                </p>
              ) : (
                <div className="max-h-72 overflow-y-auto rounded-md border">
                  <table className="w-full text-sm">
                    <thead className="sticky top-0 bg-muted text-left text-xs text-muted-foreground">
                      <tr>
                        <th className="px-2 py-1.5 font-medium">In the source</th>
                        <th className="px-2 py-1.5 font-medium">Translation</th>
                        <th className="w-8" />
                      </tr>
                    </thead>
                    <tbody>
                      {forms.map((f, i) => (
                        <tr key={i} className="border-t">
                          <td className="px-1.5 py-1">
                            <span className="flex items-center gap-1.5">
                              <Input
                                aria-label="Source form"
                                className="h-7"
                                value={f.src}
                                onChange={(e) => setRow(i, { src: e.target.value })}
                              />
                              {f.count !== undefined && (
                                <span className="w-8 shrink-0 text-right text-[10px] tabular-nums text-muted-foreground" title="Times it occurs">
                                  ×{f.count}
                                </span>
                              )}
                            </span>
                          </td>
                          <td className="px-1.5 py-1">
                            <Input
                              aria-label="Translation of this form"
                              className={cn("h-7", f.suggested && "italic text-muted-foreground")}
                              value={f.tgt}
                              placeholder="translation"
                              onChange={(e) => setRow(i, { tgt: e.target.value, suggested: false })}
                            />
                          </td>
                          <td className="pr-1">
                            <Button
                              type="button"
                              size="icon-xs"
                              variant="ghost"
                              aria-label="Remove form"
                              onClick={() => setForms((fs) => fs.filter((_, j) => j !== i))}
                            >
                              <X />
                            </Button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              {anySuggested && (
                <p className="text-xs text-muted-foreground">
                  <i>Italic</i> translations are suggested from the endings — check them. Forms left without a translation are
                  not saved.
                </p>
              )}
            </div>
          )}

          <div className="grid gap-1.5">
            <Label htmlFor="term-note">Note</Label>
            <Input id="term-note" value={note} onChange={(e) => setNote(e.target.value)} placeholder="meaning, usage, gender…" />
          </div>
          <div className="flex flex-wrap gap-5 text-sm">
            <label className="flex items-center gap-2">
              <Checkbox checked={dnt} onCheckedChange={(v) => setDnt(v === true)} />
              Do not translate
            </label>
            <label className="flex items-center gap-2">
              <Checkbox checked={caseSensitive} onCheckedChange={(v) => setCaseSensitive(v === true)} />
              Case-sensitive (a name, not a common word)
            </label>
          </div>
          <DialogFooter>
            <Button type="submit" disabled={!source.trim() || save.isPending}>
              {term ? "Save" : "Add term"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
