// Package punch implementa el hole punching UDP "a mano" (fases 2 y 2b).
//
// Una Session es UN socket UDP (dual-stack IPv4/IPv6) que hace todo: descubre
// sus direcciones posibles ("candidatos": STUN IPv4, puerto UPnP, IPv6, red
// local), mantiene viva la puerta del NAT mientras esperamos, dispara paquetes a
// TODOS los candidatos del otro lado y se queda con el primero que responde.
// Es un ICE en miniatura.
//
// Protocolo (texto plano, para leerlo en los logs):
//
//	RDA1 PING <id> <seq>    "¿me escuchas?" (durante el punching y como keepalive)
//	RDA1 PONG <id> <seq>    respuesta a un PING, mismo seq → sirve para medir RTT
//	RDA1 MSG  <id> <texto>  mensaje de chat
//
// <id> identifica a la sesión que envía (solo informativo en los logs).
package punch

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/logx"
	"github.com/jmbustos1/remote-dev-access/core/netx"
	"github.com/jmbustos1/remote-dev-access/core/stun"
	"github.com/jmbustos1/remote-dev-access/core/upnp"
)

var magic = []byte("RDA1 ")

// Tiempos del protocolo.
var (
	PunchInterval     = 500 * time.Millisecond // ritmo de disparo mientras no hay conexión
	PunchTimeout      = 60 * time.Second       // disparo rápido; después sigue el keepalive
	KeepaliveInterval = 5 * time.Second        // PING lento (también tras fallar el punching)
	STUNRefresh       = 15 * time.Second       // refresco de la puerta mientras esperamos al otro
	LostAfter         = 20 * time.Second       // sin PONG por este tiempo → conexión perdida
)

// Tipos de candidato.
const (
	CandSTUN  = "stun"  // IPv4 pública según STUN
	CandUPnP  = "upnp"  // puerto reenviado por el router
	CandFixed = "fijo"  // puerto reenviado a mano en el router (Config.Port)
	CandIPv6  = "ipv6"  // IPv6 global (sin NAT)
	CandLocal = "local" // red local (mismo wifi)
)

// Candidate es una dirección por la que el otro lado podría alcanzarnos.
type Candidate struct {
	Kind string
	Addr netip.AddrPort
}

func (c Candidate) String() string { return c.Kind + "=" + c.Addr.String() }

// EventKind enumera lo que le pasa a una sesión.
type EventKind int

const (
	EvPublic    EventKind = iota // cambió nuestra lista de candidatos
	EvPunching                   // empezamos a disparar hacia el otro lado
	EvInbound                    // llegó el primer paquete del otro lado (una dirección abierta)
	EvConnected                  // ida y vuelta confirmada
	EvRTT                        // nueva medición de latencia
	EvMessage                    // mensaje de chat recibido
	EvLost                       // dejamos de recibir respuestas
	EvFailed                     // el disparo rápido no conectó (el keepalive sigue intentando)
)

func (k EventKind) String() string {
	return [...]string{"public", "punching", "inbound", "connected", "rtt", "message", "lost", "failed"}[k]
}

// Event se entrega al callback de la sesión.
type Event struct {
	Kind EventKind
	Addr netip.AddrPort // punching / inbound / connected
	RTT  time.Duration  // connected / rtt
	Text string         // public (candidatos) / message / failed
}

// Conn es lo que la sesión necesita de un socket UDP. *net.UDPConn lo cumple;
// los tests usan una red simulada (core/natsim).
type Conn interface {
	ReadFromUDPAddrPort(b []byte) (int, netip.AddrPort, error)
	WriteToUDPAddrPort(b []byte, addr netip.AddrPort) (int, error)
	LocalAddr() net.Addr
	Close() error
}

// Config de una sesión.
type Config struct {
	// ListenUDP abre los sockets (nil = sockets reales). Para tests con natsim.
	ListenUDP func(port uint16) (Conn, error)
	// STUNServers a probar en orden (se usan para IPv4 e IPv6). Vacío = sin STUN:
	// el único candidato es la dirección local (tests, misma red).
	STUNServers []string
	// UPnP: pedirle al router un puerto reenviado (para PCs en la casa).
	UPnP bool
	// Port fijo para el socket (0 = al azar). Sirve con un reenvío de puerto
	// manual en el router: se anuncia IP pública:Port aunque STUN vea otro.
	// Si está ocupado (otra sesión lo usa), se cae a un puerto al azar.
	Port uint16
	// CloseIdle cierra la sesión sola si en ese tiempo no llega nada del otro
	// lado (PING, PONG ni datos): un cliente que se fue, o un intento que nunca
	// conectó. 0 = nunca. El agente lo usa para no acumular sesiones muertas.
	CloseIdle time.Duration
	OnEvent   func(Event)
}

