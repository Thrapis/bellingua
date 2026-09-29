package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
)

// newLogger returns a logger that prints compact one-line records:
//
//	15:04:05  message  key=value key=value
//
// The level is shown only for warnings and errors.
func newLogger(level string) *slog.Logger {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	return slog.New(&cliHandler{mu: &sync.Mutex{}, w: os.Stderr, level: lv})
}

type cliHandler struct {
	mu    *sync.Mutex
	w     io.Writer
	level slog.Level
	attrs []slog.Attr
}

func (h *cliHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *cliHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("15:04:05"))
	b.WriteString("  ")
	if r.Level >= slog.LevelWarn {
		b.WriteString(strings.ToLower(r.Level.String()))
		b.WriteString(": ")
	}
	b.WriteString(r.Message)

	write := func(a slog.Attr) bool {
		v := a.Value.String()
		if strings.ContainsAny(v, " \t") {
			b.WriteString("  " + a.Key + "=" + strconv.Quote(v))
		} else {
			b.WriteString("  " + a.Key + "=" + v)
		}
		return true
	}
	for _, a := range h.attrs {
		write(a)
	}
	r.Attrs(write)
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *cliHandler) WithAttrs(as []slog.Attr) slog.Handler {
	n := *h
	n.attrs = append(append([]slog.Attr(nil), h.attrs...), as...)
	return &n
}

func (h *cliHandler) WithGroup(string) slog.Handler { return h }
