package mobile

import (
	"context"

	"github.com/jmbustos1/remote-dev-access/core/punch"
	"github.com/jmbustos1/remote-dev-access/core/stun"
)

// PunchListener lo implementa Kotlin para seguir una sesión de hole punching.
//
// kind: public | punching | inbound | connected | rtt | message | lost | failed
// addr: dirección asociada (puede ir vacía), rttMs: latencia (0 si no aplica),
// text: mensaje recibido o motivo de la falla.
type PunchListener interface {
	OnPunchEvent(kind, addr string, rttMs int64, text string)
}

// PunchSession envuelve punch.Session para gomobile.
type PunchSession struct {
	s *punch.Session
}

// OpenPunchSession abre el socket y averigua la dirección pública (bloquea unos
// cientos de ms: llamarlo fuera del hilo de UI).
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

func (p *PunchSession) PublicAddr() string        { return p.s.Public().String() }
func (p *PunchSession) LocalAddr() string         { return p.s.Local().String() }
func (p *PunchSession) Connect(peer string) error { return p.s.Connect(peer) }
func (p *PunchSession) Send(text string) error    { return p.s.Send(text) }
func (p *PunchSession) Close()                    { p.s.Close() }
