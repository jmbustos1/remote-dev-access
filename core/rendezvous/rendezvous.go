// Package rendezvous junta señalización y hole punching: reemplaza el copiar y
// pegar de direcciones de la fase 2.
//
//	celular                         señalización                     PC (agente)
//	   │ abre sesión punch (STUN…)        │                               │
//	   │── offer{session, candidatos} ──►│── offer ─────────────────────►│ abre sesión punch (+UPnP)
//	   │◄─────────────────── answer ─────│◄── answer{session, candidatos}│ Connect(candidatos del celular)
//	   │ Connect(candidatos del PC)       │                               │
//	   │◄═════════════ hole punching / canal directo ════════════════════►│
package rendezvous

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/logx"
	"github.com/jmbustos1/remote-dev-access/core/punch"
	"github.com/jmbustos1/remote-dev-access/core/signaling"
	"github.com/jmbustos1/remote-dev-access/core/stun"
)

// Tiempos máximos de cada etapa.
var (
	AnswerTimeout  = 15 * time.Second
	ConnectTimeout = 30 * time.Second
)

// message viaja dentro de "data" de un signal.
type message struct {
	Kind       string `json:"kind"` // offer | answer
	Session    string `json:"session"`
	Candidates string `json:"candidates"`
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Lado PC

// ListenerConfig configura el agente del PC.
type ListenerConfig struct {
	Signaling signaling.ClientConfig
	UPnP      bool
	// STUNServers por defecto stun.DefaultServers (vacío en tests: sin STUN).
	STUNServers []string
	// OnEvent recibe los eventos de cada sesión. s puede ser nil en el primer
	// evento (EvPublic llega antes de que Open devuelva la sesión).
	OnEvent func(from string, s *punch.Session, e punch.Event)
}

// Listener mantiene al PC conectado a la señalización y atiende offers.
type Listener struct {
	cfg    ListenerConfig
	client *signaling.Client

	mu       sync.Mutex
	sessions map[string]*punch.Session // por dispositivo remoto: una sesión activa cada uno
}

// Listen arranca el agente. Close lo detiene y cierra las sesiones.
func Listen(cfg ListenerConfig) *Listener {
	l := &Listener{cfg: cfg, sessions: map[string]*punch.Session{}}
	sc := cfg.Signaling
	userMsg := sc.OnMessage
	sc.OnMessage = func(in signaling.Incoming) {
		if in.Type == "signal" {
			go l.handle(in)
		}
		if userMsg != nil {
			userMsg(in)
		}
	}
	l.client = signaling.Start(sc)
	return l
}

func (l *Listener) Close() {
	l.client.Close()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.sessions {
		s.Close()
	}
}

func (l *Listener) handle(in signaling.Incoming) {
	log := logx.For(logx.StageSignaling)
	var m message
	if err := json.Unmarshal(in.Data, &m); err != nil || m.Kind != "offer" {
		return
	}
	log.Info("offer recibida", "from", in.From, "session", m.Session)

	var holder struct {
		sync.Mutex
		s *punch.Session
	}
	servers := l.cfg.STUNServers
	if servers == nil {
		servers = stun.DefaultServers
	}
	s, err := punch.Open(context.Background(), punch.Config{
		STUNServers: servers,
		UPnP:        l.cfg.UPnP,
		OnEvent: func(e punch.Event) {
			if l.cfg.OnEvent != nil {
				holder.Lock()
				cur := holder.s
				holder.Unlock()
				l.cfg.OnEvent(in.From, cur, e)
			}
		},
	})
	if err != nil {
		log.Warn("no se pudo abrir la sesión para la offer", "from", in.From, "err", err)
		return
	}
	holder.Lock()
	holder.s = s
	holder.Unlock()

	l.mu.Lock()
	if old := l.sessions[in.From]; old != nil {
		old.Close() // reconexión del mismo dispositivo: la sesión nueva reemplaza a la vieja
	}
	l.sessions[in.From] = s
	l.mu.Unlock()

	if err := l.client.Signal(in.From, message{Kind: "answer", Session: m.Session, Candidates: s.CandidatesString()}); err != nil {
		log.Warn("no se pudo enviar la answer", "to", in.From, "err", err)
		return
	}
	if err := s.Connect(m.Candidates); err != nil {
		log.Warn("candidatos inválidos en la offer", "from", in.From, "err", err)
	}
}

// ---------------------------------------------------------------------------
// Lado celular / cliente

// DialerConfig configura el lado que inicia la conexión.
type DialerConfig struct {
	Signaling   signaling.ClientConfig
	UPnP        bool
	STUNServers []string // por defecto stun.DefaultServers
}

// Dialer mantiene una conexión de señalización para listar PCs y conectarse.
type Dialer struct {
	cfg    DialerConfig
	client *signaling.Client

	mu      sync.Mutex
	answers map[string]chan message
	lists   []chan []signaling.DeviceStatus
	errs    []chan string
}

// NewDialer arranca la señalización del lado cliente.
func NewDialer(cfg DialerConfig) *Dialer {
	d := &Dialer{cfg: cfg, answers: map[string]chan message{}}
	sc := cfg.Signaling
	userMsg := sc.OnMessage
	sc.OnMessage = func(in signaling.Incoming) {
		d.route(in)
		if userMsg != nil {
			userMsg(in)
		}
	}
	d.client = signaling.Start(sc)
	return d
}

func (d *Dialer) Close() { d.client.Close() }

func (d *Dialer) route(in signaling.Incoming) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch in.Type {
	case "devices":
		for _, ch := range d.lists {
			ch <- in.Devices
		}
		d.lists = nil
	case "signal":
		var m message
		if json.Unmarshal(in.Data, &m) == nil && m.Kind == "answer" {
			if ch := d.answers[m.Session]; ch != nil {
				ch <- m
			}
		}
	case "error":
		for _, ch := range d.errs {
			select {
			case ch <- in.Error:
			default:
			}
		}
	}
}

