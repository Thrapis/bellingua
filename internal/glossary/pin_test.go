package glossary

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Thrapis/bellingua/internal/model"
)

func TestPinUnpin(t *testing.T) {
	g := New("ru", append(terms, Term{ID: 7, Source: "бестия", Target: "бэстыя"}, Term{ID: 8, Source: "справка"}))
	src := []model.Piece{tx("Бестия ищет "), cd(`<b>`), tx("Орбиту"), cd("</b>"), tx(" в Silver Bay, справка. ПРОГРАММИСТ!")}
	pinned, repl := g.Pin(src)
	wantRepl := []string{"Бэстыя", "Орбіта", "Silver Bay", "ПРАГРАМІСТ"}
	if !reflect.DeepEqual(repl, wantRepl) {
		t.Fatalf("repl = %q", repl)
	}
	if model.Plain(pinned) == model.Plain(src) || pinned[0].Code != true {
		t.Fatalf("pinned = %+v", pinned)
	}
	// A provider keeps codes and translates the text around them; merged
	// markers (a pin next to markup) must split back apart.
	tr := []model.Piece{pinned[0], tx(" шукае "), cd("<b>" + pinned[3].Text + "</b>"), tx(" у "), pinned[6], tx(", даведка. "), pinned[8], tx("!")}
	got := Unpin(tr, repl)
	want := []model.Piece{tx("Бэстыя шукае "), cd("<b>"), tx("Орбіта"), cd("</b>"), tx(" у Silver Bay, даведка. ПРАГРАМІСТ!")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unpin = %+v", got)
	}
}

func TestForms(t *testing.T) {
	bm := Term{ID: 9, Source: "Бауман", Target: "Баўман", CaseSensitive: true,
		Forms: []Form{{"Баумана", "Баўмана"}, {"бауманом", "Баўманам"}}}
	g := New("ru", []Term{bm})

	src := []model.Piece{tx("Спроси Баумана о БАУМАНОМ, Бауману и Бауман.")}
	var ins []string
	for _, m := range g.Match(src) {
		ins = append(ins, m.Insert)
	}
	// A form, a form in all caps (case-insensitive lookup), no form -> base.
	if want := []string{"Баўмана", "БАЎМАНАМ", "Баўман", "Баўман"}; !reflect.DeepEqual(ins, want) {
		t.Errorf("inserts = %q", ins)
	}
	if _, repl := g.Pin(src); !reflect.DeepEqual(repl, ins) {
		t.Errorf("pin repl = %q", repl)
	}
	// QA: a form translation counts as the agreed term.
	if p := g.Check([]model.Piece{tx("Спроси Баумана")}, []model.Piece{tx("Спытай у Баўмана")}); len(p) != 0 {
		t.Errorf("form flagged: %+v", p)
	}

	c := NewFormCounter("ru", "Бауман")
	for _, s := range []string{"Баумана нет.", "Где Баумана?", "баумана", "Бауман и Бауманом"} {
		c.Add(s)
	}
	if got, want := c.Result(), []FormCount{{"Баумана", 3}, {"Бауманом", 1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("forms = %+v", got)
	}
	if k := SearchKey("ru", "Бауман Младший"); k == "" || !strings.HasPrefix("баумана", k) {
		t.Errorf("search key = %q", k)
	}
}

func TestPinNothing(t *testing.T) {
	g := New("ru", terms)
	src := []model.Piece{tx("Просто текст.")}
	if p, r := g.Pin(src); r != nil || !reflect.DeepEqual(p, src) {
		t.Errorf("%+v %q", p, r)
	}
}
