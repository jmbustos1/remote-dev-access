// Package logx centraliza los logs del core. Cada evento lleva una "etapa"
// (signaling, stun, ice, punch, relay, ssh) para poder filtrarlos después,
// por ejemplo en la pestaña de Logs de la APK.
package logx

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// Etapas de una conexión.
const (
	StageSignaling = "signaling"
	StageSTUN      = "stun"
	StageICE       = "ice"
	StagePunch     = "punch"
	StageRelay     = "relay"
	StageTunnel    = "tunnel"
	StageSSH       = "ssh"
)

// Event es un log ya aplanado, listo para mostrarse en otra UI.
type Event struct {
	Level   string // DEBUG, INFO, WARN, ERROR
	Stage   string
	Message string
	Attrs   string // "clave=valor clave=valor"
}

// Sink recibe cada evento además de la salida de texto (lo usa la APK).
type Sink func(Event)

var (
	mu   sync.RWMutex
	root = newLogger(os.Stderr, slog.LevelInfo, nil)
)

// Setup reconfigura el logger raíz. verbose=true habilita nivel debug.
// sink puede ser nil; w puede ser nil para no escribir texto.
func Setup(w io.Writer, verbose bool, sink ...Sink) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	var s Sink
	if len(sink) > 0 {
		s = sink[0]
	}
	mu.Lock()
	root = newLogger(w, level, s)
	mu.Unlock()
}

// For devuelve un logger etiquetado con la etapa dada.
func For(stage string) *slog.Logger {
	mu.RLock()
	defer mu.RUnlock()
	return root.With("stage", stage)
}

func newLogger(w io.Writer, level slog.Level, sink Sink) *slog.Logger {
	var text slog.Handler
	if w != nil {
		text = slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	}
	return slog.New(&handler{text: text, sink: sink, level: level})
}

// handler reparte cada registro entre la salida de texto y el sink.
type handler struct {
	text  slog.Handler
	sink  Sink
	level slog.Level
	attrs []slog.Attr
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	if h.sink != nil {
		ev := Event{Level: r.Level.String(), Message: r.Message}
		var b strings.Builder
		add := func(a slog.Attr) bool {
			if a.Key == "stage" {
				ev.Stage = a.Value.String()
				return true
			}
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(a.Key + "=" + a.Value.String())
			return true
		}
		for _, a := range h.attrs {
			add(a)
		}
		r.Attrs(add)
		ev.Attrs = b.String()
		h.sink(ev)
	}
	if h.text != nil {
		return h.text.Handle(ctx, r)
	}
	return nil
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	n := *h
	n.attrs = append(append([]slog.Attr{}, h.attrs...), as...)
	if h.text != nil {
		n.text = h.text.WithAttrs(as)
	}
	return &n
}

func (h *handler) WithGroup(name string) slog.Handler {
	n := *h
	if h.text != nil {
		n.text = h.text.WithGroup(name)
	}
	return &n
}
