// Package logx centraliza los logs del core. Cada evento lleva una "etapa"
// (signaling, stun, ice, punch, relay, ssh) para poder filtrarlos después,
// por ejemplo en la pestaña de Logs de la APK.
package logx

import (
	"io"
	"log/slog"
	"os"
)

// Etapas de una conexión.
const (
	StageSignaling = "signaling"
	StageSTUN      = "stun"
	StageICE       = "ice"
	StagePunch     = "punch"
	StageRelay     = "relay"
	StageSSH       = "ssh"
)

var root = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

// Setup reconfigura el logger raíz. verbose=true habilita nivel debug.
func Setup(w io.Writer, verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	root = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

// For devuelve un logger etiquetado con la etapa dada.
func For(stage string) *slog.Logger {
	return root.With("stage", stage)
}
