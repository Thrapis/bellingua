"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft } from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";
import { api, type Settings, type SettingsPatch } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { ThemeToggle } from "@/components/theme-toggle";

// Go prints 15m as 15m0s and 1h as 1h0m0s; show what people type.
const friendly = (d: string) => (d === "0s" ? "0" : d.replace(/(?<=[hm])0s$/, "").replace(/(?<=h)0m$/, ""));

const emptySftp = { host: "", port: 0, user: "", key: "", knownHosts: "", dir: "" };
const emptyData = { url: "", app: "", folder: "", caFile: "", timeout: "" };

function Section({ title, hint, children }: { title: string; hint?: string; children: React.ReactNode }) {
  return (
    <section className="grid gap-4 rounded-xl border p-5">
      <div>
        <h2 className="font-semibold">{title}</h2>
        {hint && <p className="text-sm text-muted-foreground">{hint}</p>}
      </div>
      {children}
    </section>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="grid gap-1.5">
      <Label>{label}</Label>
      {children}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  );
}

export function SettingsPage() {
  const q = useQuery({ queryKey: ["settings"], queryFn: api.appSettings });
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 px-6 py-8">
      <header className="flex items-center gap-3">
        <Button asChild variant="ghost" size="icon" aria-label="Back to projects">
          <Link href="/">
            <ArrowLeft />
          </Link>
        </Button>
        <h1 className="text-xl font-semibold tracking-tight">Settings</h1>
        <div className="flex-1" />
        <ThemeToggle />
      </header>
      {q.isPending ? (
        <Skeleton className="h-64 w-full" />
      ) : q.error ? (
        <p className="text-destructive">Cannot load settings: {q.error.message}</p>
      ) : (
        // Keyed by the saved data, so the form starts from what the server now holds.
        <SettingsForm key={JSON.stringify(q.data)} initial={q.data} />
      )}
    </div>
  );
}

