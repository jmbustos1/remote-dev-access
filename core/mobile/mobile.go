// Package mobile es la API que ve la APK. gomobile solo exporta tipos simples
// (string, int, bool, error, interfaces), así que los resultados viajan como JSON.
package mobile

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/logx"
	"github.com/jmbustos1/remote-dev-access/core/stun"
)

// LogListener lo implementa Kotlin para recibir cada log del core.
type LogListener interface {
	OnLog(level, stage, message, attrs string)
}

// SetLogListener conecta los logs del core con la app. verbose habilita debug.
func SetLogListener(l LogListener, verbose bool) {
	logx.Setup(nil, verbose, func(e logx.Event) {
		l.OnLog(e.Level, e.Stage, e.Message, e.Attrs)
	})
}

// Version del core (la fija el build).
var version = "dev"

func Version() string { return version }

type stunResult struct {
	Server string `json:"server"`
	Mapped string `json:"mapped,omitempty"`
	RTTms  int64  `json:"rtt_ms,omitempty"`
	Error  string `json:"error,omitempty"`
}

type stunReport struct {
	Local   string       `json:"local"`
	NAT     string       `json:"nat"`
	Results []stunResult `json:"results"`
}

// ProbeSTUN consulta los servidores STUN por defecto desde un mismo socket y
// devuelve el reporte en JSON. Bloquea: llamarlo fuera del hilo de UI.
func ProbeSTUN(timeoutMs int) (string, error) {
	rep, err := stun.Probe(context.Background(), stun.DefaultServers, 0, time.Duration(timeoutMs)*time.Millisecond)
	if err != nil {
		return "", err
	}
	out := stunReport{Local: rep.Local.String(), NAT: rep.Kind.String()}
	for _, r := range rep.Results {
		sr := stunResult{Server: r.Server}
		if r.Err != nil {
			sr.Error = r.Err.Error()
		} else {
			sr.Mapped = r.Mapped.String()
			sr.RTTms = r.RTT.Milliseconds()
		}
		out.Results = append(out.Results, sr)
	}
	b, err := json.Marshal(out)
	return string(b), err
}
