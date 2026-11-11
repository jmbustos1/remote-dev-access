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
	"cmp"
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
	// ListenerFirstShot: cuánto espera el agente, tras mandar la answer, antes
	// de su primer PING (salvo que el cliente dispare antes). El cliente dispara
	// apenas recibe la answer; así su NAT anota la conversación primero (ver
	// punch.Session.ConnectAfter). Medido: la answer tarda ~0,1–0,4 s en llegar.
	ListenerFirstShot = time.Second
	// FirstAttempt: si el intento normal (el cliente dispara primero) no conecta
	// en este tiempo tras la answer, el cliente reintenta con sockets nuevos y el
	// orden invertido: el agente dispara al tiro y el cliente espera DialerHold.
	// Cubre al agente detrás de un NAT de Linux (ver docs/nat-traversal.md §9).
	FirstAttempt = 5 * time.Second
	DialerHold   = time.Second
	// SessionIdleClose: el agente cierra una sesión sin noticias del otro lado
	// por este tiempo (más que el MaxIdleTimeout de QUIC, 3 min, para no cortar
	// una conexión que QUIC todavía considera viva).
	SessionIdleClose = 4 * time.Minute
)

// Orden de los disparos (campo "order" de la offer).
const (
	OrderDialerFirst   = ""               // por defecto: el cliente dispara primero
	OrderListenerFirst = "listener-first" // reintento: el agente dispara primero
)

// message viaja dentro de "data" de un signal.
type message struct {
	Kind       string `json:"kind"` // offer | answer
	Session    string `json:"session"`
	Candidates string `json:"candidates"`
	Order      string `json:"order,omitempty"` // solo en la offer: ver OrderListenerFirst
	// NAT de quien envía (punch.NATEIM / punch.NATSym), en offer y answer. Con uno
	// simétrico y el otro no, se suma el camino iterativo (ver iterRole).
	NAT string `json:"nat,omitempty"`
	// Sweep (kind "sweep"): el lado simétrico ya abrió sus sockets y le dice al
	// otro dónde barrer.
	Sweep *punch.SweepInfo `json:"sweep,omitempty"`
}

// Papel de un lado en el camino iterativo (docs/nat-traversal.md §4.3).
const (
	roleNone  = ""      // no aplica: el punching normal basta, o el otro lado es un agente antiguo
	roleSpray = "spray" // somos el simétrico: abrimos muchos sockets
	roleSweep = "sweep" // el simétrico es el otro: barremos sus puertos
	roleRelay = "relay" // los dos simétricos: solo un relay (fase 5e) o un puerto reenviado
)

// iterRole decide el papel según el tipo de NAT de cada lado. No depende de
// quién llama: el simétrico puede ser el agente.
func iterRole(own, peer string) string {
	switch {
	case own == punch.NATSym && peer == punch.NATSym:
		return roleRelay
	case own == punch.NATSym && peer == punch.NATEIM:
		return roleSpray
	case own == punch.NATEIM && peer == punch.NATSym:
		return roleSweep
	}
	return roleNone
}

// spray abre los sockets del lado simétrico y le avisa al otro dónde barrer.
func spray(c *signaling.Client, to, session string, s *punch.Session, candidates string) {
	log := logx.For(logx.StagePunch)
	info, err := s.Spray(candidates, punch.SpraySockets)
	if err != nil {
		log.Warn("camino iterativo: no se pudo iniciar", "err", err)
		return
	}
	if err := c.Signal(to, message{Kind: "sweep", Session: session, Sweep: &info}); err != nil {
		log.Warn("camino iterativo: no se pudo avisar al otro lado", "to", to, "err", err)
	}
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
	// Port fijo del socket UDP (reenvío de puerto manual en el router); 0 = al azar.
	Port uint16
	// CloseIdle: cerrar la sesión de un cliente sin noticias por este tiempo
	// (0 = SessionIdleClose).
	CloseIdle time.Duration
	// ListenUDP abre los sockets (nil = reales); para tests con natsim.
	ListenUDP func(port uint16) (punch.Conn, error)
	// STUNServers por defecto stun.DefaultServers (vacío en tests: sin STUN).
	STUNServers []string
	// OnEvent recibe los eventos de cada sesión. s puede ser nil en el primer
	// evento (EvPublic llega antes de que Open devuelva la sesión).
	OnEvent func(from string, s *punch.Session, e punch.Event)
	// OnSession se llama (en su propia goroutine) con cada sesión nueva, apenas
	// se envía la answer: el agente monta ahí el canal QUIC + SSH.
	OnSession func(from string, s *punch.Session)
}

