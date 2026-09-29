// Package mt translates units with machine translation providers while
// keeping every code intact.
//
// Codes are replaced by §N§ sentinels (the form CTranslate2 models preserve
// best), the text is translated in one request, and the codes are spliced
// back. When the provider mangles a
// sentinel, each text fragment is translated on its own instead. Strings are
// first split at codes that contain a line break, so a huge string becomes
// many small requests within the model's input limit.
package mt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Thrapis/bellingua/internal/config"
	"github.com/Thrapis/bellingua/internal/model"
)

// Provider translates plain text.
type Provider interface {
	Name() string
	Translate(ctx context.Context, text, from, to string) (string, error)
}

// Chain tries providers in order.
type Chain []Provider

// New builds the chain from config.
func New(cfg config.MT) (Chain, error) {
	var c Chain
	for _, p := range cfg.Providers {
		switch p.Name {
		case "lingvanex":
			c = append(c, NewLingvanex(p.URL, p.Timeout))
		default:
			return nil, fmt.Errorf("mt: unknown provider %q", p.Name)
		}
	}
	return c, nil
}

// Result is a machine translation of a unit.
type Result struct {
	Target   []model.Piece `json:"tgt"`
	Provider string        `json:"provider"`
}

// ErrNoProvider is returned when the chain is empty.
var ErrNoProvider = errors.New("mt: no provider configured")

// TranslatePieces translates a unit's source. The first provider that
// succeeds for the whole string wins.
func (c Chain) TranslatePieces(ctx context.Context, src []model.Piece, from, to string) (Result, error) {
	if len(c) == 0 {
		return Result{}, ErrNoProvider
	}
	if !hasLetters(src) {
		return Result{Target: clone(src), Provider: "copy"}, nil
	}
	var errs []error
	for _, p := range c {
		out, err := translate(ctx, p, src, from, to)
		if err == nil {
			return Result{Target: out, Provider: p.Name()}, nil
		}
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
	}
	return Result{}, errors.Join(errs...)
}

func translate(ctx context.Context, p Provider, src []model.Piece, from, to string) ([]model.Piece, error) {
	var out []model.Piece
	for _, seg := range segments(src) {
		if !seg.translatable {
			out = append(out, seg.pieces...)
			continue
		}
		tr, err := translateSegment(ctx, p, seg.pieces, from, to)
		if err != nil {
			return nil, err
		}
		out = append(out, tr...)
	}
	return merge(out), nil
}

type segment struct {
	pieces       []model.Piece
	translatable bool
}

// segments splits at codes holding a line break; those codes form their own
// untranslatable segments.
func segments(ps []model.Piece) []segment {
	var out []segment
	var cur []model.Piece
	flush := func() {
		if len(cur) > 0 {
			out = append(out, segment{cur, hasLetters(cur)})
			cur = nil
		}
	}
	for _, p := range ps {
		if p.Code && strings.ContainsAny(p.Text, "\r\n") {
			flush()
			out = append(out, segment{[]model.Piece{p}, false})
			continue
		}
		cur = append(cur, p)
	}
	flush()
	return out
}

// translateSegment masks codes, translates, unmasks; falls back per fragment.
func translateSegment(ctx context.Context, p Provider, ps []model.Piece, from, to string) ([]model.Piece, error) {
	masked, markers, lead, trail := mask(ps)
	if len(markers) == 0 {
		t, err := p.Translate(ctx, masked, from, to)
		if err != nil {
			return nil, err
		}
		return wrap(lead, []model.Piece{{Text: keepSpaces(masked, t)}}, trail), nil
	}
	t, err := p.Translate(ctx, masked, from, to)
	if err != nil {
		return nil, err
	}
	if sentinelsIntact(t, len(markers)) {
		return wrap(lead, unmask(t, markers), trail), nil
	}
	// Fragment fallback: translate every text piece on its own.
	out := make([]model.Piece, len(ps))
	for i, x := range ps {
		out[i] = x
		if x.Code || !hasLetters([]model.Piece{x}) {
			continue
		}
		t, err := p.Translate(ctx, strings.TrimSpace(x.Text), from, to)
		if err != nil {
			return nil, err
		}
		out[i].Text = keepSpaces(x.Text, t)
	}
	return out, nil
}

