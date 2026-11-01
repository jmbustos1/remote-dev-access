package mobile

import (
	"context"
	"strings"

	"github.com/jmbustos1/remote-dev-access/core/punch"
	"github.com/jmbustos1/remote-dev-access/core/stun"
)

// PunchListener lo implementa Kotlin para seguir una sesión de hole punching.
//
// kind: public | punching | inbound | connected | rtt | message | lost | failed
// addr: dirección asociada (puede ir vacía), rttMs: latencia (0 si no aplica),
// text: candidatos (public), mensaje recibido o motivo de la falla.
type PunchListener interface {
	OnPunchEvent(kind, addr string, rttMs int64, text string)
}

// PunchSession envuelve punch.Session para gomobile.
type PunchSession struct {
	s *punch.Session
}

// OpenPunchSession abre el socket y reúne los candidatos (bloquea ~1 s: llamarlo
// fuera del hilo de UI). Sin UPnP: en datos móviles no hay router al que pedirle.
func OpenPunchSession(l PunchListener) (*PunchSession, error) {
	s, err := punch.Open(context.Background(), punch.Config{
		STUNServers: stun.DefaultServers,
		OnEvent: func(e punch.Event) {
			addr := ""
			if e.Addr.IsValid() {
				addr = e.Addr.String()
			}
			l.OnPunchEvent(e.Kind.String(), addr, e.RTT.Milliseconds(), e.Text)
		},
	})
	if err != nil {
		return nil, err
	}
	return &PunchSession{s: s}, nil
}

// Candidates es la línea para copiarle al otro lado.
func (p *PunchSession) Candidates() string { return p.s.CandidatesString() }

// CandidatesDetail es una línea por candidato: "tipo dirección".
func (p *PunchSession) CandidatesDetail() string {
	var b strings.Builder
	for _, c := range p.s.Candidates() {
		b.WriteString(c.Kind + " " + c.Addr.String() + "\n")
	}
	return strings.TrimSpace(b.String())
}

// Connect recibe la línea de candidatos del otro lado.
func (p *PunchSession) Connect(candidates string) error { return p.s.Connect(candidates) }
func (p *PunchSession) Send(text string) error          { return p.s.Send(text) }
func (p *PunchSession) Close()                          { p.s.Close() }