// Session es un socket UDP con estado de punching.
type Session struct {
	conn    Conn // el socket vigente: leer con sock() (el camino iterativo lo cambia)
	id      string
	cfg     Config
	closeCh chan struct{}
	once    sync.Once
	local   netip.AddrPort
	mapping *upnp.Mapping
	data    chan []byte // paquetes que no son STUN ni RDA1 (QUIC), ver PacketConn

	mu         sync.Mutex
	extras     []Conn // camino iterativo: sockets abiertos por Spray, hasta que uno gana
	nat        string // NATEIM / NATSym, o "" si no se pudo medir
	cands      []Candidate
	stunServer *net.UDPAddr // servidor IPv4 para refrescar la puerta
	pending    map[stun.TxID]chan []byte
	remote     []netip.AddrPort        // candidatos del otro lado
	peer       netip.AddrPort          // dirección elegida (la que respondió)
	seen       map[netip.AddrPort]bool // direcciones que nos han hablado en RDA1
	punching   bool
	looping    bool // hay un punchLoop corriendo (como mucho uno)
	holding    bool // ConnectAfter: esperando que el otro lado dispare primero
	inbound    bool
	connected  bool
	seq        uint64
	sentAt     map[uint64]time.Time
	lastPong   time.Time
	lastHeard  time.Time // último paquete del otro lado (o la apertura): ver Config.CloseIdle
}

// Open abre el socket y reúne nuestros candidatos.
func Open(ctx context.Context, cfg Config) (*Session, error) {
	conn, fixed, err := listen(cfg, cfg.Port)
	if err != nil {
		return nil, err
	}
	idb := make([]byte, 4)
	rand.Read(idb)
	la := conn.LocalAddr().(*net.UDPAddr)
	port := uint16(la.Port)
	s := &Session{
		conn:      conn,
		id:        hex.EncodeToString(idb),
		cfg:       cfg,
		closeCh:   make(chan struct{}),
		data:      make(chan []byte, 1024),
		pending:   map[stun.TxID]chan []byte{},
		sentAt:    map[uint64]time.Time{},
		seen:      map[netip.AddrPort]bool{},
		lastHeard: time.Now(),
		local:     netx.LocalAddr(port),
	}
	if ip, ok := netip.AddrFromSlice(la.IP); ok && !ip.IsUnspecified() {
		s.local = netip.AddrPortFrom(ip.Unmap(), port) // socket ya atado a una IP (natsim)
	}
	go s.readLoop(conn)

	log := logx.For(logx.StagePunch)
	log.Info("sesión abierta", "id", s.id, "local", s.local)
	if cfg.Port != 0 && !fixed {
		log.Warn("puerto fijo ocupado, uso uno al azar (sin el reenvío del router)", "port", cfg.Port)
	}

	if len(cfg.STUNServers) > 0 {
		pub4, err := s.discover(ctx, "ip4")
		if err != nil {
			s.Close()
			return nil, err
		}
		s.addCand(Candidate{CandSTUN, pub4})
		s.detectNAT(ctx, pub4)
		if fixed {
			// Con el reenvío manual, desde internet se entra por IP pública:Port
			// aunque el NAT haya mapeado la salida a otro puerto.
			s.addCand(Candidate{CandFixed, netip.AddrPortFrom(pub4.Addr(), cfg.Port)})
		}

		if cfg.UPnP {
			if m, err := upnp.MapUDP(ctx, port, "remote-dev-access", time.Hour); err != nil {
				log.Info("UPnP no disponible", "err", err)
			} else {
				s.mapping = m
				if m.External.Addr() != pub4.Addr() {
					log.Warn("la IP externa del router no es la pública: hay doble NAT y el puerto UPnP no servirá desde internet",
						"router", m.External.Addr(), "stun", pub4.Addr())
				}
				s.addCand(Candidate{CandUPnP, m.External})
			}
		}

		if pub6, err := s.discover(ctx, "ip6"); err != nil {
			log.Info("sin IPv6 pública en esta red", "err", err)
		} else {
			s.addCand(Candidate{CandIPv6, pub6})
		}
	}
	s.addCand(Candidate{CandLocal, s.local})

	log.Info("candidatos listos", "candidatos", s.CandidatesString())
	s.emit(Event{Kind: EvPublic, Text: s.CandidatesString()})
	go s.maintain()
	return s, nil
}

