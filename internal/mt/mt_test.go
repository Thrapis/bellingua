package mt

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Thrapis/bellingua/internal/model"
)

// fake maps whole requests to answers and records what it was sent.
type fake struct {
	answers map[string]string
	sent    []string
	fail    bool
}

func (f *fake) Name() string { return "fake" }
func (f *fake) Translate(_ context.Context, text, _, _ string) (string, error) {
	f.sent = append(f.sent, text)
	if f.fail {
		return "", errors.New("down")
	}
	if a, ok := f.answers[text]; ok {
		return a, nil
	}
	return "?" + text, nil
}

func tx(s string) model.Piece { return model.Piece{Text: s} }
func cd(s string) model.Piece { return model.Piece{Text: s, Code: true} }

func TestMaskedTranslation(t *testing.T) {
	f := &fake{answers: map[string]string{"Нажмите §0§, чтобы §1§бежать": "Націсніце §0§, каб §1§бегчы"}}
	src := []model.Piece{cd("<b>"), tx("Нажмите "), cd("{KEY}"), tx(", чтобы "), cd("<i>"), tx("бежать"), cd("</>")}
	r, err := Chain{f}.TranslatePieces(context.Background(), src, "ru", "be")
	if err != nil {
		t.Fatal(err)
	}
	if got := model.Plain(r.Target); got != "<b>Націсніце {KEY}, каб <i>бегчы</>" {
		t.Errorf("got %q (sent %q)", got, f.sent)
	}
	if _, err := model.SplitTarget(model.Plain(r.Target), src); err != nil {
		t.Errorf("codes lost: %v", err)
	}
}

func TestBrokenSentinelsFallBackToFragments(t *testing.T) {
	f := &fake{answers: map[string]string{
		"Один §0§ два": "Адзін § 0 два",
		"Один":         "Адзін",
		"два":          "два",
	}}
	src := []model.Piece{tx("Один "), cd("{X}"), tx(" два")}
	r, err := Chain{f}.TranslatePieces(context.Background(), src, "ru", "be")
	if err != nil {
		t.Fatal(err)
	}
	if got := model.Plain(r.Target); got != "Адзін {X} два" {
		t.Errorf("got %q", got)
	}
}

func TestLineBreakSegments(t *testing.T) {
	f := &fake{}
	src := []model.Piece{tx("Первая строка"), cd("\n\n"), tx("Вторая"), cd("\n"), tx("123")}
	r, err := Chain{f}.TranslatePieces(context.Background(), src, "ru", "be")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 2 || f.sent[0] != "Первая строка" || f.sent[1] != "Вторая" {
		t.Errorf("sent %q", f.sent)
	}
	if got := model.Plain(r.Target); got != "?Первая строка\n\n?Вторая\n123" {
		t.Errorf("got %q", got)
	}
}

func TestChainFallbackAndCopy(t *testing.T) {
	down, up := &fake{fail: true}, &fake{}
	r, err := Chain{down, up}.TranslatePieces(context.Background(), []model.Piece{tx("Да")}, "ru", "be")
	if err != nil || model.Plain(r.Target) != "?Да" {
		t.Errorf("fallback: %+v %v", r, err)
	}
	r, _ = Chain{down}.TranslatePieces(context.Background(), []model.Piece{cd("{X}"), tx(" 42")}, "ru", "be")
	if r.Provider != "copy" || model.Plain(r.Target) != "{X} 42" {
		t.Errorf("letterless string: %+v", r)
	}
}

func TestLingvanexClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Query().Get("from") != "ru" || r.URL.Query().Get("to") != "be" {
			http.Error(w, "bad langs", 400)
			return
		}
		io.WriteString(w, strings.ToUpper(string(body)))
	}))
	defer srv.Close()
	got, err := NewLingvanex(srv.URL, 0).Translate(context.Background(), "прывітанне", "ru", "be")
	if err != nil || got != "ПРЫВІТАННЕ" {
		t.Errorf("got %q %v", got, err)
	}
}
