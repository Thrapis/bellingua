# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A local translation workbench (a Crowdin/Weblate replacement for one person). A
single Go binary serves a JSON API and an embedded, statically exported Next.js
UI over one SQLite database. It exchanges data with external tools only
through files: XLIFF 1.2 and Crowdin CSV. The offline MT server is bundled in
`third_party/lingvanex-server`.

**Runtime performance is the main non-functional requirement.** Every list,
search and save must stay instant on ~180k units and on strings up to 71k
chars. Check changes to hot paths against the benchmarks.

## Commands

```bash
go build ./... && go vet ./... && go test ./...
gofmt -l .                                   # must print nothing
BELLINGUA_BENCH_DB=path/to.db go test -run x -bench . ./internal/store   # real-corpus benchmarks

scripts/build.ps1   # (or build.sh) npm build in web/ -> copy web/out to internal/ui/dist -> go build bellingua.exe
cd web && npx tsc --noEmit && npx eslint .   # frontend checks
cd web && npm run dev                        # :3000, proxies /api to 127.0.0.1:7070 (BELLINGUA_API overrides)
```

The Windows exe icon comes from `cmd/bellingua/rsrc_windows_*.syso`, which
`go build` links automatically. They are generated from
`cmd/bellingua/winres/` (small design at 16–48 px, detailed one from 64 px)
and committed. They don't update themselves: regenerate them after changing
any icon PNG, or the exe keeps the old icon:
`cd cmd/bellingua && go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --out rsrc --arch amd64,arm64`.

`web/go.mod` exists only so `./...` skips `web/node_modules` (one npm package
ships Go files). Don't remove it. `internal/ui/dist` holds the embedded build
output and is gitignored except `.gitkeep`. Without a UI build, the server runs
API-only and `/` explains how to build.

## Architecture

### Store (`internal/store`)

- **Connections.** `W` is a single writer connection with `_txlock=immediate`.
  `R` is a read-only pool of NumCPU connections. WAL mode means readers never
  wait on writes. All writes go through `Store.Write`, which bumps an in-memory
  `Rev`. `Rev` backs the HTTP ETags on projects, tree and count, so any write
  invalidates them.
- **Schema** (`schema.sql`, migration 1).
  - `units.source` and `units.target` are plain strings. They are used for FTS,
    search and export.
  - `*_pieces` is a compact varint blob with the code/text split
    (`model.EncodePieces`). It is NULL when a string has no codes.
  - `target IS NULL` means untranslated.
  - `obsolete=1` units are hidden everywhere and never deleted.
- **Triggers** keep two things in sync with `units`:
  - the denormalised per-file counters in `files` (`total`, `n_mt`,
    `n_translated`, `n_approved`, `n_err`, `n_warn`)
  - the FTS5 `trigram` index `units_fts` (external content, source + target)

  `BeginBulk`/`EndBulk` drop the triggers and rebuild FTS, counters and
  `ANALYZE` in one pass. Only a *fresh* import uses bulk mode; a re-import
  keeps the triggers, because few rows change.
- **List queries**:
  - Keyset paging, never OFFSET. `store.Sort` picks the order:
    - file order uses `(project_id, file_id, ord)`
    - `updated`, `short` and `long` use the partial indexes `units_updated`
      and `units_len` (migration 3)
  - `Cursor{A, B}` is opaque to clients. For expression orders the cursor
    condition is spelled `a >= x AND (a > x OR b > y)`, because SQLite
    ignores a row-value range on an expression index.
  - `Filter.where` must spell `state < 2` and `qa <> 0 AND obsolete = 0`
    exactly, or the planner ignores the covering partial indexes `units_todo`
    and `units_qa`. Check `EXPLAIN QUERY PLAN` if you touch it.
  - `Count` without a query/QA/changed filter is answered from the `files`
    counters.
- **Search**: queries of 3+ characters use FTS trigram (Unicode
  case-insensitive, so Cyrillic works). Shorter ones use a case-sensitive
  `instr` scan. Key and context search always use `instr`.
- **List rows** carry previews cut to `PreviewRunes`; full texts come from
  `GET /api/units/{id}`.

### Codes / placeholders

A string is a sequence of `model.Piece`s, each either text or a code (markup
that must survive unchanged). The editor and API exchange **plain strings**;
the pieces are recovered with `model.SplitTarget`:

- Source codes are located in the target, longest first, each used as often as
  it occurs in the source.
- A mismatch returns `*model.CodeMismatch`. The API answers it with 422, unless
  `force` is set.
- `web/lib/pieces.ts#splitTarget` is a line-for-line port used for instant
  client-side validation. Keep the two in step.
- The CodeMirror chip decorations (`components/target-editor.tsx#buildChips`)
  use the same matching rules.

### Import / export

- **Import** (`internal/importer`): NumCPU parser goroutines feed one writer
  inside one transaction. QA masks are computed in the parsers. Merge rules
  are documented at the top of `importer.go`; never let MT overwrite state
  `translated`/`approved`.
