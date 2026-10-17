package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Thrapis/bellingua/internal/glossary"
	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/qa"
	"github.com/Thrapis/bellingua/internal/store"
)

// The MCP server at /mcp lets an agent read the corpus, translate, review and
// edit the glossary. It reuses the handlers' cores, so its writes behave like
// the editor's (TM index, QA masks, Rev), except that a translation it saves
// never overwrites an approved one.

const mcpInstructions = `bellingua is a translation workbench. A project has files of units (strings); each unit has a source, a target and a state.

States: untranslated (no target), mt (machine translation, unreviewed), translated (written by a human), approved (reviewed, final).

Codes: some sources contain markup or placeholders ("codes", listed per unit in "codes"). A target must contain each code exactly as often as the source does, byte for byte. Texts are plain strings with the codes inline.

Workflow for translating: find units with list_units (states ["untranslated","mt"]), read each with get_unit (full texts; list rows are cut to a preview), consult get_unit_tm, the glossary terms in get_unit (use each term's "inserts" verbatim) and machine_translate, then save_translation.

For many units at once: list_units with langs (e.g. ["uk","en"]) puts those extra-language texts on every row as refs, so no get_unit_langs call per line; save_translations saves up to 200 units per call, and an item without target keeps the current text and just marks it translated (a reviewed good machine translation). Each item succeeds or fails on its own.

Terminology sweeps: when a term is decided and the old form (with its inflections) sits in many units, do not retype them; use replace_in_targets with pairs per form. It is a dry run unless you pass dryRun=false: run the dry run first, check the samples and failures (a replacement that breaks the codes is refused for that unit), then run it for real. Approved units are never touched, states stay as they are.

save_translation, save_translations and replace_in_targets mark your edits with origin "ai". You can overwrite anything below approved (the previous text stays in the unit's history), but it never changes an approved unit. Glossary edits trigger a project QA recheck after 2 s.`

// mcpOrigin marks agent saves in a unit's history.
const mcpOrigin = "ai"

func (s *Server) mcpHandler() http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{Name: "bellingua", Version: "1"},
		&mcp.ServerOptions{Instructions: mcpInstructions, Capabilities: &mcp.ServerCapabilities{}})
	s.mcpTools(srv)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	// The SDK refuses a non-local Host (DNS rebinding); this refuses requests
	// a browser makes on behalf of another site.
	return http.NewCrossOriginProtection().Handler(h)
}

// tool registers a typed tool whose errors reach the agent as readable tool
// errors instead of protocol failures.
func tool[In any](s *Server, srv *mcp.Server, t *mcp.Tool, fn func(ctx context.Context, in In) (any, error)) {
	mcp.AddTool(srv, t, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		out, err := fn(ctx, in)
		if err != nil {
			return nil, nil, s.mcpError(t.Name, err)
		}
		return nil, out, nil
	})
}

func (s *Server) mcpError(name string, err error) error {
	var he *httpError
	var mm *model.CodeMismatch
	switch {
	case errors.As(err, &he):
		return errors.New(he.msg)
	case errors.Is(err, store.ErrNotFound):
		return errors.New("not found")
	case errors.As(err, &mm):
		return fmt.Errorf("the target's codes don't match the source (missing %q, extra %q); copy every code exactly as in the source, or set force", mm.Missing, mm.Extra)
	case errors.Is(err, store.ErrSkipped):
		return errors.New("not changed: this string is approved")
	case errors.Is(err, store.ErrDuplicate), errors.Is(err, store.ErrInvalid), errors.Is(err, context.Canceled):
		return err
	}
	s.log.Error("mcp tool failed", "tool", name, "err", err)
	return err
}

var (
	yes, no     = true, false
	readOnly    = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &no}
	writes      = &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no}
	destructive = &mcp.ToolAnnotations{DestructiveHint: &yes, OpenWorldHint: &no}
)

// --- inputs ------------------------------------------------------------------

type projectIn struct {
	Project int64 `json:"project" jsonschema:"project id (from list_projects)"`
}

type treeIn struct {
	Project int64  `json:"project" jsonschema:"project id"`
	Dir     string `json:"dir,omitempty" jsonschema:"folder path, e.g. \"a/b\"; empty for the root"`
}

