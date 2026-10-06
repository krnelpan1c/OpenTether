package mobile

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"strings"
)

// tlsIdentity hides crypto/tls from gomobile's exported surface.
type tlsIdentity struct{ tls.Certificate }

// logHandler forwards slog records to Platform.Log (and so to logcat and
// the in-app log).
type logHandler struct {
	p     Platform
	attrs []slog.Attr
}

func newLogger(p Platform) *slog.Logger { return slog.New(&logHandler{p: p}) }

func (h *logHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *logHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	write := func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
		return true
	}
	for _, a := range h.attrs {
		write(a)
	}
	r.Attrs(write)
	h.p.Log(int32(r.Level), b.String())
	return nil
}

func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &logHandler{p: h.p, attrs: append(append([]slog.Attr(nil), h.attrs...), attrs...)}
}

func (h *logHandler) WithGroup(string) slog.Handler { return h }
