package model

import "testing"

func TestWords(t *testing.T) {
	text := func(s string) Piece { return Piece{Text: s} }
	code := func(s string) Piece { return Piece{Text: s, Code: true} }
	for _, c := range []struct {
		name string
		in   []Piece
		want int
	}{
		{"empty", nil, 0},
		{"plain", []Piece{text("Hello, brave new world!")}, 4},
		{"cyrillic", []Piece{text("Прывітанне, свет")}, 2},
		{"hyphen and apostrophe stay inside a word", []Piece{text("well-known п'ятка don’t")}, 3},
		{"lone punctuation is not a word", []Piece{text("a - b — c ...")}, 3},
		{"numbers count", []Piece{text("Level 12 of 100")}, 4},
		{"codes are not words and separate them", []Piece{text("Press "), code("{key}"), text("now"), code("<b>"), text("go"), code("</b>")}, 3},
		{"code only", []Piece{code("{0}")}, 0},
	} {
		if got := Words(c.in); got != c.want {
			t.Errorf("%s: Words = %d, want %d", c.name, got, c.want)
		}
	}
}