type filterIn struct {
	Project int64    `json:"project" jsonschema:"project id"`
	Dir     string   `json:"dir,omitempty" jsonschema:"only units under this folder"`
	File    int64    `json:"file,omitempty" jsonschema:"only units of this file id (from get_tree)"`
	Query   string   `json:"query,omitempty" jsonschema:"text search; 3+ characters search case-insensitively"`
	In      string   `json:"in,omitempty" jsonschema:"where to search: source, target, both (default), key or context"`
	States  []string `json:"states,omitempty" jsonschema:"only these states: untranslated, mt, translated, approved"`
	QA      []string `json:"qa,omitempty" jsonschema:"only units failing one of these QA checks (ids: placeholders, empty, whitespace, punctuation, numbers, double-space, brackets, untranslated, forbidden, length, glossary), or [\"any\"]"`
	Errors  bool     `json:"errors,omitempty" jsonschema:"only units with QA errors"`
	Changed bool     `json:"changed,omitempty" jsonschema:"only units whose source changed on the last import"`
	Same    int64    `json:"same,omitempty" jsonschema:"only units with the same source as this unit id"`
}

func (in filterIn) filter() (store.Filter, error) {
	f := store.Filter{ProjectID: in.Project, Dir: strings.Trim(in.Dir, "/"), FileID: in.File, Query: in.Query, In: in.In,
		Errors: in.Errors, Changed: in.Changed, SameAs: in.Same}
	return f, filterLists(&f, in.States, in.QA)
}

type listIn struct {
	filterIn
	Sort   string `json:"sort,omitempty" jsonschema:"file (default), updated, short or long"`
	Cursor string `json:"cursor,omitempty" jsonschema:"the next value of the previous page"`
	Limit  int    `json:"limit,omitempty" jsonschema:"page size, 1-200 (default 50)"`
	// Langs adds the project's extra-language texts (see get_unit_langs) to each row.
	Langs []string `json:"langs,omitempty" jsonschema:"extra languages whose texts to add to each row as refs, e.g. [\"uk\",\"en\"]"`
}

type unitIn struct {
	ID      int64 `json:"id" jsonschema:"unit id"`
	History bool  `json:"history,omitempty" jsonschema:"include the edit history"`
}

type idIn struct {
	ID int64 `json:"id" jsonschema:"unit id"`
}

type saveIn struct {
	ID     int64  `json:"id" jsonschema:"unit id"`
	Target string `json:"target" jsonschema:"the full translation, with the source's codes inline"`
	State  string `json:"state,omitempty" jsonschema:"translated (default) or mt"`
	Force  bool   `json:"force,omitempty" jsonschema:"save even if the codes don't match the source"`
}

// maxSaveItems bounds one save_translations call.
const maxSaveItems = 200

type saveItemIn struct {
	ID     int64   `json:"id" jsonschema:"unit id"`
	Target *string `json:"target,omitempty" jsonschema:"the full translation, with the source's codes inline; omit to keep the current text and only set the state"`
	State  string  `json:"state,omitempty" jsonschema:"translated (default) or mt"`
}

type saveManyIn struct {
	Items []saveItemIn `json:"items" jsonschema:"1-200 units to save; each is saved on its own, a failing one does not stop the others"`
}

type termsIn struct {
	Project  int64  `json:"project" jsonschema:"project id"`
	Contains string `json:"contains,omitempty" jsonschema:"only terms whose source or target contains this (case-insensitive)"`
}

type formsIn struct {
	Project int64  `json:"project" jsonschema:"project id"`
	Source  string `json:"source" jsonschema:"a term's source word or phrase"`
}

type termFields struct {
	Source        string          `json:"source" jsonschema:"the term in the source language"`
	Target        string          `json:"target,omitempty" jsonschema:"its translation; alternatives separated by |"`
	Note          string          `json:"note,omitempty" jsonschema:"a note for translators"`
	DNT           bool            `json:"dnt,omitempty" jsonschema:"do not translate: the term stays unchanged in the target"`
	CaseSensitive bool            `json:"caseSensitive,omitempty" jsonschema:"match only with the same capitalisation"`
	Forms         []glossary.Form `json:"forms,omitempty" jsonschema:"inflected source forms with their translations, e.g. {src: Баумана, tgt: Баўмана}"`
}

