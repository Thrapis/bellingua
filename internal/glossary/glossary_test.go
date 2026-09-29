package glossary

import (
	"testing"
	"unicode/utf16"

	"github.com/Thrapis/bellingua/internal/model"
)

func tx(s string) model.Piece { return model.Piece{Text: s} }
func cd(s string) model.Piece { return model.Piece{Text: s, Code: true} }

// sub returns the text between UTF-16 offsets, like JavaScript slice().
func sub(s string, start, end int) string {
	u := utf16.Encode([]rune(s))
	return string(utf16.Decode(u[start:end]))
}

var terms = []Term{
	{ID: 1, Source: "Орбита", Target: "Орбіта"},
	{ID: 2, Source: "Орбита Корпорейшн", Target: "Карпарацыя Орбіта"},
	{ID: 3, Source: "землетрясение", Target: "землятрус"},
	{ID: 4, Source: "Silver Bay", DNT: true},
	{ID: 5, Source: "Мост", Target: "Мост", CaseSensitive: true}, // a name, not «мост» (bridge)
	{ID: 6, Source: "программист", Target: "праграміст|кодэр"},
}

func TestMatchInflectedAndLongest(t *testing.T) {
	g := New("ru", terms)
	src := []model.Piece{tx("Сотрудники "), cd(`<Rich color="x">`), tx("Орбиты"), cd("</>"), tx(" и Орбита Корпорейшн боятся землетрясения.")}
	plain := model.Plain(src)
	got := g.Match(src)
	want := []struct {
		id   int64
		text string
	}{{1, "Орбиты"}, {2, "Орбита Корпорейшн"}, {3, "землетрясения"}}
	if len(got) != len(want) {
		t.Fatalf("matches = %+v", got)
	}
	for i, w := range want {
		if got[i].TermID != w.id || sub(plain, got[i].Start, got[i].End) != w.text {
			t.Errorf("match %d = %+v %q, want %d %q", i, got[i], sub(plain, got[i].Start, got[i].End), w.id, w.text)
		}
	}
}

func TestMarkupIsNotMatched(t *testing.T) {
	g := New("ru", []Term{{ID: 1, Source: "color", Target: "колер"}})
	if m := g.Match([]model.Piece{cd(`<Rich color="Gold">`), tx("цвет")}); len(m) != 0 {
		t.Errorf("matched inside markup: %+v", m)
	}
}

func TestCaseSensitive(t *testing.T) {
	g := New("ru", terms)
	if m := g.Match([]model.Piece{tx("Перейдите мост")}); len(m) != 0 {
		t.Errorf("lower-case «мост» matched the case-sensitive name: %+v", m)
	}
	if m := g.Match([]model.Piece{tx("Банда Мост")}); len(m) != 1 {
		t.Errorf("«Мост» not matched: %+v", m)
	}
}

func TestCheck(t *testing.T) {
	g := New("ru", terms)
	cases := []struct {
		name     string
		src, tgt string
		problems []int64
	}{
		{"inflected target ok", "Бойтесь землетрясения", "Бойцеся землятрусу", nil},
		{"missing target term", "Бойтесь землетрясения", "Бойцеся навальніцы", []int64{3}},
		{"alternative target", "Программист взломал", "Кодэр узламаў", nil},
		{"dnt kept", "Добро пожаловать в Silver Bay", "Сардэчна запрашаем у Silver Bay", nil},
		{"dnt translated", "Добро пожаловать в Silver Bay", "Сардэчна запрашаем у Срэбная Бухта", []int64{4}},
		{"multi-word target", "Орбита Корпорейшн", "Карпарацыі Орбіты", nil},
	}
	for _, c := range cases {
		var got []int64
		for _, p := range g.Check([]model.Piece{tx(c.src)}, []model.Piece{tx(c.tgt)}) {
			got = append(got, p.Term.ID)
		}
		if len(got) != len(c.problems) || (len(got) > 0 && got[0] != c.problems[0]) {
			t.Errorf("%s: problems %v, want %v", c.name, got, c.problems)
		}
	}
}

func TestNonRussianIsExactWord(t *testing.T) {
	g := New("en", []Term{{ID: 1, Source: "programmer", Target: "праграміст"}})
	if m := g.Match([]model.Piece{tx("The Programmer and programmers")}); len(m) != 1 {
		t.Errorf("matches = %+v", m)
	}
}
