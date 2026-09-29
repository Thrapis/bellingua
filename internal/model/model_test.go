package model

import (
	"errors"
	"reflect"
	"testing"
)

func TestPiecesBlobRoundTrip(t *testing.T) {
	ps := []Piece{{Text: "Можете "}, {Text: `<Rich color="Gold">`, Code: true}, {Text: ""}, {Text: "\n", Code: true}}
	got, err := DecodePieces(EncodePieces(ps), Plain(ps))
	if err != nil || !reflect.DeepEqual(got, ps) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if b := EncodePieces([]Piece{{Text: "plain"}}); b != nil {
		t.Errorf("text-only pieces encoded to %v, want nil", b)
	}
	got, _ = DecodePieces(nil, "plain")
	if !reflect.DeepEqual(got, []Piece{{Text: "plain"}}) {
		t.Errorf("nil blob = %+v", got)
	}
	if _, err := DecodePieces([]byte{0, 9, 'a'}, ""); err == nil {
		t.Error("truncated blob decoded without error")
	}
}

func TestSplitTarget(t *testing.T) {
	src := []Piece{{Text: "Нажмите "}, {Text: "{KEY}", Code: true}, {Text: " и "}, {Text: "<b>", Code: true}, {Text: "беги"}, {Text: "</>", Code: true}}

	got, err := SplitTarget("<b>Бяжы</> пасля {KEY}", src)
	if err != nil {
		t.Fatal(err)
	}
	want := []Piece{{Text: "<b>", Code: true}, {Text: "Бяжы"}, {Text: "</>", Code: true}, {Text: " пасля "}, {Text: "{KEY}", Code: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}

	_, err = SplitTarget("<b>Бяжы {KEY} {KEY}", src)
	var m *CodeMismatch
	if !errors.As(err, &m) || !reflect.DeepEqual(m.Missing, []string{"</>"}) || !reflect.DeepEqual(m.Extra, []string{"{KEY}"}) {
		t.Errorf("err = %v", err)
	}
}

// A code that is a prefix of another must not steal the longer one's match.
func TestSplitTargetLongestCodeWins(t *testing.T) {
	src := []Piece{{Text: "<", Code: true}, {Text: "a"}, {Text: "<br>", Code: true}}
	got, err := SplitTarget("<br>а<", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Text != "<br>" || !got[0].Code || got[2].Text != "<" {
		t.Errorf("got %+v", got)
	}
}

func TestStateText(t *testing.T) {
	for s := Untranslated; s <= Approved; s++ {
		b, _ := s.MarshalText()
		var back State
		if err := back.UnmarshalText(b); err != nil || back != s {
			t.Errorf("%v -> %s -> %v %v", s, b, back, err)
		}
	}
	if FromXLIFFState("needs-review-translation") != MT || FromXLIFFState("final") != Approved {
		t.Error("xliff state mapping")
	}
}
