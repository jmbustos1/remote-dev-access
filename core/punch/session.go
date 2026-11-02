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

// Config de una sesión.
type Config struct {
	// STUNServers a probar en orden (se usan para IPv4 e IPv6). Vacío = sin STUN:
	// el único candidato es la dirección local (tests, misma red).
	STUNServers []string
	// UPnP: pedirle al router un puerto reenviado (para PCs en la casa).
	UPnP    bool
	OnEvent func(Event)
}

// Session es un socket UDP con estado de punching.
type Session struct {
	conn    *net.UDPConn
	id      string
	cfg     Config
	closeCh chan struct{}
	once    sync.Once
	local   netip.AddrPort
	mapping *upnp.Mapping
	data    chan []byte // paquetes que no son STUN ni RDA1 (QUIC), ver PacketConn

	mu         sync.Mutex
	cands      []Candidate
	stunServer *net.UDPAddr // servidor IPv4 para refrescar la puerta
	pending    map[stun.TxID]chan []byte
	remote     []netip.AddrPort        // candidatos del otro lado
	peer       netip.AddrPort          // dirección elegida (la que respondió)
	seen       map[netip.AddrPort]bool // direcciones que nos han hablado en RDA1
	punching   bool
	inbound    bool
	connected  bool
	seq        uint64
	sentAt     map[uint64]time.Time
	lastPong   time.Time
}

// Open abre el socket y reúne nuestros candidatos.
func Open(ctx context.Context, cfg Config) (*Session, error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{}) // dual-stack: IPv4 e IPv6 en el mismo puerto
	if err != nil {
		if conn, err = net.ListenUDP("udp4", &net.UDPAddr{}); err != nil {
			return nil, fmt.Errorf("abriendo socket UDP: %w", err)
		}
	}
	idb := make([]byte, 4)
	rand.Read(idb)
	port := uint16(conn.LocalAddr().(*net.UDPAddr).Port)
	s := &Session{
		conn:    conn,
		id:      hex.EncodeToString(idb),
		cfg:     cfg,
		closeCh: make(chan struct{}),
		data:    make(chan []byte, 1024),
		pending: map[stun.TxID]chan []byte{},
		sentAt:  map[uint64]time.Time{},
		seen:    map[netip.AddrPort]bool{},
		local:   netx.LocalAddr(port),
	}
	go s.readLoop()

	log := logx.For(logx.StagePunch)
	log.Info("sesión abierta", "id", s.id, "local", s.local)

	if len(cfg.STUNServers) > 0 {
		pub4, err := s.discover(ctx, "ip4")
		if err != nil {
			s.Close()
			return nil, err
		}
		s.addCand(Candidate{CandSTUN, pub4})

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
	s.remote, s.punching = remote, true
	s.mu.Unlock()

	logx.For(logx.StagePunch).Info("disparando hacia todos los candidatos del otro lado", "candidatos", candidates)
	s.emit(Event{Kind: EvPunching, Addr: remote[0]})
	go s.punchLoop()
	return nil
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
		s.conn.Close()
		if s.mapping != nil {
			s.mapping.Close()
		}
		logx.For(logx.StagePunch).Info("sesión cerrada", "id", s.id)
	})
}

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

// bind manda un Binding Request y espera su respuesta, que llega por readLoop.
func (s *Session) bind(server *net.UDPAddr, timeout time.Duration, attempts int) (netip.AddrPort, error) {
	id, req, err := stun.NewBindingRequest()
	if err != nil {
		return netip.AddrPort{}, err
	}
	ch := make(chan []byte, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pending, id); s.mu.Unlock() }()

	for i := 0; i < attempts; i++ {
		if _, err := s.conn.WriteToUDP(req, server); err != nil {
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

func (s *Session) readLoop() {
	buf := make([]byte, 2048)
	for {
		n, from, err := s.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			select {
			case <-s.closeCh:
				return
			default:
				logx.For(logx.StagePunch).Warn("error leyendo socket", "err", err)
				continue
			}
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
			s.handle(string(pkt[len(magic):]), from)
		default:
			// Datos del canal confiable (QUIC). Los aceptamos de cualquier dirección
			// que ya nos haya hablado en RDA1, no solo del peer vigente: cada lado
			// elige su camino por separado y pueden quedar asimétricos (visto en
			// pruebas: uno manda por IPv6 y el otro por la red local).
			s.mu.Lock()
			ok := s.seen[from]
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

func (s *Session) handle(body string, from netip.AddrPort) {
	log := logx.For(logx.StagePunch)
	parts := strings.SplitN(body, " ", 3)
	if len(parts) < 3 {
		return
	}
	kind, sender, arg := parts[0], parts[1], parts[2]

	s.mu.Lock()
	s.seen[from] = true
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
		go s.punchLoop()
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
	_, err := s.conn.WriteToUDPAddrPort(append(append([]byte(nil), magic...), body...), to)
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
func (s *Session) punchLoop() {
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
			active := s.peer.IsValid() || len(s.remote) > 0
			s.mu.Unlock()
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