// Listener mantiene al PC conectado a la señalización y atiende offers.
type Listener struct {
	cfg    ListenerConfig
	client *signaling.Client

	mu       sync.Mutex
	sessions map[string]*punch.Session // por dispositivo remoto: una sesión activa cada uno
	offers   map[string]message        // la offer de esa sesión (para cuando llegue su "sweep")
}

// Listen arranca el agente. Close lo detiene y cierra las sesiones.
func Listen(cfg ListenerConfig) *Listener {
	l := &Listener{cfg: cfg, sessions: map[string]*punch.Session{}, offers: map[string]message{}}
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
	if err := json.Unmarshal(in.Data, &m); err != nil {
		return
	}
	if m.Kind == "sweep" && m.Sweep != nil {
		l.mu.Lock()
		s, offer := l.sessions[in.From], l.offers[in.From]
		l.mu.Unlock()
		if s == nil || offer.Session != m.Session {
			return
		}
		if err := s.Sweep(offer.Candidates, *m.Sweep); err != nil {
			log.Warn("barrido rechazado", "from", in.From, "err", err)
		}
		return
	}
	if m.Kind != "offer" {
		return
	}
	log.Info("offer recibida", "from", in.From, "session", m.Session)

	var holder struct {
		sync.Mutex
		s *punch.Session
	}
	// Reconexión del mismo dispositivo: la sesión vieja se cierra antes de abrir
	// la nueva, para que la nueva pueda quedarse con el puerto fijo.
	l.mu.Lock()
	if old := l.sessions[in.From]; old != nil {
		old.Close()
		delete(l.sessions, in.From)
	}
	l.mu.Unlock()

	servers := l.cfg.STUNServers
	if servers == nil {
		servers = stun.DefaultServers
	}
	s, err := punch.Open(context.Background(), punch.Config{
		STUNServers: servers,
		UPnP:        l.cfg.UPnP,
		Port:        l.cfg.Port,
		CloseIdle:   cmp.Or(l.cfg.CloseIdle, SessionIdleClose),
		ListenUDP:   l.cfg.ListenUDP,
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
		old.Close() // llegó otra offer del mismo dispositivo mientras abríamos esta
	}
	l.sessions[in.From] = s
	l.offers[in.From] = m
	l.mu.Unlock()
	go func() { // al cerrarse (p. ej. abandonada), sacarla del mapa
		<-s.Done()
		l.mu.Lock()
		if l.sessions[in.From] == s {
			delete(l.sessions, in.From)
			delete(l.offers, in.From)
		}
		l.mu.Unlock()
	}()

	if err := l.client.Signal(in.From, message{Kind: "answer", Session: m.Session, Candidates: s.CandidatesString(), NAT: s.NAT()}); err != nil {
		log.Warn("no se pudo enviar la answer", "to", in.From, "err", err)
		return
	}
	wait := ListenerFirstShot
	if m.Order == OrderListenerFirst {
		wait = 0
		log.Info("reintento con el orden invertido: disparo primero", "from", in.From)
	}
	role := iterRole(s.NAT(), m.NAT)
	if role == roleSpray {
		wait = 0 // el simétrico dispara primero siempre
	}
	if err := s.ConnectAfter(m.Candidates, wait); err != nil {
		log.Warn("candidatos inválidos en la offer", "from", in.From, "err", err)
	}
	switch role {
	case roleSpray:
		spray(l.client, in.From, m.Session, s, m.Candidates)
	case roleSweep:
		log.Info("el cliente tiene NAT simétrico: espero sus datos para barrer", "from", in.From)
	case roleRelay:
		log.Warn("los dos lados tienen NAT simétrico: solo conectará si hay un puerto reenviado", "from", in.From)
	}
	if l.cfg.OnSession != nil {
		go l.cfg.OnSession(in.From, s)
	}
}

// ---------------------------------------------------------------------------
// Lado celular / cliente

// DialerConfig configura el lado que inicia la conexión.
type DialerConfig struct {
	Signaling   signaling.ClientConfig
	UPnP        bool
	STUNServers []string // por defecto stun.DefaultServers
	// ListenUDP abre los sockets (nil = reales); para tests con natsim.
	ListenUDP func(port uint16) (punch.Conn, error)
}

// Dialer mantiene una conexión de señalización para listar PCs y conectarse.
type Dialer struct {
	cfg    DialerConfig
	client *signaling.Client

	mu      sync.Mutex
	answers map[string]chan message
	sweeps  map[string]func(punch.SweepInfo) // por sesión: qué hacer cuando el otro lado pide barrer
	lists   []chan []signaling.DeviceStatus
	errs    []chan string
}

// NewDialer arranca la señalización del lado cliente.
func NewDialer(cfg DialerConfig) *Dialer {
	d := &Dialer{cfg: cfg, answers: map[string]chan message{}, sweeps: map[string]func(punch.SweepInfo){}}
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
		if json.Unmarshal(in.Data, &m) != nil {
			return
		}
		switch {
		case m.Kind == "answer":
			if ch := d.answers[m.Session]; ch != nil {
				ch <- m
			}
		case m.Kind == "sweep" && m.Sweep != nil:
			if f := d.sweeps[m.Session]; f != nil {
				go f(*m.Sweep)
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
	if err := d.client.WaitConnected(ctx); err != nil {
		return nil, err
	}
	// Intento 1: el cliente dispara primero (el agente espera ListenerFirstShot).
	s, err := d.attempt(ctx, pc, OrderDialerFirst, FirstAttempt, onEvent)
	if err == nil || !errors.Is(err, errNoPunch) || ctx.Err() != nil {
		return s, err // errNoIter no se reintenta: ver su comentario
	}
	// Intento 2: sockets nuevos y el orden invertido.
	logx.For(logx.StagePunch).Info("el primer intento no conectó: reintento con el orden invertido", "pc", pc, "tras", FirstAttempt)
	return d.attempt(ctx, pc, OrderListenerFirst, ConnectTimeout, onEvent)
}

// errNoPunch: hubo answer pero el hole punching no conectó a tiempo (vale la
// pena reintentar con otro orden; un PC desconectado, no).
var errNoPunch = errors.New("el hole punching no conectó")

// errNoIter: un lado tiene NAT simétrico y tampoco conectó con el camino
// iterativo. Cambiar el orden de los disparos no lo arregla: no se reintenta.
var errNoIter = errors.New("un lado tiene NAT simétrico")

func (d *Dialer) attempt(ctx context.Context, pc, order string, punchTimeout time.Duration, onEvent func(punch.Event)) (*punch.Session, error) {
	log := logx.For(logx.StageSignaling)
	connected := make(chan struct{}, 1)
	servers := d.cfg.STUNServers
	if servers == nil {
		servers = stun.DefaultServers
	}
	s, err := punch.Open(ctx, punch.Config{
		STUNServers: servers,
		UPnP:        d.cfg.UPnP,
		ListenUDP:   d.cfg.ListenUDP,
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
		delete(d.sweeps, id)
		for i, ch := range d.errs {
			if ch == errs {
				d.errs = append(d.errs[:i], d.errs[i+1:]...)
				break
			}
		}
		d.mu.Unlock()
	}()

	log.Info("enviando offer", "to", pc, "session", id, "order", order)
	if err := d.client.Signal(pc, message{Kind: "offer", Session: id, Candidates: s.CandidatesString(), Order: order, NAT: s.NAT()}); err != nil {
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
	hold := time.Duration(0)
	if order == OrderListenerFirst {
		hold = DialerHold
	}
	role := iterRole(s.NAT(), m.NAT)
	if role == roleSpray {
		hold = 0 // el simétrico dispara primero siempre
	}
	if role == roleSweep {
		d.mu.Lock()
		d.sweeps[id] = func(info punch.SweepInfo) {
			if err := s.Sweep(m.Candidates, info); err != nil {
				log.Warn("barrido rechazado", "from", pc, "err", err)
			}
		}
		d.mu.Unlock()
	}
	if err := s.ConnectAfter(m.Candidates, hold); err != nil {
		return fail(err)
	}
	if role == roleSpray {
		spray(d.client, pc, id, s, m.Candidates)
	}
	if role != roleNone {
		// Con un NAT simétrico el reintento con el orden invertido no aporta: se
		// le da a este intento todo el tiempo (el barrido son unos segundos).
		punchTimeout = ConnectTimeout
	}

	select {
	case <-connected:
		return s, nil
	case <-time.After(punchTimeout):
		switch role {
		case roleRelay:
			return fail(fmt.Errorf("%w: lo tienen este lado y %s; sin un puerto reenviado solo conectaría un relay", errNoIter, pc))
		case roleSpray, roleSweep:
			return fail(fmt.Errorf("%w y el camino iterativo no acertó con %s en %s", errNoIter, pc, punchTimeout))
		}
		return fail(fmt.Errorf("%w con %s en %s", errNoPunch, pc, punchTimeout))
	case <-ctx.Done():
		return fail(ctx.Err())
	}
}