// listen abre el socket UDP (dual-stack si se puede) en port, o al azar si
// port es 0 o está ocupado. fixed indica si quedó en port.
func listen(cfg Config, port uint16) (conn Conn, fixed bool, err error) {
	open := func(p int) (Conn, error) {
		if cfg.ListenUDP != nil {
			return cfg.ListenUDP(uint16(p))
		}
		c, err := net.ListenUDP("udp", &net.UDPAddr{Port: p}) // IPv4 e IPv6 en el mismo puerto
		if err != nil {
			c, err = net.ListenUDP("udp4", &net.UDPAddr{Port: p})
		}
		if err != nil {
			return nil, err // sin esto, un *net.UDPConn nil quedaría como Conn no nil
		}
		return c, nil
	}
	if port != 0 {
		if conn, err = open(int(port)); err == nil {
			return conn, true, nil
		}
	}
	if conn, err = open(0); err != nil {
		return nil, false, fmt.Errorf("abriendo socket UDP: %w", err)
	}
	return conn, false, nil
}

func (s *Session) addCand(c Candidate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.cands {
		if x.Addr == c.Addr {
			// Misma dirección por dos vías (p. ej. el router reenvió el mismo
			// puerto que ya mapeaba el NAT): lo anotamos en el tipo.
			s.cands[i].Kind += "+" + c.Kind
			return
		}
	}
	s.cands = append(s.cands, c)
}

// Candidates devuelve nuestras direcciones posibles, en orden de preferencia.
func (s *Session) Candidates() []Candidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.cands)
}

// CandidatesString es lo que hay que pasarle al otro lado: direcciones separadas por espacio.
func (s *Session) CandidatesString() string {
	var parts []string
	for _, c := range s.Candidates() {
		parts = append(parts, c.Addr.String())
	}
	return strings.Join(parts, " ")
}

// Public es nuestra IPv4 vista desde internet (o la local si no hay STUN).
func (s *Session) Public() netip.AddrPort {
	c := s.Candidates()
	return c[0].Addr
}

// Local es la dirección dentro de la red local.
func (s *Session) Local() netip.AddrPort { return s.local }

// ParseCandidates acepta direcciones "ip:puerto" o "[ipv6]:puerto" separadas por
// espacios, comas o saltos de línea.
func ParseCandidates(text string) ([]netip.AddrPort, error) {
	var out []netip.AddrPort
	for _, f := range strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == ',' || r == '\n' || r == '\t' }) {
		ap, err := netip.ParseAddrPort(f)
		if err != nil {
			return nil, fmt.Errorf("dirección inválida %q (formato ip:puerto o [ipv6]:puerto)", f)
		}
		out = append(out, netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()))
	}
	if len(out) == 0 {
		return nil, errors.New("no hay direcciones")
	}
	return out, nil
}

// Connect empieza a disparar hacia todos los candidatos del otro lado. No bloquea.
func (s *Session) Connect(candidates string) error {
	return s.ConnectAfter(candidates, 0)
}

// ConnectAfter es Connect, pero el primer PING sale recién después de wait, o
// antes si llega un paquete del otro lado (señal de que su NAT ya nos abrió la
// puerta: se le responde al tiro).
//
// Para qué: hay NATs (el de Linux, p. ej. la zona Wi-Fi de Android) que, si les
// llega un paquete NUESTRO antes de que su equipo haya escrito, anotan esa
// conversación en su tabla; cuando su equipo nos escribe después, ya no pueden
// usar el mismo puerto y salen por otro, que nuestro NAT descarta: el punching
// queda trabado para siempre. Si el otro lado dispara primero, no pasa. El
// agente (que responde la offer) espera; el cliente, que suele estar "en la
// calle" detrás de esos NATs, dispara de inmediato. Ver docs/nat-traversal.md.
func (s *Session) ConnectAfter(candidates string, wait time.Duration) error {
	remote, err := ParseCandidates(candidates)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.punching || s.connected {
		// Ya hay un intento en curso (p. ej. el otro lado nos alcanzó antes de que
		// llegaran sus candidatos y lo adoptamos): sumamos los candidatos a ese intento.
		for _, r := range remote {
			if !slices.Contains(s.remote, r) {
				s.remote = append(s.remote, r)
			}
		}
		s.mu.Unlock()
		logx.For(logx.StagePunch).Info("candidatos sumados al intento en curso", "candidatos", candidates)
		return nil
	}
	s.remote, s.punching, s.holding = remote, true, wait > 0
	s.mu.Unlock()

	log := logx.For(logx.StagePunch)
	if wait > 0 {
		log.Info("candidatos del otro lado recibidos; espero a que dispare primero", "candidatos", candidates, "espera", wait)
	} else {
		log.Info("disparando hacia todos los candidatos del otro lado", "candidatos", candidates)
	}
	s.emit(Event{Kind: EvPunching, Addr: remote[0]})
	go s.punchLoop(wait)
	return nil
}

