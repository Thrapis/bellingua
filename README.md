# bellingua

A local, single-binary translation workbench, like Crowdin, Weblate or Tolgee,
but it needs no Docker, no PostgreSQL and no server. You run one executable on
your laptop and edit in the browser at `http://localhost:7070`. The database
is one SQLite file, and it can be backed up to a VPS that only needs an SSH
account.

It is format-driven, not tied to a product or a toolchain: it imports and
exports **XLIFF 1.2** (markup as locked `<ph>` tags) and **Crowdin CSV**
(`id,source,translation,context`).

## Features

- **Fast at scale.** Measured on a large real project (3,677 files,
  179,947 strings) on a Ryzen 7 4700U:

  | Operation | Time |
  |---|---|
  | Import | 11 s |
  | Re-import (no changes) | 1.4 s |
  | Export | 7 s, byte-identical to the input |
  | Page of 200 strings | 0.5 ms |
  | Substring search | 9–50 ms |
  | Save | 1.7 ms |

  The UI virtualizes the list and the editor, so a 71k-character string
  edits without lag.
- **Editor**:
  - folder tree with progress
  - search, with state / QA / "source changed" filters behind one Filters
    button (active filters show as removable chips)
  - sorting: file order, recently changed, shortest or longest source first
  - markup shown as locked chips you can move but not break
  - live placeholder validation
  - drafts that survive navigation
  - edit history with restore
- **Keyboard-first**:

  | Shortcut | Action |
  |---|---|
  | `Ctrl+Enter` | Save and go to the next string |
  | `Ctrl+Shift+Enter` | Approve |
  | `Alt+↑/↓` | Previous / next string |
  | `Alt+M` | Machine translation |
  | `Alt+Shift+M` | Machine translation with glossary terms |
  | `Ctrl+Shift+C` | Copy the source |
  | `Ctrl+1…9` | Insert placeholder N |
  | `Ctrl+Shift+G` | Add a glossary term from the selection |
  | `Alt+1…5` | Use translation-memory suggestion N |
  | `Ctrl+B` | Show / hide the file tree |

- **Translation memory**, built from your own human translations
  (Translated/Approved):
  - **Duplicates**: saving a string that also appears elsewhere (other files,
    `@male`/`@female` variants) offers "Apply to N identical strings".
    "Show all" first lists them all, each with its context, because the same
    words can mean different things.
  - **Bulk fill**: the Bulk menu's "Fill 100% matches from TM" fills every
    matching string in the current filter; a whole project takes about a
    second.
  - **Fuzzy suggestions**: a Translation memory section shows up to 5 similar
    strings, from 60% up, scored by word-level edit distance. The words that
    differ are highlighted. Click one, or press `Alt+1…5`, to use it.
- **Glossary**:
  - A per-project term list with notes, alternative translations (`a | b`),
    "do not translate" and case-sensitive names.
  - Russian source terms match every noun and adjective form, via the Snowball
    stemmer: «Орбита» also finds «Орбиты» and «Орбитой».
  - Verbs whose stem changes («нажать» / «нажмите») need their own entry.
  - Terms are underlined in the source; hover shows the translation, and
    clicking inserts it.
  - Select a word and press `Ctrl+Shift+G` to add a term.
  - **Word forms**: a term can list its forms with their translations
    («Баумана» → «Баўмана»). "Find forms in the texts" lists the forms the
    project actually uses, with counts and suggested endings. Clicking a
    highlighted form and the glossary MT suggestion then insert the matching
    translation. Forms also match where the stemmer alone would miss them.
  - The Glossary page supports search, editing, and CSV import/export
    (`source,target,note,dnt,case_sensitive,forms`; forms as
    `Баумана=Баўмана; Бауману=Баўману`).
- **QA checks**, run on every save and import:
  - placeholder mismatch
  - empty translation
  - leading/trailing whitespace
  - trailing punctuation
  - numbers
  - double spaces
  - unbalanced brackets and quotes
  - translation identical to the source
  - forbidden letters (letters the target alphabet lacks; a default is set
    for some target languages)
  - length ratio
  - glossary terms not translated as agreed. The check tolerates target
    inflection, and the whole project is rechecked automatically in the
    background after glossary edits.
- **Machine translation on demand**, fully offline, through the bundled
  Lingvanex CTranslate2 server (`third_party/lingvanex-server`):
  - `bellingua serve` starts it in the background and stops it on exit, even
    after a crash. The editor is usable while it loads.
  - If a server is already running on the port, it is reused.
  - Markup is masked as `§N§` sentinels. If they come back broken, bellingua
    translates the text fragment by fragment instead.
  - When the source contains glossary terms, a second suggestion hides them
    from the model and inserts the agreed translations (in dictionary form, so
    check the endings). `Alt+Shift+M` applies it.
  - While the Machine translation section is open, each string is translated
    as soon as you stop on it; collapse the section to turn that off.
  - Batch MT runs over any filter as a background job.
