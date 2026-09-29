package xliff

import (
	"bytes"
	"strings"
	"testing"
)

func tx(s string) Piece { return Piece{Text: s} }
func cd(s string) Piece { return Piece{Text: s, Code: true} }

func sample() *File {
	return &File{
		Original: "onscreens.csv", SourceLang: "ru", TargetLang: "be",
		Units: []Unit{
			{
				ID: "17437", Note: "Tutorial | a & b",
				Source: []Piece{tx("Можете "), cd(`<Rich color="Gold">`), tx("схватить"), cd("</>"), tx(" его <сразу>")},
				Target: []Piece{tx("Можаце "), cd(`<Rich color="Gold">`), tx("схапіць"), cd("</>"), tx(" яго")},
				State:  "needs-review-translation",
			},
			{ID: "40", Source: []Piece{tx("Новости")}},
		},
	}
}

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if dropped, err := Write(&buf, sample()); err != nil || dropped != nil {
		t.Fatalf("write: %v %v", dropped, err)
	}
	out := buf.String()
	for _, want := range []string{
		`<ph id="1">&lt;Rich color=&#34;Gold&#34;&gt;</ph>`,
		`<target state="needs-review-translation">Можаце <ph id="1">`,
		`его &lt;сразу&gt;`,
		`<note>Tutorial | a &amp; b</note>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	f, problems, err := Read(strings.NewReader(out))
	if err != nil || problems != nil {
		t.Fatalf("read: %v %v", problems, err)
	}
	if f.SourceLang != "ru" || f.TargetLang != "be" || len(f.Units) != 2 {
		t.Fatalf("file = %+v", f)
	}
	u := f.Units[0]
	if Plain(u.Source) != `Можете <Rich color="Gold">схватить</> его <сразу>` ||
		Plain(u.Target) != `Можаце <Rich color="Gold">схапіць</> яго` ||
		u.Note != "Tutorial | a & b" || u.State != "needs-review-translation" {
		t.Errorf("unit = %+v", u)
	}
	if f.Units[1].Target != nil {
		t.Errorf("untranslated unit got a target: %+v", f.Units[1])
	}
}

// A translator may reorder tags and a tool may rewrite their content; the
// source markup is what comes back.
func TestReadReorderedAndRewrittenCodes(t *testing.T) {
	doc := `<?xml version="1.0"?><xliff version="1.2"><file original="x" source-language="ru" target-language="be"><body>
<trans-unit id="1"><source><ph id="1">{a}</ph> и <ph id="2">{b}</ph></source>
<target><x id="2"/> і <ph id="1">garbage</ph></target></trans-unit></body></file></xliff>`
	f, problems, err := Read(strings.NewReader(doc))
	if err != nil || problems != nil {
		t.Fatalf("%v %v", problems, err)
	}
	if got := Plain(f.Units[0].Target); got != "{b} і {a}" {
		t.Errorf("target = %q", got)
	}
}

func TestReadRejectsBrokenCodes(t *testing.T) {
	for name, target := range map[string]string{
		"missing":   `<target>толькі тэкст <ph id="1">x</ph></target>`,
		"duplicate": `<target><ph id="1">x</ph><ph id="1">x</ph><ph id="2">y</ph></target>`,
		"unknown":   `<target><ph id="1">x</ph><ph id="2">y</ph><ph id="9">z</ph></target>`,
	} {
		doc := `<xliff><file><body><trans-unit id="u"><source><ph id="1">{a}</ph> и <ph id="2">{b}</ph></source>` +
			target + `</trans-unit></body></file></xliff>`
		f, problems, err := Read(strings.NewReader(doc))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(problems) != 1 || f.Units[0].Target != nil {
			t.Errorf("%s: problems %v, target %+v", name, problems, f.Units[0].Target)
		}
	}
}

func TestWriteDropsUnmatchableTarget(t *testing.T) {
	f := sample()
	f.Units[0].Target[1].Text = `<Rich color="Blue">` // not a source code
	var buf bytes.Buffer
	dropped, err := Write(&buf, f)
	if err != nil || len(dropped) != 1 || dropped[0] != "17437" {
		t.Fatalf("dropped %v err %v", dropped, err)
	}
	if strings.Contains(buf.String(), "<target") {
		t.Error("unmatchable target was written")
	}
}