// waitFirstShot espera hasta wait, o hasta que llegue algo del otro lado o se
// cierre la sesión. Devuelve false si la sesión se cerró.
func (s *Session) waitFirstShot(wait time.Duration) bool {
	end := time.After(wait)
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	defer func() {
		s.mu.Lock()
		s.holding = false
		s.mu.Unlock()
	}()
	for {
		s.mu.Lock()
		heard := s.inbound || s.connected
		s.mu.Unlock()
		if heard {
			logx.For(logx.StagePunch).Info("el otro lado disparó primero: dejo de esperar")
			return true
		}
		select {
		case <-end:
			logx.For(logx.StagePunch).Info("disparando hacia todos los candidatos del otro lado (terminó la espera)")
			return true
		case <-s.closeCh:
			return false
		case <-t.C:
		}
	}
}

// Send manda un mensaje de chat. Requiere estar conectado.
func (s *Session) Send(text string) error {
	s.mu.Lock()
	peer, ok := s.peer, s.connected
	s.mu.Unlock()
	if !ok {
		return errors.New("todavía no hay conexión")
	}
	return s.write(peer, "MSG "+s.id+" "+text)
}

// Close cierra el socket, libera el puerto UPnP y detiene todo.
func (s *Session) Close() {
	s.once.Do(func() {
		close(s.closeCh)
		s.mu.Lock()
		socks := append([]Conn{s.conn}, s.extras...)
		s.extras = nil
		s.mu.Unlock()
		for _, c := range socks {
			c.Close()
		}
		if s.mapping != nil {
			s.mapping.Close()
		}
		logx.For(logx.StagePunch).Info("sesión cerrada", "id", s.id)
	})
}

// Done se cierra cuando la sesión termina (Close, o CloseIdle sin noticias).
func (s *Session) Done() <-chan struct{} { return s.closeCh }

func (s *Session) emit(e Event) {
	if s.cfg.OnEvent != nil {
		s.cfg.OnEvent(e)
	}
}

// --- STUN sobre el socket compartido ---------------------------------------

func (s *Session) discover(ctx context.Context, family string) (netip.AddrPort, error) {
	log := logx.For(logx.StageSTUN)
	var lastErr error
	for _, name := range s.cfg.STUNServers {
		addr, err := netx.ResolveUDP(ctx, name, family)
		if err != nil {
			lastErr = err
			continue
		}
		mapped, err := s.bind(addr, 2*time.Second, 2)
		if err != nil {
			lastErr = err
			log.Debug("servidor STUN sin respuesta", "server", name, "family", family, "err", err)
			continue
		}
		if family == "ip4" {
			s.mu.Lock()
			s.stunServer = addr
			s.mu.Unlock()
		}
		log.Info("dirección pública", "family", family, "public", mapped, "server", name)
		return mapped, nil
	}
	return netip.AddrPort{}, fmt.Errorf("ningún servidor STUN respondió por %s: %w", family, lastErr)
}

// sock es el socket vigente.
func (s *Session) sock() Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn
}

// Tipo de NAT que se anuncia al otro lado (ver Session.NAT).
const (
	NATEIM = "eim" // mismo IP:puerto público hacia cualquier destino
	NATSym = "sym" // cambia según el destino: el candidato STUN no le sirve al otro lado
)

// NAT dice cómo mapea nuestro NAT, medido al abrir la sesión: "" si no se pudo
// medir (sin STUN, o respondió un solo servidor).
func (s *Session) NAT() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nat
}