- **Re-import merge.** A new source update or MT run never overwrites human work:
  - Changed sources are flagged for review.
  - Strings that disappeared become obsolete; they are hidden, not deleted.
- **Other languages as reference** (e.g. existing official translations into
  other languages):
  - Shown read-only in the "Other languages" section, which loads only while
    it is open. Click a text to copy it into the editor.
  - A text made from a different source is marked outdated.
  - Import one with the Import dialog ("Other language") or
    `bellingua import -project my-app -in xliff-en -extra`. Remove it from
    the project card.
  - Expected input: an XLIFF tree laid out exactly like the main import, with
    the same paths, the same ids and the same `<source>`. The language's text
    goes in `<target>`, and `<file target-language="en">` names the language.
- **Backups**:
  - `VACUUM INTO` snapshot, zstd, then upload to a local folder and/or SFTP.
  - Written under a temp name and renamed; the newest N are kept.
  - Automatic every 15 minutes when there were edits, and on exit.
  - About 18 MB for a 180k-string project, because the search index is
    rebuilt on restore instead of shipped.

## Quick start

Download or build `bellingua.exe` and double-click it. The editor opens in
your browser, and the console window shows the log; close it to stop
bellingua. Double-clicking again while it runs just opens the editor again.
Started this way, bellingua keeps `bellingua.db` (and reads `bellingua.yaml`)
in the exe's folder. Import your files from the project menu.

From a terminal, the same works with explicit commands:

```powershell
bellingua import -project my-app -in "C:\path\to\xliff"
bellingua serve -open
```

A typical round trip, with your own tools extracting the strings to XLIFF and
putting the translations back:

```powershell
bellingua import -project my-app -in xliff               # once; re-run after a source update or MT pass
bellingua serve -open                                    # translate, review, approve
bellingua export -project my-app -out xliff-out          # -min-state translated|approved to filter
```

Import and export are also available from the web UI (project menu). Paths are
paths on the machine running bellingua.

## Configuration

Copy `bellingua.example.yaml` to `bellingua.yaml` (read from the working
directory, or pass `-config`). It sets the DB path, the listen address, the MT
providers and the backup targets. Every command also accepts `-db`.

### Backup to a VPS

1. Create an SSH user on the VPS and authorize an **unencrypted** key for it.
   ssh-agent is not used.
2. Run `ssh user@vps` once, so the host key lands in `~/.ssh/known_hosts`.
3. Set `backup.sftp` in `bellingua.yaml`. Nothing needs to be installed on the
   VPS.

Useful commands:

```powershell
bellingua backup                 # snapshot now
bellingua backup -list           # list snapshots on every target
bellingua restore                # latest snapshot from SFTP (or -target dir); stop the server first
bellingua restore -from bellingua-20260925-204019.db.zst
```

`restore` checks the snapshot's integrity, rebuilds the search index, and
keeps the previous database as `bellingua.db.bak`.

## Commands

| Command | |
|---|---|
| *(no arguments)* | `serve -open` with the data in the exe's folder (a double-click) |
| `serve [-listen addr] [-open]` | web editor + API; if bellingua already runs there, `-open` just opens it |
| `import -project P -in DIR [-format xliff\|crowdin-csv] [-source-lang en -target-lang de]` | import or re-import a tree; creates the project if needed (languages default to the XLIFF header) |
| `export -project P -out DIR [-format …] [-min-state mt\|translated\|approved]` | export a tree mirroring the import |
| `qa -project P` | recompute QA for all units (after changing checks) |
| `backup [-list]`, `restore [-from NAME] [-target dir\|sftp]` | see above |
| `projects` | list projects with progress |

## Machine translation setup

The translation server is Python and needs two packages. Its models (about
310 MB) are not in git, so copy them into `third_party/lingvanex-server/`
(one `<src>_<tgt>/1/…` folder per language pair, e.g. `en_de/1/…`):

```powershell
py -m pip install -r third_party/lingvanex-server/requirements.txt
```

That's all: `serve` runs it. Its state is shown in the Machine translation
section, and `GET /api/info` returns it as `mtServer`. Details and tuning are
in `third_party/lingvanex-server/README.md`.

## Building

You need Go 1.25+ and Node 20+.

```powershell
scripts\build.ps1        # or scripts/build.sh — npm build, copy to internal/ui/dist, go build
```

For development, run the API and the Next dev server side by side. The dev
server proxies `/api` to the Go server.

```powershell
go run ./cmd/bellingua serve          # :7070
cd web; npm run dev                   # :3000
```

Test with:

```powershell
go test ./...
```

For benchmarks against a real DB, set `BELLINGUA_BENCH_DB=path\to.db` and run:

```powershell
go test -bench . ./internal/store
```
