package logx

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestSinkReceivesStageAndAttrs(t *testing.T) {
	var got []Event
	var text bytes.Buffer
	Setup(&text, false, func(e Event) { got = append(got, e) })
	defer Setup(nil, false)

	log := For(StageSTUN)
	log.Debug("oculto") // nivel info: no debe llegar
	log.With("server", "google").Info("respuesta", "mapped", "1.2.3.4:5")

	if len(got) != 1 {
		t.Fatalf("eventos = %d, quiero 1", len(got))
	}
	e := got[0]
	if e.Level != "INFO" || e.Stage != StageSTUN || e.Message != "respuesta" {
		t.Errorf("evento inesperado: %+v", e)
	}
	if e.Attrs != "server=google mapped=1.2.3.4:5" {
		t.Errorf("attrs = %q", e.Attrs)
	}
	if !strings.Contains(text.String(), "stage=stun") {
		t.Errorf("la salida de texto no tiene la etapa: %q", text.String())
	}
}

// Un logger obtenido ANTES de Setup tiene que obedecer al Setup posterior
// (es lo que hace rda ssh al abrir la terminal: silenciar los logs). Antes, las
// goroutines de larga vida seguían escribiendo encima de tmux.
func TestLoggerFollowsLaterSetup(t *testing.T) {
	var before bytes.Buffer
	Setup(&before, false)
	defer Setup(nil, false)
	log := For(StagePunch) // como el ciclo de la señalización o el keepalive

	log.Info("uno")
	Setup(io.Discard, false)
	log.Warn("dos: no debería verse")

	var after bytes.Buffer
	Setup(&after, true)
	log.Debug("tres", "k", "v")

	if !strings.Contains(before.String(), "uno") || strings.Contains(before.String(), "dos") {
		t.Errorf("antes: %q", before.String())
	}
	if got := after.String(); !strings.Contains(got, "tres") || !strings.Contains(got, "stage=punch") || !strings.Contains(got, "k=v") {
		t.Errorf("después: %q", got)
	}
}