- **XLIFF** (`internal/formats/xliff`): export must stay **byte-identical**
  to the imported files for unchanged data. The real-corpus check: import an
  XLIFF tree, export it, `diff -r` shows 0 differences.
- **Extra languages** (`importer/langs.go`, `store/langs.go`, `api/langs.go`,
  migration 6) are reference texts (e.g. en) next to the main target.
  - **Input contract**: an XLIFF tree laid out exactly like the main import.
    It has the same paths (locale folders included), the same `trans-unit`
    ids, the same `<source>` and the language in `<target>`.
    `<file target-language>` names it. Producing it is the upstream tool's
    job; bellingua never maps locale folders.
  - **Storage**:
    - `project_langs` has a `locked` column (always 1 for now; it becomes
      the switch once extra languages are editable).
    - `unit_langs` is `WITHOUT ROWID`, keyed `(unit_id, lang)`, with no FTS or
      counters, so none of the hot paths change.
    - `unit_langs.source_hash` vs `units.source_hash` flags a text made from
      an older source as outdated.
  - **Import**:
    - It matches by (path, key) and never creates units.
    - A re-import replaces the language.
    - It refuses the project's own languages and mixed trees.
    - About 8 s for 180k texts (3 s on re-import).
  - **UI**: `GET /api/units/{id}/langs` is separate from the unit GET. The
    "Other languages" section queries it only while open (`usePanelOpen`),
    and the section is hidden when the project has no extra language.

### QA (`internal/qa`)

- Each check is one bit in `units.qa`. `qa.Errors` (placeholders, empty)
  drives `n_err`; the other bits drive `n_warn`.
- Details are recomputed on demand (`Runner.Run`) and not stored.
- Per-project config lives in `projects.settings`. The API caches one `Runner`
  per settings value.

### Glossary (`internal/glossary`, `store/terms.go`, `api/terms.go`)

- **Storage**: terms live in `terms` (migration 2). Every change bumps
  `projects.gloss_rev`; the API caches one compiled `glossary.Glossary` +
  `qa.Runner` per project, keyed by QA settings and `gloss_rev`.
- **Source matching**: Russian source words are Snowball-stemmed
  (`kljensen/snowball`, stems memoised process-wide). Other languages compare
  lower-cased words.
  - Tokens come only from text pieces, so markup never matches.
  - Multi-word terms match consecutive words; the longest term wins.
  - Match offsets are **UTF-16**, because the UI slices JS strings with them.
- **Target check**: target words are not stemmed, so a target word counts as
  present when some translation word starts with `prefixOf(word)` (drops up to
  2 letters, keeps ≥ 4). `ё`/`е` and `ў`/`у` are folded.
- **Word forms** (`Term.Forms`, `terms.forms` JSON, migration 5): source form
  → translation pairs; `target` stays the base form.
  - Snowball does not always give a name's forms one stem («Бауман»,
    «Баумана» → `баума`, «Бауману» → `бауман`), so every form is also a match
    entry of its term.
  - `targetFor` picks the text to insert for an occurrence: DNT → the
    occurrence, else the matching form (`normPhrase` lookup), else the first
    alternative, always with the occurrence's capitalisation (`matchCase`).
    `Match.Insert` / the unit's `terms[].inserts` carry it to the UI.
  - `QA` accepts form translations too.
  - `GET /api/projects/{pid}/terms/forms?source=` (`store.TermForms`) lists
    the forms found in the project's sources: FTS on `SearchKey`, then
    `FormCounter`, which matches loosely (a prefix, at most 3 letters longer)
    because the user reviews the result.
  - CSV column `forms`: `Баумана=Баўмана; Бауману=Баўману`.
- **MT with terms** (`glossary/pin.go`): `POST /api/units/{id}/mt` also
  returns `glossary`, a second translation run in parallel where `Pin` turned
  each term occurrence into a code piece (a private-use token), so the MT
  masking hides it from the model; `Unpin` splits those codes back into the
  agreed text from `targetFor`. Matches crossing markup are not pinned.
  Batch MT stays unpinned.
- **QA**: the check is bit `qa.Terms` (warning), and new bits go last. Term
  edits trigger a debounced (2 s) project QA recheck job.
- **`RecheckQA` performance**: masks are computed on all cores. More than 2000
  changed masks are written in one transaction with the counter trigger
  dropped, then the files are recounted.

### Translation memory (`internal/tm`, `store/tm.go`, `api/tm.go`)

- **Scope**: only human translations (state ≥ translated) are TM.
- **`same=<unitId>`** is a list filter for every string with that unit's
  source, used by "Show all" next to "Apply to N". It also relies on the
  hash index.
- **Exact matches, duplicates and bulk fill** use `units.source_hash`
  (FNV-64a of the plain source; migration 4 backfills it in Go through a
  `migration.post` hook). They always compare `source` too, so a hash
  collision is harmless.
- **Keep the hash current**: anything that writes `units.source` (today only
  the importer) must also write `source_hash = store.SourceHash(...)`.
