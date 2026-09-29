"use client";

import { useQueryClient } from "@tanstack/react-query";
import { createContext, useContext, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { LoaderCircle, X } from "lucide-react";
import { api, type Job } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";

const JobsContext = createContext<Job[]>([]);

export const useJobs = () => useContext(JobsContext);

const kindLabel: Record<Job["kind"], string> = {
  import: "Import",
  export: "Export",
  mt: "Machine translation",
  qa: "QA recheck",
  backup: "Backup",
  tm: "Translation memory fill",
};

function summary(j: Job): string {
  const r = j.result ?? {};
  switch (j.kind) {
    case "import":
      if (r.lang)
        return `${r.lang}: ${r.stored ?? 0} texts · ${r.outdated ?? 0} outdated · ${r.unknownUnits ?? 0} unknown strings · ${r.unknownFiles ?? 0} unknown files`;
      return `${r.Units ?? 0} units · ${r.Inserted ?? 0} new · ${r.Updated ?? 0} updated · ${r.SourceChanged ?? 0} source changed · ${r.Obsolete ?? 0} obsolete`;
    case "export":
      return `${r.Files ?? 0} files · ${r.Translated ?? 0} translated units`;
    case "mt":
      return `${r.translated ?? 0} translated · ${r.failed ?? 0} failed${r.firstError ? ` (${r.firstError})` : ""}`;
    case "qa":
      return `${r.changed ?? 0} units changed`;
    case "backup":
      return `${r.name ?? ""}`;
    case "tm":
      return `${r.filled ?? 0} strings filled from 100% matches`;
  }
}

/**
 * Follows /api/jobs/events (SSE) and exposes the job list. When a job
 * finishes it toasts the outcome and invalidates the data it touched.
 */
export function JobsProvider({ children }: { children: React.ReactNode }) {
  const [jobs, setJobs] = useState<Job[]>([]);
  const qc = useQueryClient();
  const seen = useRef(new Map<string, Job["status"]>());

  useEffect(() => {
    let es: EventSource | null = null;
    let retry: ReturnType<typeof setTimeout> | undefined;
    const connect = () => {
      es = new EventSource("/api/jobs/events");
      es.onmessage = (ev) => {
        const list: Job[] = JSON.parse(ev.data);
        setJobs(list);
        for (const j of list) {
          const prev = seen.current.get(j.id);
          seen.current.set(j.id, j.status);
          if (prev === "running" && j.status !== "running") {
            if (j.status === "done") toast.success(`${kindLabel[j.kind]} finished`, { description: summary(j) });
            else toast.error(`${kindLabel[j.kind]} ${j.status}`, { description: j.error });
            if (j.kind !== "export" && j.kind !== "backup") {
              qc.invalidateQueries({ predicate: (q) => q.queryKey[0] !== "info" });
            }
            if (j.kind === "backup") qc.invalidateQueries({ queryKey: ["info"] });
          }
        }
      };
      es.onerror = () => {
        es?.close();
        retry = setTimeout(connect, 2000);
      };
    };
    connect();
    return () => {
      es?.close();
      clearTimeout(retry);
    };
  }, [qc]);

  return <JobsContext.Provider value={jobs}>{children}</JobsContext.Provider>;
}

/** Compact list of running jobs with progress and a cancel button. */
export function RunningJobs({ project }: { project?: number }) {
  const jobs = useJobs().filter((j) => j.status === "running" && (project === undefined || !j.project || j.project === project));
  if (!jobs.length) return null;
  return (
    <div className="flex flex-col gap-2">
      {jobs.map((j) => {
        const pct = j.total ? Math.round((j.done / j.total) * 100) : 0;
        return (
          <div key={j.id} className="flex items-center gap-3 rounded-md border px-3 py-2 text-sm">
            <LoaderCircle className="size-4 animate-spin text-muted-foreground" />
            <span className="w-40 shrink-0 font-medium">{kindLabel[j.kind]}</span>
            <Progress value={pct} className="h-1.5 flex-1" />
            <span className="w-36 shrink-0 text-right tabular-nums text-muted-foreground">
              {j.total ? `${j.done.toLocaleString()} / ${j.total.toLocaleString()}` : (j.message ?? "…")}
            </span>
            <Button variant="ghost" size="icon" className="size-6" onClick={() => api.cancelJob(j.id)} title="Cancel">
              <X className="size-3.5" />
            </Button>
          </div>
        );
      })}
    </div>
  );
}
