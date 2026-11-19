"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";
import { api, ApiError, type CrowdinTarget, type Project, type State } from "@/lib/api";
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

/**
 * Crowdin: without `project` it copies a Crowdin project into a new one; with
 * one it shows the link and pulls again. Read-only towards Crowdin; the token
 * is set in bellingua.yaml or the environment and never shown.
 */
export function CrowdinDialog({ open, onOpenChange, project }: DialogProps & { project?: Project }) {
  const qc = useQueryClient();
  const info = useQuery({
    queryKey: ["crowdin", project?.id],
    queryFn: () => api.crowdin(project!.id),
    enabled: !!project,
  });
  const link = info.data?.link;
  const tokenSet = project ? info.data?.tokenSet : undefined;
  const [remote, setRemote] = useState("");
  const [org, setOrg] = useState("");
  const [lang, setLang] = useState("");
  const [name, setName] = useState("");
  const target = (): CrowdinTarget => ({
    remoteId: Number(remote),
    organization: org.trim() || undefined,
    targetLang: lang.trim() || undefined,
  });
  const done = () => {
    qc.invalidateQueries({ queryKey: ["crowdin"] });
    qc.invalidateQueries({ queryKey: ["projects"] });
    onOpenChange(false);
  };
  const copy = useMutation({
    mutationFn: () => api.copyCrowdin({ ...target(), name: name.trim() || undefined }),
    onSuccess: () => {
      toast.info("Copying the Crowdin project…");
      done();
    },
    onError: (e) => toast.error(errText(e)),
  });
  const relink = useMutation({
    mutationFn: () => api.linkCrowdin(project!.id, target()),
    onSuccess: () => {
      toast.success("Linked");
      qc.invalidateQueries({ queryKey: ["crowdin"] });
    },
    onError: (e) => toast.error(errText(e)),
  });
  const pull = useMutation({
    mutationFn: () => api.pullCrowdin(project!.id),
    onSuccess: done,
    onError: (e) => toast.error(errText(e)),
  });
  const validId = Number.isInteger(Number(remote)) && Number(remote) > 0;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (!project) copy.mutate();
            else if (!link) relink.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>{project ? "Crowdin project" : "Copy a Crowdin project"}</DialogTitle>
            <DialogDescription>
              Reads sources, translations and approvals from Crowdin; nothing is sent back. Pull again later to merge
              new strings; translations you wrote here are kept.
            </DialogDescription>
          </DialogHeader>
          {tokenSet === false && (
            <p className="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm">
              No Crowdin token. Set <code>crowdin.token</code> in bellingua.yaml or the{" "}
              <code>BELLINGUA_CROWDIN_TOKEN</code> environment variable (a personal access token with read-only
              scopes) and restart bellingua.
            </p>
          )}
          {project && link && (
            <div className="rounded-md border p-3 text-sm">
              <p>
                Linked to Crowdin project <span className="font-medium">{link.identifier || link.remoteId}</span> (id{" "}
                {link.remoteId}), language {link.targetLang}.
              </p>
              <p className="text-muted-foreground">
                {link.lastPull ? `Last pulled ${new Date(link.lastPull * 1000).toLocaleString()}.` : "Not pulled yet."}
              </p>
            </div>
          )}
          {(!project || !link) && (
            <>
              <Field label="Crowdin project id" hint="The number in the project's API settings (Tools → API), not the URL slug.">
                <Input autoFocus value={remote} onChange={(e) => setRemote(e.target.value)} inputMode="numeric" />
              </Field>
              <div className="grid grid-cols-2 gap-3">
                <Field label="Target language" hint="Crowdin language id, e.g. be. Optional with one target language.">
                  <Input value={lang} onChange={(e) => setLang(e.target.value)} />
                </Field>
                <Field label="Enterprise organization" hint="Leave empty for crowdin.com.">
                  <Input value={org} onChange={(e) => setOrg(e.target.value)} />
                </Field>
              </div>
              {!project && (
                <Field label="Project name" hint="Default: the Crowdin project's name.">
                  <Input value={name} onChange={(e) => setName(e.target.value)} />
                </Field>
              )}
            </>
          )}
          <DialogFooter>
            {project && link ? (
              <Button type="button" disabled={pull.isPending || tokenSet === false} onClick={() => pull.mutate()}>
                Pull from Crowdin
              </Button>
            ) : (
              <Button type="submit" disabled={!validId || copy.isPending || relink.isPending}>
                {project ? "Link" : "Copy project"}
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