- **Fuzzy matches** come from `tm.Manager`, a per-project in-memory inverted
  word index:
  - built lazily from `store.HumanUnits` (about 1.3 s for 180k units)
  - updated in place by `saveUnit` / `propagate`
  - `Invalidate`d after bulk state changes, imports, TM fill and project
    deletion
  - postings may go stale; `Search` re-checks them against current words
  - score = word-level Levenshtein, 60–99% (100 is reserved for an identical
    plain source)
- **Writes**: TM writes go through `SaveUnit` with `Origin: "tm"` and
  `IfBelow: Translated`, so they never overwrite human work.

### MT (`internal/mt`)

- `Chain` tries providers in order.
- Before translating, a string is split at codes containing line breaks.
  Inner code runs become `§N§`, and leading/trailing codes are never sent. If
  the sentinels come back broken, each fragment is translated separately.
- **Lingvanex server**: `third_party/lingvanex-server` is a local CTranslate2
  server.
  - Only its scripts are in git; the models are gitignored.
  - Its decoding params (`BEAM_SIZE`, `LENGTH_PENALTY`, `COMPUTE_TYPE`,
    `MAX_DECODING_LENGTH`) are the user's to lock; never change them. Only
    `INTER_THREADS` / `INTRA_THREADS` may be tuned.
  - It reads `LINGVANEX_HOST` / `LINGVANEX_PORT` from the environment.
- **Supervisor** (`internal/mtserver`): `serve` runs the server.
  - `Start` does not block. `Status` is starting / ready / external / failed
    / off, and is exposed as `/api/info` `mtServer`.
  - MT endpoints answer 503 with a readable reason while it isn't ready.
  - One goroutine owns `cmd.Wait`, and a crash's last stderr lines become the
    status message.
  - On Windows a job object with `KILL_ON_JOB_CLOSE`, and on Linux
    `Pdeathsig`, kill the server if bellingua dies. `Stop` uses `taskkill /T`
    or a process-group SIGTERM.
  - Tests run the test binary itself as a fake server (`FAKE_SERVER`).
- Batch jobs save with `Save.IfBelow`, so a unit a human edited meanwhile is
  skipped.

### Backup (`internal/backup`)

- Snapshot steps:
  1. `VACUUM INTO` a temp file on a separate connection, which doesn't block
     the editor.
  2. Empty the FTS index in the copy (it is half the DB) and set
     `meta.fts_stripped`.
  3. Compress with zstd.
  4. Upload each target under a `.part` name, then rename.
- Restore rebuilds FTS when that flag is set.
- Targets: `LocalDir` and `SFTP`. SFTP uses key auth and `known_hosts` only.
  The test server is an in-process SSH server with `sftp.InMemHandler`.
- `Manager.Loop` backs up on an interval when `Rev` changed, and once more on
  shutdown.

### API (`internal/api`)

- stdlib `ServeMux` patterns, with gzip via `gzhttp` (level 1; SSE excluded).
- Long operations (import, export, MT batch, QA recheck, backup) are `Jobs`.
  The UI follows them over `GET /api/jobs/events` (SSE of the whole job list,
  sent on change).
- `spa()` serves the export:
  - `/editor` resolves to `editor.html`, and unknown paths fall back to
    `index.html`.
  - `_next/static` is served as immutable.

### Frontend (`web/`)

- **Setup**: Next.js 16 app router, React Compiler, shadcn/ui on **Radix**
  (`components.json` style `radix-nova`), TanStack Query and Virtual,
  CodeMirror 6. Read `web/AGENTS.md`: this Next version differs from older
  ones, and its docs are in `node_modules/next/dist/docs/`.
- **Build modes**: `output: "export"` in production only. Rewrites (the dev
  proxy) are incompatible with export, so they are dev-only (`next.config.ts`).
- **URL state**: the editor keeps its state in the URL and writes it with
  `window.history.replaceState` (Next syncs `useSearchParams`, with no
  navigation round trip). `useSearchParams` needs the `<Suspense>` in
  `app/editor/page.tsx`.
- **Saving**:
  - Saves are optimistic: they patch the infinite-query cache, move to the
    next unit, then call `api.saveUnit` directly.
  - Don't switch to `useMutation().mutate` callbacks; they only fire for the
    latest of several quick saves.
  - The units list is `staleTime: Infinity` and is patched, not refetched,
    after saves.
- **Unit panel sections** (TM, MT, Glossary, QA, History) are a Radix
  accordion (`components/panel-sections.tsx`); open sections persist in
  `localStorage` (`usePanelOpen`, owned by `UnitPanel`). While the MT section
  is open it translates each unit shown for 300 ms (a debounce, so Alt+↓
  through strings doesn't queue model runs). `components/ui/accordion.tsx` is edited on purpose: the
  generated inner `h-(--radix-accordion-content-height)` was removed, because
  it froze the height measured at open and clipped content that loads later
  (TM, MT). Keep it removed if you re-add the component.
- **Editor**: one CodeMirror instance for the page's lifetime. `load()` swaps
  in a new `EditorState` per unit, which resets undo but doesn't remount.
  Handlers are read through refs.
