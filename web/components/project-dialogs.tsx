"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";
import { api, ApiError, type Project, type State } from "@/lib/api";
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

// Last-used paths are a per-browser convenience only.
function remembered(key: string, def = ""): string {
  try {
    return localStorage.getItem(`bellingua:${key}`) ?? def;
  } catch {
    return def;
  }
}
function remember(key: string, v: string) {
  try {
    localStorage.setItem(`bellingua:${key}`, v);
  } catch {}
}

const errText = (e: unknown) => (e instanceof ApiError || e instanceof Error ? e.message : String(e));

type DialogProps = { open: boolean; onOpenChange: (v: boolean) => void };

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="grid gap-1.5">
      <Label>{label}</Label>
      {children}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  );
}

export function NewProjectDialog({ open, onOpenChange, onCreated }: DialogProps & { onCreated: (p: Project) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [src, setSrc] = useState("en");
  const [tgt, setTgt] = useState("");
  const create = useMutation({
    mutationFn: () => api.createProject({ name: name.trim(), sourceLang: src.trim(), targetLang: tgt.trim() }),
    onSuccess: (p) => {
      qc.invalidateQueries({ queryKey: ["projects"] });
      onOpenChange(false);
      setName("");
      onCreated(p);
    },
    onError: (e) => toast.error(errText(e)),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>New project</DialogTitle>
            <DialogDescription>One project per source → target language pair.</DialogDescription>
          </DialogHeader>
          <Field label="Name">
            <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="my-app" />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Source language">
              <Input value={src} onChange={(e) => setSrc(e.target.value)} />
            </Field>
            <Field label="Target language">
              <Input value={tgt} onChange={(e) => setTgt(e.target.value)} placeholder="e.g. de" />
            </Field>
          </div>
          <DialogFooter>
            <Button type="submit" disabled={!name.trim() || !src.trim() || !tgt.trim() || create.isPending}>
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function ImportDialog({ open, onOpenChange, project }: DialogProps & { project: Project }) {
  const [as, setAs] = useState<"main" | "extra">("main");
  const key = (a: string) => (a === "extra" ? `import-lang:${project.id}` : `import:${project.id}`);
  const [dir, setDir] = useState(() => remembered(key("main")));
  const [format, setFormat] = useState("auto");
  const [lang, setLang] = useState("");
  const run = useMutation({
    mutationFn: () =>
      as === "extra"
        ? api.importLang(project.id, { dir: dir.trim(), lang: lang.trim() })
        : api.importDir(project.id, { dir: dir.trim(), format: format === "auto" ? "" : format }),
    onSuccess: () => {
      remember(key(as), dir.trim());
      onOpenChange(false);
    },
    onError: (e) => toast.error(errText(e)),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            run.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>Import into {project.name}</DialogTitle>
            <DialogDescription>
              {as === "main"
                ? "Re-importing merges by file path and key: human translations are never overwritten, changed sources are flagged, missing units become obsolete."
                : "An XLIFF tree laid out like the main import (same paths, ids and <source>), with the other language in <target>. It is shown as a read-only reference and replaces that language's previous import."}
            </DialogDescription>
          </DialogHeader>
          <Field label="Import as">
            <Select
              value={as}
              onValueChange={(v) => {
                setAs(v as "main" | "extra");
                setDir(remembered(key(v)));
              }}
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="main">
                  Strings and {project.targetLang} translations
                </SelectItem>
                <SelectItem value="extra">Other language (reference)</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field label="Folder (on this machine)">
            <Input
              value={dir}
              onChange={(e) => setDir(e.target.value)}
              placeholder={as === "main" ? "C:\\path\\to\\xliff" : "C:\\path\\to\\xliff-en"}
            />
          </Field>
          {as === "extra" ? (
            <Field label="Language">
              <Input value={lang} onChange={(e) => setLang(e.target.value)} placeholder="from the files' target-language (e.g. en, uk)" />
            </Field>
          ) : (
            <Field label="Format">
              <Select value={format} onValueChange={setFormat}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="auto">By extension (.xlf / .csv)</SelectItem>
                  <SelectItem value="xliff">XLIFF 1.2</SelectItem>
                  <SelectItem value="crowdin-csv">Crowdin CSV</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          )}
          <DialogFooter>
            <Button type="submit" disabled={!dir.trim() || run.isPending}>
              Import
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function ExportDialog({ open, onOpenChange, project }: DialogProps & { project: Project }) {
  const [dir, setDir] = useState(() => remembered(`export:${project.id}`));
  const [format, setFormat] = useState("auto");
  const [minState, setMinState] = useState<State>("mt");
  const run = useMutation({
    mutationFn: () => api.exportDir(project.id, { dir: dir.trim(), format: format === "auto" ? "" : format, minState }),
    onSuccess: () => {
      remember(`export:${project.id}`, dir.trim());
      onOpenChange(false);
    },
    onError: (e) => toast.error(errText(e)),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            run.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>Export {project.name}</DialogTitle>
            <DialogDescription>Writes the same folder layout as imported.</DialogDescription>
          </DialogHeader>
          <Field label="Output folder">
            <Input value={dir} onChange={(e) => setDir(e.target.value)} placeholder="C:\path\to\xliff-out" />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Format">
              <Select value={format} onValueChange={setFormat}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="auto">As imported</SelectItem>
                  <SelectItem value="xliff">XLIFF 1.2</SelectItem>
                  <SelectItem value="crowdin-csv">Crowdin CSV</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field label="Include translations from">
              <Select value={minState} onValueChange={(v) => setMinState(v as State)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="mt">Machine and up</SelectItem>
                  <SelectItem value="translated">Translated and up</SelectItem>
                  <SelectItem value="approved">Approved only</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </div>
          <DialogFooter>
            <Button type="submit" disabled={!dir.trim() || run.isPending}>
              Export
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function SettingsDialog({ open, onOpenChange, project }: DialogProps & { project: Project }) {
  const qc = useQueryClient();
  const info = useQuery({ queryKey: ["info"], queryFn: api.info });
  const [forbidden, setForbidden] = useState(project.settings.qa.forbidden);
  const [ratio, setRatio] = useState(String(project.settings.qa.maxLengthRatio));
  const [disabled, setDisabled] = useState<string[]>(project.settings.qa.disabled ?? []);
  const save = useMutation({
    mutationFn: async () => {
      await api.saveSettings(project.id, {
        qa: { forbidden, maxLengthRatio: Number(ratio) || 0, disabled },
      });
      return api.qaRecheck(project.id);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["projects"] });
      onOpenChange(false);
      toast.info("Settings saved — rechecking QA");
    },
    onError: (e) => toast.error(errText(e)),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{project.name} settings</DialogTitle>
          <DialogDescription>QA checks run on every save and import.</DialogDescription>
        </DialogHeader>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Forbidden letters in target" hint="Case-insensitive, e.g. letters the target alphabet lacks.">
            <Input value={forbidden} onChange={(e) => setForbidden(e.target.value)} />
          </Field>
          <Field label="Max length ratio" hint="Target/source length; 0 disables.">
            <Input type="number" step="0.1" min="0" value={ratio} onChange={(e) => setRatio(e.target.value)} />
          </Field>
        </div>
        <div className="grid gap-2">
          <Label>Enabled checks</Label>
          <div className="grid grid-cols-2 gap-2">
            {info.data?.qaChecks.map((c) => (
              <label key={c.id} className="flex items-center gap-2 text-sm">
                <Checkbox
                  checked={!disabled.includes(c.id)}
                  onCheckedChange={(v) => setDisabled((d) => (v ? d.filter((x) => x !== c.id) : [...d, c.id]))}
                />
                {c.title}
                {c.severity === "error" && <span className="text-xs text-destructive">error</span>}
              </label>
            ))}
          </div>
        </div>
        <DialogFooter>
          <Button onClick={() => save.mutate()} disabled={save.isPending}>
            Save & recheck
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
