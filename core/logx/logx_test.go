package logx

import (
	"bytes"
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
