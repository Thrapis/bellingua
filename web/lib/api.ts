// Typed client for the Go API (/api). Shapes mirror internal/store and
// internal/api; keep them in step.

export type Piece = { t: string; c?: boolean };
export type State = "untranslated" | "mt" | "translated" | "approved";
export const STATES: State[] = ["untranslated", "mt", "translated", "approved"];

export type Counts = {
  total: number;
  mt: number;
  translated: number;
  approved: number;
  errors: number;
  warnings: number;
};

export type QAConfig = { forbidden: string; maxLengthRatio: number; disabled: string[] | null };

export type Project = {
  id: number;
  name: string;
  sourceLang: string;
  targetLang: string;
  settings: { qa: QAConfig };
  counts: Counts;
  files: number;
};

export type TreeNode = { name: string; path: string; file?: number; counts: Counts };

export type Row = {
  id: number;
  file: number;
  ord: number;
  key: string;
  src: Piece[];
  tgt: Piece[] | null;
  srcLen: number;
  tgtLen: number;
  trunc?: boolean;
  state: State;
  qa: number;
  changed?: boolean;
  updatedAt: number;
  context?: string;
};

/** Opaque keyset position returned by the server. */
export type Cursor = { a: number; b: number };

/** List orders; every one is index-backed on the server. */
export type Sort = "file" | "updated" | "short" | "long";
export const SORTS: { id: Sort; label: string }[] = [
  { id: "file", label: "File order" },
  { id: "updated", label: "Recently changed" },
  { id: "short", label: "Shortest source first" },
  { id: "long", label: "Longest source first" },
];
export type UnitsPage = { rows: Row[]; next: Cursor | null };

export type Issue = { check: string; severity: "error" | "warning"; message: string };

export type Term = {
  id: number;
  source: string;
  target: string; // alternatives separated by "|"
  note: string;
  dnt: boolean; // do not translate
  caseSensitive: boolean;
  /** Inflected source forms with their translations; `target` is the base form. */
  forms: TermForm[];
  updatedAt: number;
};
export type TermForm = { src: string; tgt: string };
export type TermInput = Omit<Term, "id" | "updatedAt">;
/**
 * A glossary term found in a unit's source; spans are UTF-16 offsets into the
 * plain source, and inserts[i] is the translation for the form at spans[i].
 */
export type TermMatch = Term & { spans: [number, number][]; inserts: string[] };

export const termTargets = (t: Pick<Term, "target">) =>
  t.target
    .split("|")
    .map((s) => s.trim())
    .filter(Boolean);
export type HistoryEntry = { target: string | null; state: State; origin: string; at: number };

export type Unit = {
  id: number;
  project: number;
  file: number;
  path: string;
  ord: number;
  key: string;
  context: string;
  src: Piece[];
  tgt: Piece[] | null;
  state: State;
  qa: number;
  changed: boolean;
  updatedAt: number;
  history?: HistoryEntry[];
  issues: Issue[];
  terms: TermMatch[];
  /** Set by a save: identical strings still without a human translation. */
  duplicates?: number;
};

/** A translation-memory match; score 100 = identical source. */
export type TMSuggestion = {
  unit: number;
  path: string;
  src: Piece[];
  tgt: Piece[];
  state: State;
  updatedAt: number;
  score: number;
};
export type TMResult = { matches: TMSuggestion[]; duplicates: number };

export type QACheck = { bit: number; id: string; title: string; severity: "error" | "warning" };

export type BackupStatus = {
  configured: boolean;
  targets: string[] | null;
  running: boolean;
  dirty: boolean;
  last?: { name: string; size: number; at: string; duration: number; errors?: Record<string, string> };
  lastError?: string;
  interval: string;
};

export type Info = {
  mtProviders: string[] | null;
  qaChecks: QACheck[];
  states: State[];
  previewRunes: number;
  backup: BackupStatus;
  /** The bundled Lingvanex server, when bellingua supervises one. */
  mtServer?: MTServerStatus | null;
};

export type Job = {
  id: string;
  kind: "import" | "export" | "mt" | "qa" | "backup" | "tm";
  project?: number;
  status: "running" | "done" | "failed" | "canceled";
  done: number;
  total: number;
  message?: string;
  error?: string;
  result?: Record<string, unknown>;
  started: string;
  finished?: string;
};

export type MTResult = {
  tgt: Piece[];
  provider: string;
  /** The same translation with glossary terms pinned; only when the source has terms. */
  glossary?: Piece[];
};

/** An extra (reference) language of a project. */
export type ProjectLang = { lang: string; locked: boolean; units: number; importedAt: number };
/** A unit's text in an extra language; outdated = made from a different source. */
export type UnitLang = { lang: string; locked: boolean; tgt: Piece[]; state: State; outdated: boolean };

export type MTServerStatus = {
  state: "off" | "starting" | "ready" | "external" | "failed";
  message?: string;
  addr: string;
};

/** Filter shared by the unit list, count and bulk/MT endpoints. */
export type Filter = {
  file?: number;
  dir?: string;
  q?: string;
  in?: "both" | "source" | "target" | "key" | "context";
  states?: State[];
  qa?: string; // check ids, comma separated, or "any"
  errors?: boolean;
  changed?: boolean;
  /** Strings whose source is identical to this unit's (itself included). */
  same?: number;
};

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    public data?: unknown,
  ) {
    super(message);
  }
}

