package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Thrapis/bellingua/internal/store"
)

// preserveCase gives the replacement the letter case of what it replaced.
func TestMatchCase(t *testing.T) {
	for _, c := range []struct{ match, repl, want string }{
		{"ДЖОТАРО", "Джотара", "ДЖОТАРА"}, // all capitals
		{"Джотаро", "джотара", "Джотара"}, // capitalised
		{"джотаро", "Джотара", "джотара"}, // all lower
		{"Я", "мы", "Мы"},                 // one capital letter is a capitalised word, not shouting
		{"ОК", "ok", "OK"},                // two capitals are shouting
		{"ДжоТАро", "мы", "Мы"},           // starts with a capital, rest mixed: capitalised
		{"джоТАро", "Мы", "Мы"},           // mixed, starts lower: as given
		{"123", "Ab", "Ab"},               // no letters: as given
		{"ДЖОТАРО", "", ""},               // an empty replacement stays empty
		{"Аб'ект", "аб'екты", "Аб'екты"},  // the apostrophe is no letter
		{"АБ'ЕКТ", "аб'екты", "АБ'ЕКТЫ"},
	} {
		if got := matchCase(c.match, c.repl); got != c.want {
			t.Errorf("matchCase(%q, %q) = %q, want %q", c.match, c.repl, got, c.want)
		}
	}
}

func TestMCPReplacePreserveCase(t *testing.T) {
	cs, _, p, ids := newSweep(t, []sweepUnit{
		{"c1", "Один", "ДЖОТАРО і Джотаро і джотаро"},
		{"c2", "Два", "ДжоТАро тут"},
	})
	run := func(extra map[string]any) mcpSweep {
		a := map[string]any{"query": "джотаро", "pairs": []any{pair("джотаро", "джотара")}, "ignoreCase": true}
		for k, v := range extra {
			a[k] = v
		}
		out, text, isErr := sweep(t, cs, p, a)
		if isErr {
			t.Fatal(text)
		}
		return out
	}
	out := run(map[string]any{"preserveCase": true})
	if s, _ := sampleOf(out, ids["c1"]); s.After != "ДЖОТАРА і Джотара і джотара" {
		t.Errorf("preserveCase c1 = %q", s.After)
	}
	if s, _ := sampleOf(out, ids["c2"]); s.After != "Джотара тут" {
		t.Errorf("preserveCase c2 = %q", s.After)
	}
	// Without it the replacement is inserted as written.
	out = run(nil)
	if s, _ := sampleOf(out, ids["c1"]); s.After != "джотара і джотара і джотара" {
		t.Errorf("as written c1 = %q", s.After)
	}
	// It works on a regex template too.
	out = run(map[string]any{"regex": true, "preserveCase": true, "pairs": []any{pair(`(дж)отаро`, "${1}отара")}})
	if s, _ := sampleOf(out, ids["c1"]); s.After != "ДЖОТАРА і Джотара і джотара" {
		t.Errorf("regex c1 = %q", s.After)
	}
}

// full=true lists every matched unit with before/after, approved ones
// marked; the report counts matches inside approved units and says, per
// pair, how many units and occurrences it found.
func TestMCPReplaceFullDryRun(t *testing.T) {
	cs, _, p, ids := sweepFixture(t)
	out, text, isErr := sweep(t, cs, p, map[string]any{"pairs": []any{pair("Сеткавы дазор", "Сеткавая варта")},
		"states": []string{"mt", "translated", "approved"}, "full": true})
	if isErr {
		t.Fatal(text)
	}
	if out.Matched != 4 || out.ApprovedMatches != 1 || len(out.Matches) != 4 {
		t.Fatalf("report = %+v", out)
	}
	got := map[int64]mcpSweepSample{}
	for _, m := range out.Matches {
		got[m.ID] = m
	}
	if m := got[ids["k5"]]; !m.Approved || m.Before != "Сеткавы дазор" || m.After != "Сеткавая варта" {
		t.Errorf("approved match = %+v", m)
	}
	if m := got[ids["k1"]]; m.Approved || m.After != "Сеткавая варта {KEY} тут" {
		t.Errorf("k1 match = %+v", m)
	}
	if len(out.Pairs) != 1 || out.Pairs[0].Find != "Сеткавы дазор" || out.Pairs[0].Units != 4 || out.Pairs[0].Occurrences != 4 {
		t.Errorf("pair counts = %+v", out.Pairs)
	}
	// Without full, only the samples come back.
	out, _, _ = sweep(t, cs, p, map[string]any{"pairs": []any{pair("Сеткавы дазор", "Сеткавая варта")}})
	if out.Matches != nil {
		t.Errorf("matches without full = %+v", out.Matches)
	}
}

