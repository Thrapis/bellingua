package qa

import (
	"testing"

	"github.com/Thrapis/bellingua/internal/glossary"
	"github.com/Thrapis/bellingua/internal/model"
)

func text(s string) []model.Piece { return []model.Piece{{Text: s}} }

func TestChecks(t *testing.T) {
	r := NewRunner(DefaultConfig("be"))
	src := []model.Piece{{Text: "Нажмите "}, {Text: "{KEY}", Code: true}, {Text: ", чтобы выстрелить 3 раза."}}
	cases := []struct {
		name  string
		src   []model.Piece
		tgt   []model.Piece
		state model.State
		want  Check
	}{
		{"clean", src, []model.Piece{{Text: "Націсніце "}, {Text: "{KEY}", Code: true}, {Text: ", каб стрэліць 3 разы."}}, model.Translated, 0},
		{"no target", src, nil, model.Untranslated, 0},
		{"placeholders", src, text("Націсніце, каб стрэліць 3 разы."), model.MT, Placeholders},
		{"empty", text("Да"), []model.Piece{}, model.Translated, Empty},
		{"whitespace", text("Да "), text("Так"), model.MT, Whitespace},
		{"punctuation", text("Да!"), text("Так."), model.MT, Punctuation},
		{"ellipsis forms equal", text("Ну..."), text("Ну…"), model.MT, 0},
		{"numbers", text("Урон 25,5"), text("Шкода 25"), model.MT, Numbers},
		{"double space", text("а б"), text("а  б"), model.MT, DoubleSpace},
		{"brackets", text("«Орбита»"), text("«Орбіта"), model.MT, Brackets},
		{"untranslated", text("Дмитрий"), text("Дмитрий"), model.MT, Untranslated | Forbidden},
		{"forbidden", text("Привет"), text("Прывiт и"), model.MT, Forbidden},
		{"same code-only", []model.Piece{{Text: "{X}", Code: true}}, []model.Piece{{Text: "{X}", Code: true}}, model.MT, 0},
		{"length", text("Короткая строка"), text("Вельмі-вельмі доўгі радок, значна даўжэйшы за крыніцу"), model.MT, Length},
	}
	for _, c := range cases {
		got, issues := r.Run(Unit{Source: c.src, Target: c.tgt, State: c.state})
		if got != c.want {
			t.Errorf("%s: mask %b, want %b (%+v)", c.name, got, c.want, issues)
		}
		if len(issues) != popcount(got) {
			t.Errorf("%s: %d issues for mask %b", c.name, len(issues), got)
		}
		if m := r.Mask(Unit{Source: c.src, Target: c.tgt, State: c.state}); m != got {
			t.Errorf("%s: Mask %b != Run %b", c.name, m, got)
		}
	}
}

func TestDisabled(t *testing.T) {
	r := NewRunner(Config{Disabled: []string{"whitespace"}})
	if m := r.Mask(Unit{Source: text("Да "), Target: text("Так"), State: model.MT}); m != 0 {
		t.Errorf("disabled check fired: %b", m)
	}
}

func popcount(c Check) int {
	n := 0
	for ; c != 0; c &= c - 1 {
		n++
	}
	return n
}

func TestGlossaryCheck(t *testing.T) {
	g := glossary.New("ru", []glossary.Term{{ID: 1, Source: "землетрясение", Target: "землятрус"}})
	r := NewRunner(DefaultConfig("be")).WithGlossary(g)
	m, issues := r.Run(Unit{Source: text("Это землетрясение."), Target: text("Гэта навальніца."), State: model.MT})
	if m != Terms || len(issues) != 1 || issues[0].Check != "glossary" {
		t.Errorf("mask %b issues %+v", m, issues)
	}
	if m := r.Mask(Unit{Source: text("Это землетрясение."), Target: text("Гэта землятрус."), State: model.MT}); m != 0 {
		t.Errorf("agreed term flagged: %b", m)
	}
	if m := NewRunner(Config{Disabled: []string{"glossary"}}).WithGlossary(g).Mask(Unit{Source: text("землетрясение"), Target: text("x"), State: model.MT}); m&Terms != 0 {
		t.Error("disabled glossary check fired")
	}
}
