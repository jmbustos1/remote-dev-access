// Package punch implementa el hole punching UDP "a mano" (fase 2).
//
// Una Session es UN socket UDP que hace todo: pregunta su dirección pública por
// STUN, mantiene viva la puerta del NAT mientras esperamos, dispara paquetes al
// otro lado y, una vez conectados, mide latencia y transporta mensajes. Usar el
// mismo socket es la clave: la puerta que abrió el STUN es por la que entra el
// otro lado.
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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/logx"
	"github.com/jmbustos1/remote-dev-access/core/netx"
	"github.com/jmbustos1/remote-dev-access/core/stun"
)

var magic = []byte("RDA1 ")

// Tiempos del protocolo.
var (
	PunchInterval     = 500 * time.Millisecond // ritmo de disparo mientras no hay conexión
	PunchTimeout      = 45 * time.Second       // cuánto disparar antes de rendirse
	KeepaliveInterval = 5 * time.Second        // PING estando conectados
	STUNRefresh       = 15 * time.Second       // refresco de la puerta mientras esperamos al otro
	LostAfter         = 20 * time.Second       // sin PONG por este tiempo → conexión perdida
)

// EventKind enumera lo que le pasa a una sesión.
type EventKind int

const (
	EvPublic    EventKind = iota // conocemos nuestra dirección pública (o cambió)
	EvPunching                   // empezamos a disparar hacia el otro lado
	EvInbound                    // llegó el primer paquete del otro lado (una dirección abierta)
	EvConnected                  // ida y vuelta confirmada
	EvRTT                        // nueva medición de latencia
	EvMessage                    // mensaje de chat recibido
	EvLost                       // dejamos de recibir respuestas
	EvFailed                     // el punching no logró conectar
)

func (k EventKind) String() string {
	return [...]string{"public", "punching", "inbound", "connected", "rtt", "message", "lost", "failed"}[k]
}

// Event se entrega al callback de la sesión.
type Event struct {
	Kind EventKind
	Addr netip.AddrPort // public / punching / inbound / connected
	RTT  time.Duration  // connected / rtt
	Text string         // message / failed
}

// Config de una sesión.
type Config struct {
	// STUNServers a probar en orden. Vacío = sin STUN (la dirección pública es la
	// local; útil en tests y en una misma red).
	STUNServers []string
	OnEvent     func(Event)
}

// Session es un socket UDP con estado de punching.
type Session struct {
	conn    *net.UDPConn
	id      string
	cfg     Config
	closeCh chan struct{}
	once    sync.Once

	mu         sync.Mutex
	public     netip.AddrPort
	local      netip.AddrPort
	stunServer *net.UDPAddr
	pending    map[stun.TxID]chan []byte
	peer       netip.AddrPort
	punching   bool
	inbound    bool
	connected  bool
	seq        uint64
	sentAt     map[uint64]time.Time
	lastPong   time.Time
}

// Open abre el socket y averigua la dirección pública.
func Open(ctx context.Context, cfg Config) (*Session, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, fmt.Errorf("abriendo socket UDP: %w", err)
	}
	idb := make([]byte, 4)
	rand.Read(idb)
	s := &Session{
		conn:    conn,
		id:      hex.EncodeToString(idb),
		cfg:     cfg,
		closeCh: make(chan struct{}),
		pending: map[stun.TxID]chan []byte{},
		sentAt:  map[uint64]time.Time{},
		local:   netx.LocalAddr(uint16(conn.LocalAddr().(*net.UDPAddr).Port)),
	}
	go s.readLoop()

	log := logx.For(logx.StagePunch)
	log.Info("sesión abierta", "id", s.id, "local", s.local)

	if len(cfg.STUNServers) == 0 {
		s.public = s.local
	} else if err := s.discover(ctx); err != nil {
		s.Close()
		return nil, err
	}
	s.emit(Event{Kind: EvPublic, Addr: s.public})
	go s.maintain()
	return s, nil
}

// Public es nuestra dirección vista desde internet: la que hay que pasarle al otro lado.
func (s *Session) Public() netip.AddrPort { s.mu.Lock(); defer s.mu.Unlock(); return s.public }

// Local es la dirección dentro de la red local (sirve si ambos están en el mismo wifi).
func (s *Session) Local() netip.AddrPort { return s.local }