// detectNAT pregunta a un segundo servidor STUN, en otra IP, desde el mismo
// socket: si nos ve con otro IP:puerto que el primero, el mapeo depende del
// destino. Se mide en cada sesión: la misma red cambia de tipo durante el día
// (docs/experimentos.md, 2026-11-11).
func (s *Session) detectNAT(ctx context.Context, first netip.AddrPort) {
	s.mu.Lock()
	used := s.stunServer
	s.mu.Unlock()
	for _, name := range s.cfg.STUNServers {
		addr, err := netx.ResolveUDP(ctx, name, "ip4")
		if err != nil || addr.IP.Equal(used.IP) {
			continue
		}
		mapped, err := s.bind(addr, time.Second, 1)
		if err != nil {
			continue
		}
		kind := NATEIM
		if mapped != first {
			kind = NATSym
		}
		s.mu.Lock()
		s.nat = kind
		s.mu.Unlock()
		logx.For(logx.StageSTUN).Info("tipo de NAT", "nat", kind, "primero", first, "segundo", mapped, "server", name)
		return
	}
}

// bind manda un Binding Request por el socket vigente y espera su respuesta.
func (s *Session) bind(server *net.UDPAddr, timeout time.Duration, attempts int) (netip.AddrPort, error) {
	return s.bindOn(s.sock(), server, timeout, attempts)
}

// bindOn manda un Binding Request por conn y espera su respuesta, que llega por readLoop.
func (s *Session) bindOn(conn Conn, server *net.UDPAddr, timeout time.Duration, attempts int) (netip.AddrPort, error) {
	id, req, err := stun.NewBindingRequest()
	if err != nil {
		return netip.AddrPort{}, err
	}
	ch := make(chan []byte, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pending, id); s.mu.Unlock() }()
	to := netip.AddrPortFrom(server.AddrPort().Addr().Unmap(), uint16(server.Port))

	for i := 0; i < attempts; i++ {
		if _, err := conn.WriteToUDPAddrPort(req, to); err != nil {
			return netip.AddrPort{}, err
		}
		select {
		case resp := <-ch:
			ap, err := stun.ParseBindingResponse(resp, id)
			return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), err
		case <-time.After(timeout):
		case <-s.closeCh:
			return netip.AddrPort{}, net.ErrClosed
		}
	}
	return netip.AddrPort{}, fmt.Errorf("sin respuesta tras %d intentos", attempts)
}

// --- Lectura y despacho ----------------------------------------------------

// readLoop lee de un socket hasta que se cierra: hay uno por socket (el de la
// sesión y, en el camino iterativo, uno por cada socket de Spray).
func (s *Session) readLoop(conn Conn) {
	buf := make([]byte, 2048)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			select {
			case <-s.closeCh:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return // socket de Spray que no ganó
			}
			logx.For(logx.StagePunch).Warn("error leyendo socket", "err", err)
			continue
		}
		pkt := append([]byte(nil), buf[:n]...)
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		switch {
		case stun.IsMessage(pkt):
			s.mu.Lock()
			ch := s.pending[stun.TransactionID(pkt)]
			s.mu.Unlock()
			if ch != nil {
				ch <- pkt
			}
		case bytes.HasPrefix(pkt, magic):
			s.handle(string(pkt[len(magic):]), from, conn)
		default:
			// Datos del canal confiable (QUIC). Los aceptamos de cualquier dirección
			// que ya nos haya hablado en RDA1, no solo del peer vigente: cada lado
			// elige su camino por separado y pueden quedar asimétricos (visto en
			// pruebas: uno manda por IPv6 y el otro por la red local).
			s.mu.Lock()
			ok := s.seen[from]
			if ok {
				s.lastHeard = time.Now()
			}
			s.mu.Unlock()
			if !ok {
				logx.For(logx.StagePunch).Debug("paquete de datos de una dirección desconocida ignorado", "from", from, "bytes", n)
				continue
			}
			select {
			case s.data <- pkt:
			default:
				logx.For(logx.StagePunch).Debug("cola de datos llena: paquete descartado (QUIC lo retransmite)")
			}
		}
	}
}