// Devices pide la lista de dispositivos registrados y su estado.
func (d *Dialer) Devices(ctx context.Context) ([]signaling.DeviceStatus, error) {
	if err := d.client.WaitConnected(ctx); err != nil {
		return nil, err
	}
	ch := make(chan []signaling.DeviceStatus, 1)
	d.mu.Lock()
	d.lists = append(d.lists, ch)
	d.mu.Unlock()
	if err := d.client.List(); err != nil {
		return nil, err
	}
	select {
	case devs := <-ch:
		return devs, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Connect abre una sesión de punching con pc usando la señalización y espera a
// que haya ida y vuelta. onEvent (opcional) recibe todos los eventos de la sesión.
func (d *Dialer) Connect(ctx context.Context, pc string, onEvent func(punch.Event)) (*punch.Session, error) {
	log := logx.For(logx.StageSignaling)
	if err := d.client.WaitConnected(ctx); err != nil {
		return nil, err
	}

	connected := make(chan struct{}, 1)
	servers := d.cfg.STUNServers
	if servers == nil {
		servers = stun.DefaultServers
	}
	s, err := punch.Open(ctx, punch.Config{
		STUNServers: servers,
		UPnP:        d.cfg.UPnP,
		OnEvent: func(e punch.Event) {
			if e.Kind == punch.EvConnected {
				select {
				case connected <- struct{}{}:
				default:
				}
			}
			if onEvent != nil {
				onEvent(e)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*punch.Session, error) { s.Close(); return nil, err }

	id := newID()
	answer := make(chan message, 1)
	errs := make(chan string, 1)
	d.mu.Lock()
	d.answers[id] = answer
	d.errs = append(d.errs, errs)
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.answers, id)
		for i, ch := range d.errs {
			if ch == errs {
				d.errs = append(d.errs[:i], d.errs[i+1:]...)
				break
			}
		}
		d.mu.Unlock()
	}()

	log.Info("enviando offer", "to", pc, "session", id)
	if err := d.client.Signal(pc, message{Kind: "offer", Session: id, Candidates: s.CandidatesString()}); err != nil {
		return fail(err)
	}

	var m message
	select {
	case m = <-answer:
		log.Info("answer recibida", "from", pc, "candidatos", m.Candidates)
	case e := <-errs:
		return fail(errors.New(e))
	case <-time.After(AnswerTimeout):
		return fail(fmt.Errorf("%s no respondió la offer en %s", pc, AnswerTimeout))
	case <-ctx.Done():
		return fail(ctx.Err())
	}
	if err := s.Connect(m.Candidates); err != nil {
		return fail(err)
	}

	select {
	case <-connected:
		return s, nil
	case <-time.After(ConnectTimeout):
		return fail(fmt.Errorf("el hole punching con %s no conectó en %s", pc, ConnectTimeout))
	case <-ctx.Done():
		return fail(ctx.Err())
	}
}