// A pair with no match says whether its replacement text is already there
// ("already fixed") or not ("typo in the find text").
func TestMCPReplaceZeroMatchHint(t *testing.T) {
	cs, _, p, _ := sweepFixture(t)
	sweep(t, cs, p, map[string]any{"pairs": []any{pair("Сеткавы дазор", "Сеткавая варта")}, "dryRun": false})
	out, text, isErr := sweep(t, cs, p, map[string]any{"query": "Сеткав", "pairs": []any{
		pair("Сеткавы дазор", "Сеткавая варта"), // done before
		pair("Сеткавы дазорр", "ЦЦЦЦ"),          // typo
	}})
	if isErr {
		t.Fatal(text)
	}
	if out.Matched != 0 || len(out.Pairs) != 2 {
		t.Fatalf("report = %+v", out)
	}
	if p0 := out.Pairs[0]; p0.Occurrences != 0 || p0.AlreadyHave != 3 {
		t.Errorf("already fixed pair = %+v", p0)
	}
	if p1 := out.Pairs[1]; p1.Occurrences != 0 || p1.AlreadyHave != 0 {
		t.Errorf("typo pair = %+v", p1)
	}
}

func revert(t *testing.T, cs *mcp.ClientSession, p *store.Project, args map[string]any) (mcpRevert, string, bool) {
	t.Helper()
	args["project"] = p.ID
	var out mcpRevert
	text, isErr := call(t, cs, "revert_sweep", args, &out)
	return out, text, isErr
}

// A real run returns a sweepId; revert_sweep puts the old texts back for the
// units that still hold the sweep's result, and skips the ones edited or
// approved since. It is a dry run unless dryRun=false.
func TestMCPRevertSweep(t *testing.T) {
	cs, ts, p, ids := sweepFixture(t)
	dry, _, _ := sweep(t, cs, p, map[string]any{"pairs": []any{pair("Сеткавы дазор", "Сеткавая варта")}})
	if dry.SweepID != "" {
		t.Errorf("a dry run has no sweepId: %q", dry.SweepID)
	}
	run, text, isErr := sweep(t, cs, p, map[string]any{"pairs": []any{pair("Сеткавы дазор", "Сеткавая варта")}, "dryRun": false})
	if isErr || !strings.HasPrefix(run.SweepID, "sweep-") || run.Changed != 3 {
		t.Fatalf("run = %+v %s", run, text)
	}
	// k2 is edited by hand afterwards, k7 gets approved.
	if res, b := do(t, ts, "PUT", "/api/units/"+itoa(ids["k2"]), map[string]any{"target": "Мая праўка", "state": "translated"}); res.StatusCode != http.StatusOK {
		t.Fatalf("edit: %d %s", res.StatusCode, b)
	}
	if res, b := do(t, ts, "PUT", "/api/units/"+itoa(ids["k7"]), map[string]any{"target": "Сеткавая варта ужо", "state": "approved"}); res.StatusCode != http.StatusOK {
		t.Fatalf("approve: %d %s", res.StatusCode, b)
	}

	out, text, isErr := revert(t, cs, p, map[string]any{"sweepId": run.SweepID})
	if isErr {
		t.Fatal(text)
	}
	if !out.DryRun || out.Matched != 3 || out.Reverted != 1 || out.Skipped != 2 || out.Failed != 0 {
		t.Errorf("revert dry run = %+v", out)
	}
	if u := unitOf(t, cs, ids["k1"]); derefStr(u.Target) != "Сеткавая варта {KEY} тут" {
		t.Errorf("dry run wrote: %v", u.Target)
	}

	out, _, _ = revert(t, cs, p, map[string]any{"sweepId": run.SweepID, "dryRun": false})
	if out.DryRun || out.Reverted != 1 || out.Skipped != 2 {
		t.Errorf("revert = %+v", out)
	}
	for k, want := range map[string]string{
		"k1": "Сеткавы дазор {KEY} тут", // reverted, state kept
		"k2": "Мая праўка",              // edited since: left alone
		"k7": "Сеткавая варта ужо",      // approved since: left alone
	} {
		if u := unitOf(t, cs, ids[k]); derefStr(u.Target) != want {
			t.Errorf("%s = %q, want %q", k, derefStr(u.Target), want)
		}
	}
	if u := unitOf(t, cs, ids["k1"]); u.State != "mt" || len(u.History) == 0 || u.History[0].Origin != "ai" {
		t.Errorf("k1 after revert = %s %+v", u.State, u.History)
	}
	// Nothing is left to revert: the units no longer hold the sweep's result.
	again, _, _ := revert(t, cs, p, map[string]any{"sweepId": run.SweepID, "dryRun": false})
	if again.Reverted != 0 || again.Skipped != 3 {
		t.Errorf("second revert = %+v", again)
	}

	if _, text, isErr := revert(t, cs, p, map[string]any{"sweepId": "bulk-1-abc"}); !isErr || !strings.Contains(text, "sweepId") {
		t.Errorf("a non-sweep id: isErr=%v %q", isErr, text)
	}
	if _, text, isErr := revert(t, cs, p, map[string]any{"sweepId": "sweep-1-abc"}); !isErr || !strings.Contains(text, "no changes") {
		t.Errorf("unknown sweep: isErr=%v %q", isErr, text)
	}
}