function SettingsForm({ initial }: { initial: Settings }) {
  const qc = useQueryClient();
  const b = initial.backup;
  const projectNames = initial.projects ?? [];
  const [interval, setInterval] = useState(friendly(b.interval));
  const [keep, setKeep] = useState(String(b.keep));
  const [dir, setDir] = useState(b.dir);
  const [allProjects, setAllProjects] = useState((b.projects ?? []).length === 0);
  const [chosen, setChosen] = useState<string[]>(b.projects ?? []);
  const [sftpOn, setSftpOn] = useState(!!b.sftp);
  const [sftp, setSftp] = useState(b.sftp ?? emptySftp);
  const [dataOn, setDataOn] = useState(!!b.data);
  const [data, setData] = useState(b.data ?? { ...emptyData, tokenSet: false });
  const [crowdinBase, setCrowdinBase] = useState(initial.crowdin.baseUrl);
  const [logLevel, setLogLevel] = useState(initial.general.logLevel || "info");

  const save = useMutation({
    mutationFn: () => {
      const patch: SettingsPatch = {
        general: { logLevel },
        backup: {
          interval: interval.trim(),
          keep: Number(keep) || 0,
          dir: dir.trim(),
          projects: allProjects ? [] : chosen,
          sftp: sftpOn ? { ...sftp, port: Number(sftp.port) || 0 } : emptySftp,
          data: dataOn
            ? { url: data.url, app: data.app, folder: data.folder, caFile: data.caFile, timeout: data.timeout }
            : emptyData,
        },
        crowdin: { baseUrl: crowdinBase.trim() },
      };
      return api.saveAppSettings(patch);
    },
    onSuccess: (s) => {
      qc.setQueryData(["settings"], s);
      qc.invalidateQueries({ queryKey: ["info"] }); // the backup card on the home page
      toast.success("Settings saved", {
        description: s.restartRequired?.length ? `Applies after a restart: ${s.restartRequired.join(", ")}` : undefined,
      });
    },
    onError: (e) => toast.error(e.message),
  });

  const noneChosen = !allProjects && chosen.length === 0;
  const toggle = (name: string, on: boolean) =>
    setChosen((c) => (on ? [...c, name] : c.filter((n) => n !== name)));

  return (
    <form
      className="grid gap-6"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <p className="text-xs text-muted-foreground">
        Saved into <code className="break-all">{initial.path || "bellingua.yaml"}</code>; its comments are kept. Tokens stay in that file or in
        environment variables and are never shown here.
      </p>

      <Section title="Backup" hint="Where snapshots go and how often. Takes effect when you save.">
        <div className="grid grid-cols-2 gap-3">
          <Field label="Every" hint="e.g. 15m or 2h, only when there were edits. 0 = only when you press Back up now.">
            <Input value={interval} onChange={(e) => setInterval(e.target.value)} />
          </Field>
          <Field label="Snapshots kept per target" hint="Older ones are deleted. 0 = default (20).">
            <Input value={keep} onChange={(e) => setKeep(e.target.value)} inputMode="numeric" />
          </Field>
        </div>
        <Field label="Local folder" hint="A folder on this computer, a mounted or synced one. Empty = off.">
          <Input value={dir} onChange={(e) => setDir(e.target.value)} placeholder="D:/Backups/bellingua" />
        </Field>

        <div className="grid gap-2">
          <Label>Projects to back up</Label>
          <label className="flex items-center gap-2 text-sm">
            <Checkbox checked={allProjects} onCheckedChange={(v) => setAllProjects(v === true)} />
            All projects
          </label>
          {!allProjects && (
            <div className="grid gap-2 rounded-md border p-3">
              {projectNames.length === 0 && <p className="text-sm text-muted-foreground">No projects yet.</p>}
              {projectNames.map((n) => (
                <label key={n} className="flex items-center gap-2 text-sm">
                  <Checkbox checked={chosen.includes(n)} onCheckedChange={(v) => toggle(n, v === true)} />
                  {n}
                </label>
              ))}
              {chosen.filter((n) => !projectNames.includes(n)).map((n) => (
                <label key={n} className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Checkbox checked onCheckedChange={(v) => toggle(n, v === true)} />
                  {n} (no such project now)
                </label>
              ))}
            </div>
          )}
          <p className="text-xs text-muted-foreground">
            A snapshot then holds only the chosen projects. Restoring such a snapshot replaces the whole database, so
            the command line asks for <code>-allow-partial</code> first (the current database is kept as .bak).
          </p>
          {noneChosen && <p className="text-xs text-destructive">Choose at least one project, or back up all of them.</p>}
        </div>

        <label className="flex items-center gap-2 text-sm">
          <Checkbox checked={sftpOn} onCheckedChange={(v) => setSftpOn(v === true)} />
          SFTP server
        </label>
        {sftpOn && (
          <div className="grid gap-3 rounded-md border p-3">
            <div className="grid grid-cols-3 gap-3">
              <div className="col-span-2">
                <Field label="Host">
                  <Input value={sftp.host} onChange={(e) => setSftp({ ...sftp, host: e.target.value })} />
                </Field>
              </div>
              <Field label="Port" hint="Empty = 22">
                <Input
                  value={sftp.port ? String(sftp.port) : ""}
                  onChange={(e) => setSftp({ ...sftp, port: Number(e.target.value) || 0 })}
                  inputMode="numeric"
                />
              </Field>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <Field label="User">
                <Input value={sftp.user} onChange={(e) => setSftp({ ...sftp, user: e.target.value })} />
              </Field>
              <Field label="Remote folder">
                <Input value={sftp.dir} onChange={(e) => setSftp({ ...sftp, dir: e.target.value })} />
              </Field>
            </div>
            <Field label="Private key file" hint="Unencrypted key; ssh-agent is not used.">
              <Input value={sftp.key} onChange={(e) => setSftp({ ...sftp, key: e.target.value })} />
            </Field>
            <Field label="known_hosts file" hint="Empty = ~/.ssh/known_hosts. Connect once with ssh first.">
              <Input value={sftp.knownHosts} onChange={(e) => setSftp({ ...sftp, knownHosts: e.target.value })} />
            </Field>
          </div>
        )}

        <label className="flex items-center gap-2 text-sm">
          <Checkbox checked={dataOn} onCheckedChange={(v) => setDataOn(v === true)} />
          go-data-server
        </label>
        {dataOn && (
          <div className="grid gap-3 rounded-md border p-3">
            <div className="grid grid-cols-2 gap-3">
              <Field label="Address">
                <Input value={data.url} onChange={(e) => setData({ ...data, url: e.target.value })} placeholder="https://203.0.113.5:9696" />
              </Field>
              <Field label="App name">
                <Input value={data.app} onChange={(e) => setData({ ...data, app: e.target.value })} />
              </Field>
              <Field label="Folder" hint="Optional key prefix.">
                <Input value={data.folder} onChange={(e) => setData({ ...data, folder: e.target.value })} />
              </Field>
              <Field label="Upload timeout" hint="Empty = 10m.">
                <Input value={data.timeout === "0s" ? "" : data.timeout} onChange={(e) => setData({ ...data, timeout: e.target.value })} />
              </Field>
            </div>
            <Field label="CA certificate file" hint="Only for a self-signed certificate.">
              <Input value={data.caFile} onChange={(e) => setData({ ...data, caFile: e.target.value })} />
            </Field>
            <p className={`text-xs ${data.tokenSet ? "text-muted-foreground" : "text-destructive"}`}>
              {data.tokenSet
                ? "Token: set."
                : "Token: not set. Put it in backup.data.token of the config file or in BELLINGUA_DATA_TOKEN, then save."}
            </p>
          </div>
        )}
        {(!sftpOn || !dataOn) && (
          <p className="text-xs text-muted-foreground">
            Turning a server off removes its section from the file, including a token written there.
          </p>
        )}
      </Section>

      <Section title="Crowdin" hint="Used when copying or pulling a Crowdin project.">
        <Field label="API address" hint="Empty = crowdin.com. Crowdin Enterprise: https://{organization}.api.crowdin.com/api/v2">
          <Input value={crowdinBase} onChange={(e) => setCrowdinBase(e.target.value)} />
        </Field>
        <p className={`text-xs ${initial.crowdin.tokenSet ? "text-muted-foreground" : "text-destructive"}`}>
          {initial.crowdin.tokenSet
            ? "Token: set."
            : "Token: not set. Put it in crowdin.token of the config file or in BELLINGUA_CROWDIN_TOKEN, then restart."}
        </p>
      </Section>

      <Section title="General">
        <div className="grid grid-cols-2 gap-3">
          <Field label="Log level" hint="Applies after a restart.">
            <Select value={logLevel} onValueChange={setLogLevel}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {["debug", "info", "warn", "error"].map((l) => (
                  <SelectItem key={l} value={l}>
                    {l}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <span />
          <Field label="Listen address" hint="Decides where bellingua is reachable, so it is set in the config file (restart needed).">
            <Input value={initial.general.listen} readOnly disabled />
          </Field>
          <Field label="Database file" hint="Set in the config file or with -db (restart needed).">
            <Input value={initial.general.db} readOnly disabled />
          </Field>
        </div>
      </Section>

      <div className="flex justify-end">
        <Button type="submit" disabled={save.isPending || noneChosen}>
          {save.isPending ? "Saving…" : "Save settings"}
        </Button>
      </div>
    </form>
  );
}
