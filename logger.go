package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ── ANSI colors ───────────────────────────────────────────────────────────────

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiGray   = "\033[90m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
	ansiCyan   = "\033[36m"
	ansiBlue   = "\033[34m"
)

// ── Pretty handler ────────────────────────────────────────────────────────────

type prettyHandler struct {
	w       io.Writer
	level   slog.Level
	noColor bool
	mu      sync.Mutex
}

func (h *prettyHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *prettyHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *prettyHandler) WithGroup(string) slog.Handler       { return h }

func (h *prettyHandler) Handle(_ context.Context, r slog.Record) error {
	c := func(code, s string) string {
		if h.noColor {
			return s
		}
		return code + s + ansiReset
	}

	ts := r.Time.Format("2006-01-02 15:04:05")

	var levelTag string
	switch r.Level {
	case slog.LevelDebug:
		levelTag = c(ansiGray, "DBG")
	case slog.LevelInfo:
		levelTag = c(ansiGreen+ansiBold, "INF")
	case slog.LevelWarn:
		levelTag = c(ansiYellow+ansiBold, "WRN")
	case slog.LevelError:
		levelTag = c(ansiRed+ansiBold, "ERR")
	default:
		levelTag = r.Level.String()[:3]
	}

	var b strings.Builder
	b.WriteString(c(ansiDim, ts))
	b.WriteByte(' ')
	b.WriteString(levelTag)
	b.WriteByte(' ')
	b.WriteString(r.Message)

	r.Attrs(func(a slog.Attr) bool {
		b.WriteByte(' ')
		b.WriteString(c(ansiCyan, a.Key))
		b.WriteByte('=')
		b.WriteString(fmt.Sprintf("%v", a.Value.Any()))
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

// ── Init ──────────────────────────────────────────────────────────────────────

// initLogger sets up the global slog logger.
// level: "debug" | "info" (default) | "warn" | "error"
func initLogger(level string) {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	noColor := os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb"

	h := &prettyHandler{w: os.Stdout, level: lvl, noColor: noColor}
	slog.SetDefault(slog.New(h))

	slog.Debug("logger initialised", "level", lvl.String())
}

// ── HTTP request logging middleware ───────────────────────────────────────────

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

// loggingMiddleware logs every HTTP request at INFO (4xx → WARN, 5xx → ERROR).
// WebSocket upgrades are logged at DEBUG to avoid noise.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		dur := time.Since(start).Round(time.Millisecond)

		// WebSocket handshake — debug only
		if r.Header.Get("Upgrade") == "websocket" {
			slog.Debug("ws connect", "remote", r.RemoteAddr)
			return
		}

		args := []any{
			"status", sw.status,
			"dur", dur.String(),
			"ip", r.RemoteAddr,
		}
		msg := r.Method + " " + r.URL.Path

		switch {
		case sw.status >= 500:
			slog.Error(msg, args...)
		case sw.status >= 400:
			slog.Warn(msg, args...)
		default:
			slog.Info(msg, args...)
		}
	})
}
