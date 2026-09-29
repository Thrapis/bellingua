"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";
import {
  BookMarked,
  CloudUpload,
  Download,
  EllipsisVertical,
  FolderInput,
  Languages,
  Lock,
  Plus,
  ShieldCheck,
  SlidersHorizontal,
  Trash2,
  X,
} from "lucide-react";
import { api, type Project } from "@/lib/api";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import { CountsBar, donePct, Legend } from "@/components/pieces";
import { RunningJobs } from "@/components/jobs";
import { ExportDialog, ImportDialog, NewProjectDialog, SettingsDialog } from "@/components/project-dialogs";
import { ThemeToggle } from "@/components/theme-toggle";
import { useConfirm } from "@/components/confirm";

type DialogKind = "import" | "export" | "settings";

export default function Home() {
  const projects = useQuery({ queryKey: ["projects"], queryFn: api.projects });
  const [creating, setCreating] = useState(false);
  const [dialog, setDialog] = useState<{ kind: DialogKind; project: Project } | null>(null);

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-6 px-6 py-8">
      <header className="flex items-center gap-3">
        <Languages className="size-6" />
        <h1 className="text-xl font-semibold tracking-tight">bellingua</h1>
        <div className="flex-1" />
        <ThemeToggle />
        <Button onClick={() => setCreating(true)}>
          <Plus /> New project
        </Button>
      </header>

      <RunningJobs />

      {projects.isPending ? (
        <Skeleton className="h-32 w-full" />
      ) : projects.error ? (
        <p className="text-destructive">Cannot reach the server: {projects.error.message}</p>
      ) : projects.data.length === 0 ? (
        <div className="rounded-lg border border-dashed p-10 text-center text-muted-foreground">
          <p>No projects yet.</p>
          <p className="text-sm">Create one, then import an XLIFF or Crowdin CSV folder.</p>
        </div>
      ) : (
        <div className="grid gap-4">
          {projects.data.map((p) => (
            <ProjectCard key={p.id} project={p} onDialog={(kind) => setDialog({ kind, project: p })} />
          ))}
          <Legend />
        </div>
      )}

      <BackupCard />

      <NewProjectDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={(p) => setDialog({ kind: "import", project: p })}
      />
      {dialog?.kind === "import" && (
        <ImportDialog open onOpenChange={(o) => !o && setDialog(null)} project={dialog.project} />
      )}
      {dialog?.kind === "export" && (
        <ExportDialog open onOpenChange={(o) => !o && setDialog(null)} project={dialog.project} />
      )}
      {dialog?.kind === "settings" && (
        <SettingsDialog open onOpenChange={(o) => !o && setDialog(null)} project={dialog.project} />
      )}
    </div>
  );
}

function ProjectCard({ project: p, onDialog }: { project: Project; onDialog: (k: DialogKind) => void }) {
  const qc = useQueryClient();
  const c = p.counts;
  const untranslated = c.total - c.mt - c.translated - c.approved;
  const qa = useMutation({ mutationFn: () => api.qaRecheck(p.id), onError: (e) => toast.error(e.message) });
  const confirm = useConfirm();
  const del = useMutation({
    // Deleting ~180k strings takes a couple of seconds: show it's happening.
    mutationFn: () => {
      const t = toast.loading(`Deleting "${p.name}"…`);
      return api.deleteProject(p.id).finally(() => toast.dismiss(t));
    },
    onSuccess: () => {
      // SQLite may reuse the id for the next project: drop everything cached for it.
      qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "projects" && q.queryKey[1] === p.id });
      qc.invalidateQueries({ queryKey: ["projects"] });
      toast.success(`Project "${p.name}" deleted`);
    },
    onError: (e) => toast.error(`Could not delete: ${e.message}`),
  });
  const stat = (label: string, n: number, color?: string) => (
    <div className="flex flex-col">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="tabular-nums font-medium" style={color ? { color } : undefined}>
        {n.toLocaleString()}
      </span>
    </div>
  );
  return (
    <div className="rounded-xl border bg-card p-5">
      <div className="flex items-start gap-3">
        <div className="flex-1">
          <Link href={`/editor?project=${p.id}`} className="text-lg font-semibold hover:underline">
            {p.name}
          </Link>
          <p className="text-sm text-muted-foreground">
            {p.sourceLang} → {p.targetLang} · {p.files.toLocaleString()} files · {c.total.toLocaleString()} strings ·{" "}
            <span className="font-medium text-foreground">{donePct(c)}% human-translated</span>
          </p>
        </div>
        <Button asChild>
          <Link href={`/editor?project=${p.id}`}>Open editor</Link>
        </Button>
        <DropdownMenu modal={false}>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon" aria-label="Project actions">
              <EllipsisVertical />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onSelect={() => onDialog("import")}>
              <FolderInput /> Import…
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => onDialog("export")}>
              <Download /> Export…
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => qa.mutate()}>
              <ShieldCheck /> Recheck QA
            </DropdownMenuItem>
            <DropdownMenuItem asChild>
              <Link href={`/glossary?project=${p.id}`}>
                <BookMarked /> Glossary
              </Link>
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => onDialog("settings")}>
              <SlidersHorizontal /> Settings…
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              variant="destructive"
              disabled={del.isPending}
              onSelect={async () => {
                const ok = await confirm({
                  title: `Delete "${p.name}"?`,
                  description: `All ${c.total.toLocaleString()} strings, translations and history of this project are removed. Backups are not affected. This cannot be undone.`,
                  action: "Delete project",
                  destructive: true,
                });
                if (ok) del.mutate();
              }}
            >
              <Trash2 /> Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <ExtraLangs project={p} />
      <CountsBar counts={c} className="mt-4 h-2" />
      <div className="mt-4 grid grid-cols-3 gap-4 sm:grid-cols-6">
        {stat("Approved", c.approved, "var(--state-approved)")}
        {stat("Translated", c.translated, "var(--state-translated)")}
        {stat("Machine", c.mt, "var(--state-mt)")}
        {stat("Untranslated", untranslated)}
        {stat("QA errors", c.errors, c.errors ? "var(--destructive)" : undefined)}
        {stat("QA warnings", c.warnings)}
      </div>
    </div>
  );
}