async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const res = await fetch(`/api${path}`, {
    method,
    signal,
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  const json = text ? JSON.parse(text) : undefined;
  if (!res.ok) throw new ApiError(res.status, json?.error ?? res.statusText, json?.data);
  return json as T;
}

export function filterParams(f: Filter): URLSearchParams {
  const p = new URLSearchParams();
  if (f.file) p.set("file", String(f.file));
  else if (f.dir) p.set("dir", f.dir);
  if (f.q) p.set("q", f.q);
  if (f.q && f.in && f.in !== "both") p.set("in", f.in);
  if (f.states?.length) p.set("state", f.states.join(","));
  if (f.qa) p.set("qa", f.qa);
  if (f.errors) p.set("errors", "1");
  if (f.changed) p.set("changed", "1");
  if (f.same) p.set("same", String(f.same));
  return p;
}

export const api = {
  info: () => request<Info>("GET", "/info"),
  projects: () => request<Project[]>("GET", "/projects"),
  project: (id: number) => request<Project>("GET", `/projects/${id}`),
  createProject: (b: { name: string; sourceLang: string; targetLang: string }) =>
    request<Project>("POST", "/projects", b),
  deleteProject: (id: number) => request<void>("DELETE", `/projects/${id}`),
  saveSettings: (id: number, settings: Project["settings"]) =>
    request<Project>("PUT", `/projects/${id}/settings`, settings),
  tree: (id: number, dir: string) =>
    request<{ dir: string; children: TreeNode[] }>("GET", `/projects/${id}/tree?dir=${encodeURIComponent(dir)}`),
  units: (id: number, f: Filter, sort: Sort, after: Cursor | null, limit: number, signal?: AbortSignal) => {
    const p = filterParams(f);
    p.set("limit", String(limit));
    if (sort !== "file") p.set("sort", sort);
    if (after) p.set("after", `${after.a}.${after.b}`);
    return request<UnitsPage>("GET", `/projects/${id}/units?${p}`, undefined, signal);
  },
  count: (id: number, f: Filter, signal?: AbortSignal) =>
    request<{ count: number }>("GET", `/projects/${id}/count?${filterParams(f)}`, undefined, signal),
  unit: (id: number) => request<Unit>("GET", `/units/${id}`),
  saveUnit: (id: number, b: { target: string | null; state: State; force?: boolean }) =>
    request<Unit>("PUT", `/units/${id}`, b),
  unitMT: (id: number, signal?: AbortSignal) => request<MTResult>("POST", `/units/${id}/mt`, undefined, signal),
  unitTM: (id: number, signal?: AbortSignal) => request<TMResult>("GET", `/units/${id}/tm`, undefined, signal),
  propagate: (id: number) => request<{ changed: number }>("POST", `/units/${id}/propagate`),
  tmFill: (id: number, f: Filter) => request<Job>("POST", `/projects/${id}/tm/fill?${filterParams(f)}`),
  bulk: (id: number, f: Filter, b: { state: State; noErrors: boolean }) =>
    request<{ changed: number }>("POST", `/projects/${id}/bulk?${filterParams(f)}`, b),
  mtBatch: (id: number, f: Filter, v: { overwrite: boolean; glossary: boolean }) =>
    request<Job>("POST", `/projects/${id}/mt?${filterParams(f)}`, v),
  qaRecheck: (id: number) => request<Job>("POST", `/projects/${id}/qa`),
  importDir: (id: number, b: { dir: string; format: string }) =>
    request<Job>("POST", `/projects/${id}/import`, b),
  exportDir: (id: number, b: { dir: string; format: string; minState: State }) =>
    request<Job>("POST", `/projects/${id}/export`, b),
  terms: (id: number) => request<Term[]>("GET", `/projects/${id}/terms`),
  createTerm: (id: number, t: TermInput) => request<Term>("POST", `/projects/${id}/terms`, t),
  updateTerm: (termId: number, t: TermInput) => request<Term>("PUT", `/terms/${termId}`, t),
  deleteTerm: (termId: number) => request<void>("DELETE", `/terms/${termId}`),
  importTerms: async (id: number, csv: string) => {
    const res = await fetch(`/api/projects/${id}/terms/import`, {
      method: "POST",
      headers: { "Content-Type": "text/csv" },
      body: csv,
    });
    const json = await res.json();
    if (!res.ok) throw new ApiError(res.status, json?.error ?? res.statusText);
    return json as { inserted: number; updated: number };
  },
  exportTermsUrl: (id: number) => `/api/projects/${id}/terms/export`,
  langs: (id: number) => request<ProjectLang[]>("GET", `/projects/${id}/langs`),
  importLang: (id: number, b: { dir: string; lang: string }) => request<Job>("POST", `/projects/${id}/langs/import`, b),
  deleteLang: (id: number, lang: string) => request<void>("DELETE", `/projects/${id}/langs/${encodeURIComponent(lang)}`),
  unitLangs: (id: number, signal?: AbortSignal) => request<UnitLang[]>("GET", `/units/${id}/langs`, undefined, signal),
  termForms: (id: number, source: string) =>
    request<{ form: string; count: number }[]>("GET", `/projects/${id}/terms/forms?source=${encodeURIComponent(source)}`),
  jobs: () => request<Job[]>("GET", "/jobs"),
  cancelJob: (id: string) => request<void>("DELETE", `/jobs/${id}`),
  backup: () => request<BackupStatus>("GET", "/backup"),
  backupNow: () => request<Job>("POST", "/backup"),
};