// Connect empieza a disparar hacia peer ("ip:puerto"). No bloquea.
func (s *Session) Connect(peer string) error {
	ap, err := netip.ParseAddrPort(strings.TrimSpace(peer))
	if err != nil {
		return fmt.Errorf("dirección inválida %q (formato ip:puerto): %w", peer, err)
	}
	s.mu.Lock()
	if s.punching {
		s.mu.Unlock()
		return errors.New("ya se está conectando")
	}
	s.peer, s.punching = ap, true
	s.mu.Unlock()

	logx.For(logx.StagePunch).Info("disparando hacia el otro lado", "peer", ap)
	s.emit(Event{Kind: EvPunching, Addr: ap})
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

// Close cierra el socket y detiene todo.
func (s *Session) Close() {
	s.once.Do(func() {
		close(s.closeCh)
		s.conn.Close()
		logx.For(logx.StagePunch).Info("sesión cerrada", "id", s.id)
	})
}

func (s *Session) emit(e Event) {
	if s.cfg.OnEvent != nil {
		s.cfg.OnEvent(e)
	}
}

// --- STUN sobre el socket compartido ---------------------------------------

func (s *Session) discover(ctx context.Context) error {
	log := logx.For(logx.StageSTUN)
	var lastErr error
	for _, name := range s.cfg.STUNServers {
		addr, err := netx.ResolveUDP4(ctx, name)
		if err != nil {
			lastErr = err
			continue
		}
		mapped, err := s.bind(addr, 2*time.Second, 3)
		if err != nil {
			lastErr = err
			log.Warn("servidor STUN sin respuesta", "server", name, "err", err)
			continue
		}
		s.mu.Lock()
		s.public, s.stunServer = mapped, addr
		s.mu.Unlock()
		log.Info("dirección pública", "public", mapped, "server", name)
		return nil
	}
	return fmt.Errorf("ningún servidor STUN respondió: %w", lastErr)
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
			return stun.ParseBindingResponse(resp, id)
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
			logx.For(logx.StagePunch).Debug("paquete desconocido ignorado", "from", from, "bytes", n)
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
	adopted := false
	switch {
	case !s.peer.IsValid():
		// Llegó tráfico antes de que pegáramos la dirección del otro: nuestro NAT
		// deja entrar paquetes de cualquiera (filtrado abierto). Lo adoptamos y
		// empezamos a disparar nosotros también para confirmar la vuelta.
		log.Info("paquete entrante antes de conectar: el NAT tiene filtrado abierto; adoptando peer", "from", from)
		s.peer = from
		if !s.punching {
			s.punching, adopted = true, true
		}
	case from != s.peer:
		// El otro lado aparece desde una dirección distinta a la pegada (su NAT
		// cambió el puerto). ICE lo llama "peer reflexive candidate".
		log.Info("el otro lado llega desde otra dirección; la adoptamos", "esperada", s.peer, "real", from)
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
		log.Info("¡primer paquete del otro lado! su puerta y la nuestra están abiertas en esta dirección", "from", from, "sender", sender)
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

func (s *Session) ping() {
	s.mu.Lock()
	s.seq++
	seq, peer := s.seq, s.peer
	s.sentAt[seq] = time.Now()
	// No dejamos crecer el mapa con PINGs que nunca tuvieron respuesta.
	for k := range s.sentAt {
		if k+64 < seq {
			delete(s.sentAt, k)
		}
	}
	s.mu.Unlock()
	if err := s.write(peer, fmt.Sprintf("PING %s %d", s.id, seq)); err != nil {
		logx.For(logx.StagePunch).Warn("no se pudo enviar PING", "err", err)
		return
	}
	logx.For(logx.StagePunch).Debug("PING enviado", "to", peer, "seq", seq)
}

// --- Bucles ----------------------------------------------------------------

// punchLoop dispara PINGs seguidos hasta conectar o rendirse.
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
			logx.For(logx.StagePunch).Warn("✗ hole punching falló", "reason", reason)
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
// keepalives estando conectados.
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
			server, waiting, old := s.stunServer, !s.punching && !s.connected, s.public
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
			if mapped != old {
				s.mu.Lock()
				s.public = mapped
				s.mu.Unlock()
				log.Warn("la dirección pública cambió: vuelve a pasársela al otro lado", "antes", old, "ahora", mapped)
				s.emit(Event{Kind: EvPublic, Addr: mapped})
			}
		case <-kaT.C:
			s.mu.Lock()
			connected, lastPong := s.connected, s.lastPong
			if connected && time.Since(lastPong) > LostAfter {
				s.connected = false
				connected = false
			}
			s.mu.Unlock()
			if !connected && !lastPong.IsZero() && time.Since(lastPong) > LostAfter {
				log.Warn("conexión perdida: sin respuesta del otro lado", "silencio", time.Since(lastPong).Round(time.Second))
				s.emit(Event{Kind: EvLost})
				s.mu.Lock()
				s.lastPong = time.Time{} // avisar una sola vez
				s.mu.Unlock()
			}
			s.mu.Lock()
			peer := s.peer
			s.mu.Unlock()
			if peer.IsValid() {
				s.ping() // si vuelve un PONG, handle() marca conectado otra vez
			}
		}
	}
}