// mask replaces runs of codes with §i§. Codes at the very start and end are
// not sent at all (lead/trail), so the model sees less noise.
func mask(ps []model.Piece) (masked string, markers []string, lead, trail []model.Piece) {
	i, j := 0, len(ps)
	for i < j && ps[i].Code {
		i++
	}
	for j > i && ps[j-1].Code {
		j--
	}
	lead, trail = ps[:i], ps[j:]
	var b, pending strings.Builder
	flush := func() {
		if pending.Len() == 0 {
			return
		}
		fmt.Fprintf(&b, "§%d§", len(markers))
		markers = append(markers, pending.String())
		pending.Reset()
	}
	for _, p := range ps[i:j] {
		if p.Code {
			pending.WriteString(p.Text)
			continue
		}
		flush()
		b.WriteString(p.Text)
	}
	flush()
	return b.String(), markers, lead, trail
}

func wrap(lead, mid, trail []model.Piece) []model.Piece {
	out := make([]model.Piece, 0, len(lead)+len(mid)+len(trail))
	return append(append(append(out, lead...), mid...), trail...)
}

var sentinelRe = regexp.MustCompile(`§(\d+)§`)

// sentinelsIntact: exactly §0§…§(n-1)§ once each, and no stray "§".
func sentinelsIntact(s string, n int) bool {
	seen := make([]bool, n)
	for _, m := range sentinelRe.FindAllStringSubmatch(s, -1) {
		i, err := strconv.Atoi(m[1])
		if err != nil || i < 0 || i >= n || seen[i] {
			return false
		}
		seen[i] = true
	}
	for _, ok := range seen {
		if !ok {
			return false
		}
	}
	return strings.Count(s, "§") == 2*n
}

// unmask turns a translated masked string back into pieces. A marker may
// hold several adjacent codes, but SplitTarget-style consumers only need the
// text/code boundary, so each marker becomes one code piece.
func unmask(s string, markers []string) []model.Piece {
	var out []model.Piece
	last := 0
	for _, loc := range sentinelRe.FindAllStringSubmatchIndex(s, -1) {
		if loc[0] > last {
			out = append(out, model.Piece{Text: s[last:loc[0]]})
		}
		i, _ := strconv.Atoi(s[loc[2]:loc[3]])
		out = append(out, model.Piece{Text: markers[i], Code: true})
		last = loc[1]
	}
	if last < len(s) {
		out = append(out, model.Piece{Text: s[last:]})
	}
	return out
}

// keepSpaces restores the source's leading/trailing whitespace, which
// translators routinely strip.
func keepSpaces(src, tr string) string {
	lead := src[:len(src)-len(strings.TrimLeftFunc(src, unicode.IsSpace))]
	trail := src[len(strings.TrimRightFunc(src, unicode.IsSpace)):]
	return lead + strings.TrimSpace(tr) + trail
}

func hasLetters(ps []model.Piece) bool {
	for _, p := range ps {
		if p.Code {
			continue
		}
		for _, r := range p.Text {
			if unicode.IsLetter(r) {
				return true
			}
		}
	}
	return false
}

func clone(ps []model.Piece) []model.Piece { return append([]model.Piece(nil), ps...) }

// merge joins adjacent text pieces.
func merge(ps []model.Piece) []model.Piece {
	var out []model.Piece
	for _, p := range ps {
		if n := len(out); n > 0 && !p.Code && !out[n-1].Code {
			out[n-1].Text += p.Text
			continue
		}
		if !p.Code && p.Text == "" {
			continue
		}
		out = append(out, p)
	}
	if out == nil {
		out = []model.Piece{}
	}
	return out
}

// --- providers ---------------------------------------------------------------

// Lingvanex talks to the bundled local CTranslate2 server
// (third_party/lingvanex-server): POST ?from=&to= with the text as body,
// translation as the plain-text response.
type Lingvanex struct {
	base string
	http *http.Client
}

// NewLingvanex returns a client with a keep-alive transport.
func NewLingvanex(base string, timeout time.Duration) *Lingvanex {
	if base == "" {
		base = "http://127.0.0.1:8000"
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 16
	return &Lingvanex{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: timeout, Transport: tr}}
}

func (*Lingvanex) Name() string { return "lingvanex" }

func (l *Lingvanex) Translate(ctx context.Context, text, from, to string) (string, error) {
	u := l.base + "?from=" + url.QueryEscape(from) + "&to=" + url.QueryEscape(to)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(text))
	if err != nil {
		return "", err
	}
	res, err := l.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d: %s", res.StatusCode, body)
	}
	return string(body), nil
}