func (s *Session) handle(body string, from netip.AddrPort, conn Conn) {
	log := logx.For(logx.StagePunch)
	parts := strings.SplitN(body, " ", 3)
	if len(parts) < 3 {
		return
	}
	kind, sender, arg := parts[0], parts[1], parts[2]

	s.mu.Lock()
	if conn != s.conn {
		// Llegó por un socket de Spray: ese es el que acertó.
		losers, ok := s.promote(conn)
		if !ok {
			s.mu.Unlock()
			return
		}
		defer func() {
			for _, c := range losers {
				c.Close()
			}
		}()
		log.Info("camino iterativo: un socket acertó, pasa a ser el de la sesión", "local", conn.LocalAddr(), "from", from, "cerrados", len(losers))
	}
	s.seen[from] = true
	s.lastHeard = time.Now()
	adopted := false
	if !s.peer.IsValid() {
		switch {
		case len(s.remote) == 0:
			// Llegó tráfico antes de que pegáramos las direcciones del otro: nuestro
			// NAT deja entrar paquetes de cualquiera (filtrado abierto o puerto UPnP).
			// Lo adoptamos y disparamos nosotros también para confirmar la vuelta.
			log.Info("paquete entrante antes de conectar (filtrado abierto o puerto UPnP); adoptando peer", "from", from)
			if !s.punching {
				s.punching, adopted = true, true
			}
		case !slices.Contains(s.remote, from):
			// El otro lado aparece desde una dirección que no estaba en su lista: su
			// NAT usó otro puerto/IP para hablarnos. ICE lo llama "peer reflexive".
			log.Info("el otro lado llega desde una dirección que no estaba en su lista (peer reflexive)", "from", from)
		default:
			log.Info("respondió el candidato", "from", from)
		}
		s.peer = from
	}
	firstInbound := !s.inbound
	s.inbound = true
	s.mu.Unlock()

	if adopted {
		s.emit(Event{Kind: EvPunching, Addr: from})
		go s.punchLoop(0) // el otro lado ya nos escribió: no hay que esperar
	}
	if firstInbound {
		log.Info("¡primer paquete del otro lado!", "from", from, "sender", sender)
		s.emit(Event{Kind: EvInbound, Addr: from})
	}

	switch kind {
	case "PING":
		log.Debug("PING recibido → PONG", "from", from, "seq", arg)
		s.write(from, "PONG "+s.id+" "+arg)
	case "PONG":
		seq, err := strconv.ParseUint(arg, 10, 64)
		if err != nil {
			return
		}
		s.mu.Lock()
		sent, ok := s.sentAt[seq]
		delete(s.sentAt, seq)
		s.lastPong = time.Now()
		wasConnected := s.connected
		s.connected = true
		if s.peer != from {
			// La ida y vuelta se confirmó por este camino: es el que usamos.
			log.Info("cambiando al camino confirmado", "antes", s.peer, "ahora", from)
			s.peer = from
		}
		s.mu.Unlock()
		if !ok {
			return
		}
		rtt := time.Since(sent)
		log.Debug("PONG recibido", "from", from, "seq", seq, "rtt", rtt)
		if !wasConnected {
			log.Info("✓ conectado directo (ida y vuelta confirmada)", "peer", from, "rtt", rtt)
			s.emit(Event{Kind: EvConnected, Addr: from, RTT: rtt})
		} else {
			s.emit(Event{Kind: EvRTT, RTT: rtt})
		}
	case "MSG":
		log.Info("mensaje recibido", "from", from, "bytes", len(arg))
		s.emit(Event{Kind: EvMessage, Text: arg})
	}
}

func (s *Session) write(to netip.AddrPort, body string) error {
	_, err := s.sock().WriteToUDPAddrPort(append(append([]byte(nil), magic...), body...), to)
	return err
}

// ping manda un PING a cada destino: solo al peer si ya hay conexión; si no, a
// todos los candidatos del otro lado (y al peer tentativo, si lo hay).
func (s *Session) ping() {
	s.mu.Lock()
	var targets []netip.AddrPort
	if s.connected {
		targets = []netip.AddrPort{s.peer}
	} else {
		targets = slices.Clone(s.remote)
		if s.peer.IsValid() && !slices.Contains(targets, s.peer) {
			targets = append(targets, s.peer)
		}
	}
	seqs := make([]uint64, len(targets))
	now := time.Now()
	for i := range targets {
		s.seq++
		seqs[i] = s.seq
		s.sentAt[s.seq] = now
	}
	// No dejamos crecer el mapa con PINGs que nunca tuvieron respuesta.
	for k := range s.sentAt {
		if k+256 < s.seq {
			delete(s.sentAt, k)
		}
	}
	s.mu.Unlock()

	log := logx.For(logx.StagePunch)
	for i, t := range targets {
		if err := s.write(t, fmt.Sprintf("PING %s %d", s.id, seqs[i])); err != nil {
			log.Debug("no se pudo enviar PING", "to", t, "err", err)
			continue
		}
		log.Debug("PING enviado", "to", t, "seq", seqs[i])
	}
}