func (f termFields) term() glossary.Term {
	return glossary.Term{Source: f.Source, Target: f.Target, Note: f.Note, DNT: f.DNT, CaseSensitive: f.CaseSensitive, Forms: f.Forms}
}

type createTermIn struct {
	Project int64 `json:"project" jsonschema:"project id"`
	termFields
}

type updateTermIn struct {
	ID int64 `json:"id" jsonschema:"term id; every field is replaced"`
	termFields
}

type termIDIn struct {
	ID int64 `json:"id" jsonschema:"term id"`
}

type jobIn struct {
	ID string `json:"id" jsonschema:"job id"`
}

// --- outputs: plain strings instead of the UI's piece arrays ----------------

type mcpProject struct {
	ID         int64        `json:"id"`
	Name       string       `json:"name"`
	SourceLang string       `json:"sourceLang"`
	TargetLang string       `json:"targetLang"`
	Files      int64        `json:"files"`
	Counts     store.Counts `json:"counts"`
}

type mcpTree struct {
	Dir      string     `json:"dir"`
	Children []TreeNode `json:"children"`
}

type mcpRow struct {
	ID        int64    `json:"id"`
	File      int64    `json:"file"`
	Key       string   `json:"key"`
	Context   string   `json:"context,omitempty"`
	Source    string   `json:"source"`
	Target    *string  `json:"target"` // null = untranslated
	Codes     []string `json:"codes,omitempty"`
	State     string   `json:"state"`
	QA        []string `json:"qa,omitempty"`
	Truncated bool     `json:"truncated,omitempty"` // texts cut to a preview; get_unit has them whole
	Changed   bool     `json:"changed,omitempty"`
	// Refs maps each requested extra language to its text (cut like the
	// source); a language the unit has no text in is absent.
	Refs          map[string]string `json:"refs,omitempty"`
	RefsTruncated bool              `json:"refsTruncated,omitempty"`
}

type mcpSaveResult struct {
	ID    int64    `json:"id"`
	OK    bool     `json:"ok"`
	Error string   `json:"error,omitempty"`
	QA    []string `json:"qa,omitempty"` // failing QA check ids of the saved unit
}

type mcpBatch struct {
	Results []mcpSaveResult `json:"results"`
	Saved   int             `json:"saved"`
	Failed  int             `json:"failed"`
}

type mcpPage struct {
	Units []mcpRow `json:"units"`
	Next  string   `json:"next,omitempty"` // cursor of the next page
}

type mcpCount struct {
	Count int64 `json:"count"`
}

type mcpHistory struct {
	Target *string `json:"target"`
	State  string  `json:"state"`
	Origin string  `json:"origin"`
	At     int64   `json:"at"`
}

type mcpUnitTerm struct {
	ID      int64           `json:"id"`
	Source  string          `json:"source"`
	Target  string          `json:"target"`
	Note    string          `json:"note,omitempty"`
	DNT     bool            `json:"dnt,omitempty"`
	Forms   []glossary.Form `json:"forms,omitempty"`
	Inserts []string        `json:"inserts"` // the agreed text for each occurrence form
}

type mcpUnit struct {
	ID         int64         `json:"id"`
	Project    int64         `json:"project"`
	File       int64         `json:"file"`
	Path       string        `json:"path"`
	Key        string        `json:"key"`
	Context    string        `json:"context,omitempty"`
	Source     string        `json:"source"`
	Target     *string       `json:"target"`
	Codes      []string      `json:"codes,omitempty"`
	State      string        `json:"state"`
	Changed    bool          `json:"changed,omitempty"`
	UpdatedAt  int64         `json:"updatedAt"`
	Issues     []qa.Issue    `json:"issues"`
	Terms      []mcpUnitTerm `json:"terms"`
	Duplicates int           `json:"duplicates,omitempty"` // identical strings still without a human translation
	History    []mcpHistory  `json:"history,omitempty"`
}

