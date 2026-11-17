package rx

import (
	"reflect"
	"strings"
	"testing"
)

func must(t *testing.T, pat string) *Regexp {
	t.Helper()
	r, err := Compile(pat)
	if err != nil {
		t.Fatalf("Compile(%q): %v", pat, err)
	}
	return r
}

// \b must work on Cyrillic (Go's own \b is ASCII-only) and count an
// apostrophe between letters as part of the word.
func TestWordBoundary(t *testing.T) {
	for _, c := range []struct {
		pat, text string
		want      bool
	}{
		{`ў\b`, "ён ішоў дадому", true},
		{`ў\b`, "паўза", false},
		{`\bў`, "пайшоў ў лес", true},
		{`\bў`, "паўза", false},
		{`\bдзверы\b`, "адна дзверы.", true},
		{`\bдзверы\b`, "аднадзверы", false},
		{`\bект`, "аб'ект", false}, // the apostrophe keeps "аб'ект" one word
		{`аб\b`, "аб’ект", false},  // typographic apostrophe too
		{`аб\b`, "аб' ект", true},  // a trailing apostrophe is not in the word
		{`\Bў`, "паўза", true},     // \B is the opposite
		{`\Bў`, "ў лес", false},
		{`\bfoo\b`, "a foo b", true}, // ASCII still works
		{`\Q\b\E`, `a\bc`, true},     // quoted: a literal backslash-b
		{`\Q\b\E`, "a b", false},
		{`\b(?:К|Да)\b`, "Групы Да", true},
		{`\b\p{L}+ў\b`, "паўза ішоў", true}, // a rejected candidate does not hide a later match
	} {
		if got := must(t, c.pat).MatchString(c.text); got != c.want {
			t.Errorf("%q on %q = %v, want %v", c.pat, c.text, got, c.want)
		}
	}
}

// \w and \W cover Unicode letters, digits and marks, inside classes too.
func TestWordClass(t *testing.T) {
	r := must(t, `\w+`)
	if got := r.ReplaceAllString("ён ішоў", "X"); got != "X X" {
		t.Errorf(`\w+ -> %q`, got)
	}
	if got := must(t, `[\w-]+`).ReplaceAllString("то-бок так", "X"); got != "X X" {
		t.Errorf(`[\w-]+ -> %q`, got)
	}
	if got := must(t, `\W+`).ReplaceAllString("ён, ішоў", "_"); got != "ён_ішоў" {
		t.Errorf(`\W+ -> %q`, got)
	}
}

// Groups keep the numbers and names the caller wrote, whatever \b adds inside.
func TestReplaceGroups(t *testing.T) {
	r := must(t, `\b(\p{L}+)ў\b (?P<next>\p{L}+)`)
	if got := r.ReplaceAllString("ён ішоў дадому", "${next} $1у"); got != "ён дадому ішоу" {
		t.Errorf("got %q", got)
	}
	if got := must(t, `(\d+)\$`).ReplaceAllString("5$", "$$$1"); got != "$5" {
		t.Errorf("got %q", got)
	}
	// Only verified matches are replaced.
	if got := must(t, `ў\b`).ReplaceAllString("паўза ішоў", "у"); got != "паўза ішоу" {
		t.Errorf("got %q", got)
	}
	if got := must(t, `ў\b`).FindAllStringIndex("ў паўза ў"); !reflect.DeepEqual(got, [][]int{{0, 2}, {14, 16}}) {
		t.Errorf("FindAllStringIndex = %v", got)
	}
}

// Literal is text every match contains: the list query narrows by it through
// the trigram index, so it must never be something a match could lack.
func TestLiteral(t *testing.T) {
	for pat, want := range map[string]string{
		`(?i)джотаро\p{L}*`: "джотаро",
		`Груп(ы|а) К`:       "Груп",
		`\bадна дзверы\b`:   "адна дзверы",
		`а|абв`:             "",
		`ў\b`:               "",
		`(абв)?где`:         "где",
		`(абв)+`:            "абв",
		`(абв)*`:            "",
		`аб{2,}вгд`:         "вгд",
		`[аб]вгд.`:          "вгд",
	} {
		if got := must(t, pat).Literal(); !strings.EqualFold(got, want) { // an ignore-case literal may come back in either case
			t.Errorf("Literal(%q) = %q, want %q", pat, got, want)
		}
	}
}

func TestCompileError(t *testing.T) {
	if _, err := Compile(`(аб`); err == nil {
		t.Error("no error for an unbalanced pattern")
	}
	if _, err := Compile(``); err == nil {
		t.Error("no error for an empty pattern")
	}
}