// --- Bucles ----------------------------------------------------------------

// punchLoop dispara PINGs seguidos hasta conectar o agotar el disparo rápido.
// Con wait > 0, primero espera a que el otro lado dispare (ver ConnectAfter).
func (s *Session) punchLoop(wait time.Duration) {
	s.mu.Lock()
	if s.looping {
		s.mu.Unlock()
		return
	}
	s.looping = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.looping = false
		s.mu.Unlock()
	}()
	if wait > 0 && !s.waitFirstShot(wait) {
		return
	}
	deadline := time.Now().Add(PunchTimeout)
	t := time.NewTicker(PunchInterval)
	defer t.Stop()
	for {
		s.mu.Lock()
		connected := s.connected
		s.mu.Unlock()
		if connected {
			return
		}
		if time.Now().After(deadline) {
			s.mu.Lock()
			inbound := s.inbound
			s.punching = false
			s.mu.Unlock()
			reason := "no llegó ningún paquete del otro lado"
			if inbound {
				reason = "llegan paquetes del otro lado pero los nuestros no le llegan"
			}
			logx.For(logx.StagePunch).Warn("✗ el disparo rápido no conectó; sigo intentando cada 5 s", "reason", reason)
			s.emit(Event{Kind: EvFailed, Text: reason})
			return
		}
		s.ping()
		select {
		case <-t.C:
		case <-s.closeCh:
			return
		}
	}
}

// maintain mantiene la puerta abierta: refresca STUN mientras esperamos y manda
// keepalives (que también siguen intentando conectar tras un disparo fallido).
func (s *Session) maintain() {
	log := logx.For(logx.StagePunch)
	stunT := time.NewTicker(STUNRefresh)
	kaT := time.NewTicker(KeepaliveInterval)
	defer stunT.Stop()
	defer kaT.Stop()
	for {
		select {
		case <-s.closeCh:
			return
		case <-stunT.C:
			s.mu.Lock()
			server, waiting := s.stunServer, !s.punching && !s.connected
			s.mu.Unlock()
			if server == nil || !waiting {
				continue
			}
			mapped, err := s.bind(server, 2*time.Second, 2)
			if err != nil {
				log.Warn("refresco STUN falló", "err", err)
				continue
			}
			log.Debug("refresco STUN (mantiene la puerta abierta)", "public", mapped)
			s.mu.Lock()
			var old netip.AddrPort
			changed := false
			if len(s.cands) > 0 && strings.HasPrefix(s.cands[0].Kind, CandSTUN) && s.cands[0].Addr != mapped {
				old, changed = s.cands[0].Addr, true
				s.cands[0].Addr = mapped
			}
			s.mu.Unlock()
			if changed {
				log.Warn("la dirección pública cambió: vuelve a pasársela al otro lado", "antes", old, "ahora", mapped)
				s.emit(Event{Kind: EvPublic, Text: s.CandidatesString()})
			}
		case <-kaT.C:
			s.mu.Lock()
			lastPong := s.lastPong
			lost := s.connected && time.Since(lastPong) > LostAfter
			if lost {
				s.connected = false
				s.lastPong = time.Time{}
			}
			active := (s.peer.IsValid() || len(s.remote) > 0) && !s.holding
			silent := time.Since(s.lastHeard)
			s.mu.Unlock()
			if idle := s.cfg.CloseIdle; idle > 0 && silent > idle {
				log.Info("cerrando sesión abandonada: sin noticias del otro lado", "id", s.id, "silencio", silent.Round(time.Second))
				go s.Close() // Close cierra closeCh, que también termina este bucle
				return
			}
			if lost {
				log.Warn("conexión perdida: sin respuesta del otro lado", "silencio", time.Since(lastPong).Round(time.Second))
				s.emit(Event{Kind: EvLost})
			}
			if active {
				s.ping() // si vuelve un PONG, handle() marca conectado otra vez
			}
		}
	}
}