type mcpTMMatch struct {
	Unit   int64  `json:"unit"`
	Path   string `json:"path"`
	Source string `json:"source"`
	Target string `json:"target"`
	State  string `json:"state"`
	Score  int    `json:"score"` // percent; 100 = identical source
}

type mcpTM struct {
	Matches    []mcpTMMatch `json:"matches"`
	Duplicates int          `json:"duplicates"`
}

type mcpLang struct {
	Lang     string  `json:"lang"`
	Target   *string `json:"target"`
	State    string  `json:"state"`
	Outdated bool    `json:"outdated,omitempty"` // made from an older source
}

type mcpMT struct {
	Translation  string `json:"translation"`
	Provider     string `json:"provider"`
	WithGlossary string `json:"withGlossary,omitempty"` // the same run with glossary terms fixed in their agreed form
}

type mcpTermOut = glossary.Term

// MCP structured content must be a JSON object, so list results are wrapped.
type mcpProjects struct {
	Projects []mcpProject `json:"projects"`
}

type mcpLangs struct {
	Langs []mcpLang `json:"langs"`
}

type mcpTerms struct {
	Terms []glossary.Term `json:"terms"`
}

type mcpForms struct {
	Forms []glossary.FormCount `json:"forms"`
}

type mcpJobs struct {
	Jobs []Job `json:"jobs"`
}

func plainPtr(ps []model.Piece) *string {
	if ps == nil {
		return nil
	}
	s := model.Plain(ps)
	return &s
}

func checkIDs(mask qa.Check) []string {
	var ids []string
	for _, i := range qa.Describe(mask) {
		ids = append(ids, i.ID)
	}
	return ids
}

func toMCPUnit(r unitResponse) mcpUnit {
	u := r.Unit
	out := mcpUnit{ID: u.ID, Project: u.ProjectID, File: u.FileID, Path: u.FilePath, Key: u.Key, Context: u.Context,
		Source: model.Plain(u.Source), Target: plainPtr(u.Target), Codes: model.Codes(u.Source), State: u.State.String(),
		Changed: u.Changed, UpdatedAt: u.UpdatedAt, Issues: r.Issues, Terms: []mcpUnitTerm{}, Duplicates: r.Duplicates}
	for _, t := range r.Terms {
		ut := mcpUnitTerm{ID: t.ID, Source: t.Source, Target: t.Target, Note: t.Note, DNT: t.DNT, Forms: t.Forms, Inserts: []string{}}
		for _, ins := range t.Inserts {
			if !slices.Contains(ut.Inserts, ins) {
				ut.Inserts = append(ut.Inserts, ins)
			}
		}
		out.Terms = append(out.Terms, ut)
	}
	for _, h := range u.History {
		out.History = append(out.History, mcpHistory{Target: h.Target, State: h.State.String(), Origin: h.Origin, At: h.At})
	}
	return out
}

// mcpSaveState maps an agent's state name to the state it may save.
func mcpSaveState(name string) (model.State, error) {
	switch name {
	case "", "translated":
		return model.Translated, nil
	case "mt":
		return model.MT, nil
	}
	return 0, badRequest("state must be translated or mt, not %q", name)
}

// saveItem saves one entry of save_translations through the same core as
// save_translation. Without a target it keeps the unit's current text.
func (s *Server) saveItem(ctx context.Context, it saveItemIn) (unitResponse, error) {
	state, err := mcpSaveState(it.State)
	if err != nil {
		return unitResponse{}, err
	}
	target := it.Target
	if target == nil {
		cur, err := s.st.Unit(ctx, it.ID, false)
		if err != nil {
			return unitResponse{}, err
		}
		if cur.Target == nil {
			return unitResponse{}, badRequest("the unit has no translation to keep; send a target")
		}
		t := model.Plain(cur.Target)
		target = &t
	}
	return s.save(ctx, it.ID, store.Save{Target: target, State: state, Origin: mcpOrigin, IfBelow: model.Approved})
}

// --- tools -------------------------------------------------------------------