/** The project's reference languages, with delete. */
function ExtraLangs({ project: p }: { project: Project }) {
  const qc = useQueryClient();
  const confirm = useConfirm();
  const langs = useQuery({ queryKey: ["langs", p.id], queryFn: () => api.langs(p.id) });
  const del = useMutation({
    mutationFn: (lang: string) => api.deleteLang(p.id, lang),
    onSuccess: (_, lang) => {
      qc.invalidateQueries({ queryKey: ["langs", p.id] });
      qc.invalidateQueries({ queryKey: ["unit-langs"] });
      toast.success(`${lang} removed`);
    },
    onError: (e) => toast.error(e.message),
  });
  if (!langs.data?.length) return null;
  return (
    <div className="mt-3 flex flex-wrap items-center gap-2 text-sm">
      <span className="text-xs text-muted-foreground">Reference languages:</span>
      {langs.data.map((l) => (
        <span key={l.lang} className="group inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5">
          {l.locked && <Lock className="size-3 text-muted-foreground" aria-label="locked (read-only)" />}
          <span className="font-medium">{l.lang}</span>
          <span className="text-xs tabular-nums text-muted-foreground">{l.units.toLocaleString()} strings</span>
          <button
            type="button"
            className="text-muted-foreground opacity-0 hover:text-destructive group-hover:opacity-100"
            aria-label={`Remove ${l.lang}`}
            onClick={async () => {
              if (await confirm({ title: `Remove ${l.lang}?`, description: "Its texts are deleted; import it again to get them back.", action: "Remove", destructive: true }))
                del.mutate(l.lang);
            }}
          >
            <X className="size-3.5" />
          </button>
        </span>
      ))}
    </div>
  );
}

function BackupCard() {
  const info = useQuery({ queryKey: ["info"], queryFn: api.info, refetchInterval: 60_000 });
  const run = useMutation({ mutationFn: api.backupNow, onError: (e) => toast.error(e.message) });
  const b = info.data?.backup;
  if (!b) return null;
  return (
    <div className="flex items-center gap-4 rounded-xl border p-4 text-sm">
      <CloudUpload className="size-5 text-muted-foreground" />
      <div className="flex-1">
        {b.configured ? (
          <>
            <p>
              Backups to <span className="font-medium">{b.targets?.join(", ")}</span>
              {b.interval !== "0s" && <> · every {b.interval} when there are edits</>}
            </p>
            <p className="text-muted-foreground">
              {b.last ? (
                <>
                  Last: {new Date(b.last.at).toLocaleString()} · {(b.last.size / 1048576).toFixed(1)} MB
                </>
              ) : (
                "No backup yet"
              )}
              {b.dirty && " · unsaved edits since last backup"}
              {b.lastError && <span className="text-destructive"> · {b.lastError}</span>}
            </p>
          </>
        ) : (
          <p className="text-muted-foreground">
            Backups are off. Set <code>backup.dir</code> and/or <code>backup.sftp</code> in bellingua.yaml.
          </p>
        )}
      </div>
      {b.configured && (
        <Button variant="outline" onClick={() => run.mutate()} disabled={b.running || run.isPending}>
          Back up now
        </Button>
      )}
    </div>
  );
}