func (s *Server) mcpTools(srv *mcp.Server) {
	tool(s, srv, &mcp.Tool{Name: "list_projects", Annotations: readOnly,
		Description: "Lists the projects with their languages and per-state counts."},
		func(ctx context.Context, _ struct{}) (any, error) {
			ps, err := s.st.Projects(ctx)
			if err != nil {
				return nil, err
			}
			out := mcpProjects{Projects: []mcpProject{}}
			for _, p := range ps {
				out.Projects = append(out.Projects, mcpProject{ID: p.ID, Name: p.Name, SourceLang: p.SourceLang, TargetLang: p.TargetLang, Files: p.Files, Counts: p.Counts})
			}
			return out, nil
		})

	tool(s, srv, &mcp.Tool{Name: "get_tree", Annotations: readOnly,
		Description: "Lists a folder's subfolders and files with their counts. Files carry the file id used by list_units."},
		func(ctx context.Context, in treeIn) (any, error) {
			dir := strings.Trim(in.Dir, "/")
			kids, err := s.treeChildren(ctx, in.Project, dir)
			if err != nil {
				return nil, err
			}
			return mcpTree{Dir: dir, Children: kids}, nil
		})

	tool(s, srv, &mcp.Tool{Name: "list_units", Annotations: readOnly,
		Description: "Lists units matching a filter, one page at a time. Texts are previews (\"truncated\" marks cut ones); call get_unit before editing a long string. With langs, each row also carries those extra languages' texts as refs (cut the same way; \"refsTruncated\")."},
		func(ctx context.Context, in listIn) (any, error) {
			f, err := in.filter()
			if err != nil {
				return nil, err
			}
			sort, ok := store.ParseSort(in.Sort)
			if !ok {
				return nil, badRequest("unknown sort %q", in.Sort)
			}
			after, err := parseCursor(in.Cursor)
			if err != nil {
				return nil, err
			}
			limit := in.Limit
			if limit <= 0 || limit > 200 {
				limit = 50
			}
			rows, next, err := s.st.List(ctx, f, sort, after, limit)
			if err != nil {
				return nil, err
			}
			var refs map[int64][]store.UnitLang
			if len(in.Langs) > 0 {
				ids := make([]int64, len(rows))
				for i, r := range rows {
					ids[i] = r.ID
				}
				if refs, err = s.st.UnitLangsFor(ctx, ids, in.Langs); err != nil {
					return nil, err
				}
			}
			out := mcpPage{Units: []mcpRow{}}
			for _, r := range rows {
				row := mcpRow{ID: r.ID, File: r.FileID, Key: r.Key, Context: r.Context,
					Source: model.Plain(r.Source), Target: plainPtr(r.Target), Codes: model.Codes(r.Source),
					State: r.State.String(), QA: checkIDs(r.QA), Truncated: r.Truncated, Changed: r.Changed}
				for _, l := range refs[r.ID] {
					if row.Refs == nil {
						row.Refs = map[string]string{}
					}
					row.Refs[l.Lang] = model.Plain(l.Target)
					row.RefsTruncated = row.RefsTruncated || l.Truncated
				}
				out.Units = append(out.Units, row)
			}
			if next != nil {
				out.Next = fmt.Sprintf("%d.%d", next.A, next.B)
			}
			return out, nil
		})

	tool(s, srv, &mcp.Tool{Name: "count_units", Annotations: readOnly,
		Description: "Counts the units matching a filter (the same filter as list_units)."},
		func(ctx context.Context, in filterIn) (any, error) {
			f, err := in.filter()
			if err != nil {
				return nil, err
			}
			n, err := s.st.Count(ctx, f)
			return mcpCount{Count: n}, err
		})

	tool(s, srv, &mcp.Tool{Name: "get_unit", Annotations: readOnly,
		Description: "Returns a unit with its full texts, codes, QA issues and the glossary terms found in its source (with the agreed text to insert)."},
		func(ctx context.Context, in unitIn) (any, error) {
			u, err := s.st.Unit(ctx, in.ID, in.History)
			if err != nil {
				return nil, err
			}
			r, err := s.unitWithIssues(ctx, u)
			if err != nil {
				return nil, err
			}
			return toMCPUnit(r), nil
		})

	tool(s, srv, &mcp.Tool{Name: "get_unit_tm", Annotations: readOnly,
		Description: "Returns translation-memory matches for a unit: human translations of identical (100%) or similar sources."},
		func(ctx context.Context, in idIn) (any, error) {
			r, err := s.tmMatches(ctx, in.ID)
			if err != nil {
				return nil, err
			}
			out := mcpTM{Matches: []mcpTMMatch{}, Duplicates: r.Duplicates}
			for _, m := range r.Matches {
				out.Matches = append(out.Matches, mcpTMMatch{Unit: m.UnitID, Path: m.Path, Source: model.Plain(m.Source),
					Target: model.Plain(m.Target), State: m.State.String(), Score: m.Score})
			}
			return out, nil
		})

	tool(s, srv, &mcp.Tool{Name: "get_unit_langs", Annotations: readOnly,
		Description: "Returns the unit's reference translations in the project's extra languages (e.g. English)."},
		func(ctx context.Context, in idIn) (any, error) {
			ls, err := s.st.UnitLangs(ctx, in.ID)
			if err != nil {
				return nil, err
			}
			out := mcpLangs{Langs: []mcpLang{}}
			for _, l := range ls {
				out.Langs = append(out.Langs, mcpLang{Lang: l.Lang, Target: plainPtr(l.Target), State: l.State.String(), Outdated: l.Outdated})
			}
			return out, nil
		})

	tool(s, srv, &mcp.Tool{Name: "machine_translate", Annotations: readOnly,
		Description: "Machine-translates a unit's source with the local MT server. Nothing is saved."},
		func(ctx context.Context, in idIn) (any, error) {
			r, err := s.translateUnit(ctx, in.ID)
			if err != nil {
				return nil, err
			}
			out := mcpMT{Translation: model.Plain(r.Target), Provider: r.Provider}
			if r.Glossary != nil {
				out.WithGlossary = model.Plain(r.Glossary)
			}
			return out, nil
		})

	tool(s, srv, &mcp.Tool{Name: "save_translation", Annotations: writes,
		Description: "Saves a unit's translation as translated (or mt). Refused when the codes don't match the source. Overwrites anything below approved (history keeps the old text), but never changes an approved string."},
		func(ctx context.Context, in saveIn) (any, error) {
			state, err := mcpSaveState(in.State)
			if err != nil {
				return nil, err
			}
			r, err := s.save(ctx, in.ID, store.Save{Target: &in.Target, State: state, Origin: mcpOrigin, Force: in.Force, IfBelow: model.Approved})
			if err != nil {
				return nil, err
			}
			return toMCPUnit(r), nil
		})

	tool(s, srv, &mcp.Tool{Name: "save_translations", Annotations: writes,
		Description: "Saves up to 200 units in one call: items [{id, target?, state?}], each as save_translation does (origin ai, never an approved unit, codes must match). Omit target to keep the unit's current text and only set the state (translated by default): the way to mark a good machine translation as reviewed. Returns one compact result per item {id, ok, error?, qa}; a failing item never stops the others."},
		func(ctx context.Context, in saveManyIn) (any, error) {
			if n := len(in.Items); n == 0 || n > maxSaveItems {
				return nil, badRequest("items must hold 1-%d entries, not %d", maxSaveItems, n)
			}
			out := mcpBatch{Results: make([]mcpSaveResult, 0, len(in.Items))}
			for _, it := range in.Items {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				res := mcpSaveResult{ID: it.ID}
				if r, err := s.saveItem(ctx, it); err != nil {
					res.Error = s.mcpError("save_translations", err).Error()
					out.Failed++
				} else {
					res.OK = true
					res.QA = checkIDs(r.Unit.QA)
					out.Saved++
				}
				out.Results = append(out.Results, res)
			}
			return out, nil
		})

	tool(s, srv, &mcp.Tool{Name: "replace_in_targets", Annotations: writes,
		Description: "Find/replace over translations, for terminology sweeps after a term decision (all inflected forms): pairs [{find, replace}] are applied in order to each target as plain text (case-sensitive; ignoreCase, wholeWord optional). Units come from the list_units filter (query searches the targets; default states mt and translated; with a single pair and no query, the find text is the query; with several pairs give a query, dir or file). DRY RUN BY DEFAULT: dryRun=true only reports; run it first, read the samples, then repeat with dryRun=false. Saves as save_translation does (origin ai, state kept, approved units skipped, a replacement that breaks the codes is refused for that unit only). Returns {dryRun, matched, changed, skipped, failed, samples (max 10, before/after), failures, next}; at most limit (default 500, max 2000) matching units per call, repeat with next as cursor (or just repeat the real run: changed units no longer match)."},
		func(ctx context.Context, in replaceIn) (any, error) { return s.replaceInTargets(ctx, in) })

	tool(s, srv, &mcp.Tool{Name: "propagate_translation", Annotations: writes,
		Description: "Copies a unit's human translation to the identical strings that have none yet (the \"duplicates\" count of get_unit_tm)."},
		func(ctx context.Context, in idIn) (any, error) {
			n, err := s.propagateFrom(ctx, in.ID)
			return map[string]int{"changed": n}, err
		})

	tool(s, srv, &mcp.Tool{Name: "list_terms", Annotations: readOnly,
		Description: "Lists the project's glossary terms."},
		func(ctx context.Context, in termsIn) (any, error) {
			terms, err := s.st.Terms(ctx, in.Project)
			if err != nil {
				return nil, err
			}
			out := mcpTerms{Terms: []glossary.Term{}}
			q := strings.ToLower(in.Contains)
			for _, t := range terms {
				if q == "" || strings.Contains(strings.ToLower(t.Source), q) || strings.Contains(strings.ToLower(t.Target), q) {
					out.Terms = append(out.Terms, t)
				}
			}
			return out, nil
		})

	tool(s, srv, &mcp.Tool{Name: "term_forms", Annotations: readOnly,
		Description: "Lists the inflected forms of a source word or phrase found in the project's texts, with counts; useful to fill a term's forms."},
		func(ctx context.Context, in formsIn) (any, error) {
			p, err := s.st.Project(ctx, in.Project)
			if err != nil {
				return nil, err
			}
			src := strings.TrimSpace(in.Source)
			if src == "" {
				return nil, badRequest("source is required")
			}
			forms, err := s.st.TermForms(ctx, p, src)
			if forms == nil {
				forms = []glossary.FormCount{}
			}
			return mcpForms{Forms: forms}, err
		})

	tool(s, srv, &mcp.Tool{Name: "create_term", Annotations: writes,
		Description: "Adds a glossary term."},
		func(ctx context.Context, in createTermIn) (any, error) {
			if _, err := s.st.Project(ctx, in.Project); err != nil {
				return nil, err
			}
			return s.addTerm(ctx, in.Project, in.term())
		})

	tool(s, srv, &mcp.Tool{Name: "update_term", Annotations: writes,
		Description: "Replaces a glossary term's fields (send all of them, including forms)."},
		func(ctx context.Context, in updateTermIn) (any, error) {
			return s.editTerm(ctx, in.ID, in.term())
		})

	tool(s, srv, &mcp.Tool{Name: "delete_term", Annotations: destructive,
		Description: "Deletes a glossary term."},
		func(ctx context.Context, in termIDIn) (any, error) {
			return map[string]bool{"deleted": true}, s.removeTerm(ctx, in.ID)
		})

	tool(s, srv, &mcp.Tool{Name: "recheck_qa", Annotations: writes,
		Description: "Starts a QA recheck of the whole project as a background job; follow it with get_job."},
		func(ctx context.Context, in projectIn) (any, error) {
			p, err := s.st.Project(ctx, in.Project)
			if err != nil {
				return nil, err
			}
			return s.startQA(ctx, p)
		})

	tool(s, srv, &mcp.Tool{Name: "list_jobs", Annotations: readOnly,
		Description: "Lists recent background jobs (import, export, MT, QA, backup) and their progress."},
		func(ctx context.Context, _ struct{}) (any, error) {
			jobs := s.jobs.List()
			if jobs == nil {
				jobs = []Job{}
			}
			return mcpJobs{Jobs: jobs}, nil
		})

	tool(s, srv, &mcp.Tool{Name: "get_job", Annotations: readOnly,
		Description: "Returns one background job's status and result."},
		func(ctx context.Context, in jobIn) (any, error) {
			j, ok := s.jobs.Get(in.ID)
			if !ok {
				return nil, store.ErrNotFound
			}
			return j, nil
		})
}
