package punch

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/logx"
	"github.com/jmbustos1/remote-dev-access/core/natlab"
	"github.com/jmbustos1/remote-dev-access/core/netx"
)

// Camino iterativo (fase 5d): para cuando un lado tiene NAT simétrico y el otro
// conserva su IP:puerto público. El puerto con que el simétrico sale hacia el
// otro no se puede saber, así que se apuesta a una coincidencia entre muchos:
//
//	S (simétrico): Spray. Abre N sockets y cada uno le escribe al otro lado: su
//	   NAT crea N mapeos con puertos públicos desconocidos.
//	E (el otro):   Sweep. Desde su único socket dispara a M puertos al azar de
//	   las IPs públicas de S: su NAT abre M filtros.
//
// Basta una coincidencia: el paquete entra por un mapeo de S, y la respuesta de
// S entra por el filtro que E acaba de abrir. S dispara primero siempre (crea
// los mapeos, y un NAT de Linux en su lado no se "envenena"). Cuentas y
// condiciones en docs/nat-traversal.md §4.3.

// Parámetros del camino iterativo.
var (
	SpraySockets = 256                   // N: sockets que abre el lado simétrico
	SpraySample  = 16                    // a cuántos se les pregunta por STUN para estimar el rango
	SweepMax     = 4096                  // tope de disparos por ronda (entre todas las IPs)
	SweepRounds  = 3                     // rondas, cada una con puertos nuevos
	SweepBatch   = 64                    // disparos seguidos antes de una pausa
	SweepPause   = 20 * time.Millisecond // ~3200 paquetes/s: una ronda típica dura < 1 s
)

// SweepInfo es lo que el lado simétrico le manda al otro para que barra.
type SweepInfo struct {
	IPs     []netip.Addr `json:"ips"` // IPs públicas por las que podemos salir
	Lo      uint16       `json:"lo"`  // rango de puertos públicos donde caen nuestros sockets
	Hi      uint16       `json:"hi"`
	Sockets int          `json:"sockets"` // cuántos sockets abrimos (N)
}

// Probes: cuántos puertos hay que probar por IP para acertar con 99 %, y
// cuántos se prueban de verdad con el tope SweepMax.
func (i SweepInfo) Probes() (want, perIP int) {
	r := int(i.Hi) - int(i.Lo) + 1
	want = natlab.ProbesFor(0.99, max(i.Sockets, 1), r)
	return want, min(want, r, SweepMax/max(len(i.IPs), 1))
}

func (i SweepInfo) validate() error {
	if len(i.IPs) == 0 || len(i.IPs) > 8 || i.Lo < 1024 || i.Hi < i.Lo || i.Sockets <= 0 {
		return fmt.Errorf("datos de barrido inválidos: %+v", i)
	}
	// Solo IPv4 públicas: los datos vienen del otro lado, y sin este filtro un
	// dispositivo comprometido podría hacer que barramos nuestra propia red local
	// (192.168.x.x), loopback, multicast o el espacio del CGNAT.
	for _, ip := range i.IPs {
		if !publicIPv4(ip) {
			return fmt.Errorf("datos de barrido inválidos: %s no es una IPv4 pública", ip)
		}
	}
	return nil
}

var cgnatSpace = netip.MustParsePrefix("100.64.0.0/10") // RFC 6598, compartido por los CGNAT

func publicIPv4(ip netip.Addr) bool {
	return ip.Is4() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !cgnatSpace.Contains(ip) &&
		ip != netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

// Spray es el lado simétrico: abre n sockets, dispara desde todos hacia los
// candidatos del otro lado y devuelve lo que el otro necesita para barrer. Sigue
// disparando hasta que un socket recibe respuesta (pasa a ser el de la sesión y
// los demás se cierran) o se agota PunchTimeout. Se puede combinar con Connect:
// el socket de la sesión sigue con el punching normal, que es el que sirve si el
// otro lado tiene un puerto reenviado (UPnP o fijo).
func (s *Session) Spray(candidates string, n int) (SweepInfo, error) {
	remote, err := ParseCandidates(candidates)
	if err != nil {
		return SweepInfo{}, err
	}
	log := logx.For(logx.StagePunch)

	var socks []Conn
	for len(socks) < n {
		c, _, err := listen(s.cfg, 0)
		if err != nil {
			// Típico: límite de archivos abiertos. Con menos sockets baja la
			// probabilidad por ronda, pero sigue sirviendo.
			log.Warn("no se pudieron abrir todos los sockets", "abiertos", len(socks), "pedidos", n, "err", err)
			break
		}
		socks = append(socks, c)
	}
	if len(socks) == 0 {
		return SweepInfo{}, errors.New("no se pudo abrir ningún socket para el camino iterativo")
	}

	s.mu.Lock()
	if s.connected || len(s.extras) > 0 {
		s.mu.Unlock()
		for _, c := range socks {
			c.Close()
		}
		return SweepInfo{}, errors.New("la sesión ya está conectada o iterando")
	}
	for _, r := range remote {
		if !slices.Contains(s.remote, r) {
			s.remote = append(s.remote, r)
		}
	}
	remote = slices.Clone(s.remote)
	s.punching, s.extras = true, socks
	s.mu.Unlock()
	for _, c := range socks {
		go s.readLoop(c)
	}

	log.Info("camino iterativo: disparando desde varios sockets", "sockets", len(socks), "candidatos", candidates)
	s.emit(Event{Kind: EvPunching, Addr: remote[0]})
	s.spray() // antes de avisarle al otro: nuestros mapeos tienen que existir cuando barra

	info := s.sweepInfo(socks)
	log.Info("camino iterativo: rango estimado", "ips", info.IPs, "lo", info.Lo, "hi", info.Hi)
	go s.sprayLoop()
	return info, nil
}

// sprayTargets: a qué direcciones del otro lado se dispara desde cada socket.
// Solo las públicas IPv4: son las únicas que pasan por nuestro NAT.
func sprayTargets(remote []netip.AddrPort) []netip.AddrPort {
	var pub []netip.AddrPort
	for _, r := range remote {
		if a := r.Addr(); a.Is4() && !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() {
			pub = append(pub, r)
		}
	}
	if len(pub) == 0 {
		return remote
	}
	return pub
}

// spray manda un PING desde cada socket de Spray a cada destino. Todos llevan
// el mismo seq: cualquiera que vuelva sirve para confirmar la conexión.
func (s *Session) spray() {
	s.mu.Lock()
	socks := slices.Clone(s.extras)
	targets := sprayTargets(s.remote)
	s.seq++
	seq := s.seq
	s.sentAt[seq] = time.Now()
	s.mu.Unlock()
	pkt := append(append([]byte(nil), magic...), fmt.Sprintf("PING %s %d", s.id, seq)...)
	for _, c := range socks {
		for _, t := range targets {
			c.WriteToUDPAddrPort(pkt, t)
		}
	}
}

func (s *Session) sprayLoop() {
	deadline := time.Now().Add(PunchTimeout)
	t := time.NewTicker(PunchInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-s.closeCh:
			return
		}
		s.mu.Lock()
		iterating, connected := len(s.extras) > 0, s.connected
		s.mu.Unlock()
		switch {
		case connected:
			s.dropExtras()
			return
		case !iterating:
			// Un socket acertó y ya es el de la sesión: falta confirmar la ida y vuelta.
			s.punchLoop(0)
			return
		case time.Now().After(deadline):
			s.dropExtras()
			s.failed("el camino iterativo no acertó ningún puerto")
			return
		}
		s.spray()
	}
}

func (s *Session) dropExtras() {
	s.mu.Lock()
	socks := s.extras
	s.extras = nil
	s.mu.Unlock()
	for _, c := range socks {
		c.Close()
	}
}

func (s *Session) failed(reason string) {
	s.mu.Lock()
	s.punching = false
	s.mu.Unlock()
	logx.For(logx.StagePunch).Warn("✗ "+reason+"; sigo intentando cada 5 s", "reason", reason)
	s.emit(Event{Kind: EvFailed, Text: reason})
}

// promote convierte un socket de Spray en el de la sesión y devuelve los que hay
// que cerrar. Llamar con s.mu tomado. ok = false si conn ya no sirve (la sesión
// conectó por otro socket).
func (s *Session) promote(conn Conn) (losers []Conn, ok bool) {
	if s.connected || !slices.Contains(s.extras, conn) {
		return nil, false
	}
	losers = append(losers, s.conn)
	for _, c := range s.extras {
		if c != conn {
			losers = append(losers, c)
		}
	}
	s.conn, s.extras = conn, nil
	s.peer = netip.AddrPort{} // lo que hubiera era del socket viejo
	return losers, true
}

// sweepInfo estima por dónde salen nuestros sockets: les pregunta a algunos por
// STUN (repartidos entre los servidores, para ver todas las IPs públicas) y
// arma el rango de puertos con el mismo análisis de `rda natlab`.
func (s *Session) sweepInfo(socks []Conn) SweepInfo {
	info := SweepInfo{Lo: 1024, Hi: 65535, Sockets: len(socks)}
	addIP := func(ip netip.Addr) {
		if ip.IsValid() && ip.Is4() && !slices.Contains(info.IPs, ip) {
			info.IPs = append(info.IPs, ip)
		}
	}
	for _, c := range s.Candidates() {
		if c.Kind != CandLocal {
			addIP(c.Addr.Addr())
		}
	}

	var servers []*net.UDPAddr
	for _, name := range s.cfg.STUNServers {
		if a, err := netx.ResolveUDP4(context.Background(), name); err == nil {
			servers = append(servers, a)
		}
	}
	if len(servers) == 0 {
		if len(info.IPs) == 0 {
			info.IPs = []netip.Addr{s.Local().Addr()} // sin STUN (tests, misma red)
		}
		return info
	}

	sample := socks[:min(SpraySample, len(socks))]
	obs := make([]natlab.Obs, len(sample))
	var wg sync.WaitGroup
	for i, c := range sample {
		wg.Add(1)
		go func() {
			defer wg.Done()
			obs[i].Local = uint16(c.LocalAddr().(*net.UDPAddr).Port)
			obs[i].Mapped, obs[i].Err = s.bindOn(c, servers[i%len(servers)], time.Second, 1)
		}()
	}
	wg.Wait()
	for _, o := range obs {
		if o.Err == nil {
			addIP(o.Mapped.Addr())
		}
	}
	// Cualquier bloque de ese tamaño que contenga lo observado cae en [hi−bloque, lo+bloque].
	// Solo si lo observado cabe en el bloque: con asignación secuencial (bloque
	// estimado 64) o que conserva el puerto (1), los puertos se reparten más allá
	// y el rango saldría invertido (lo > hi): se barre el rango completo.
	if a := natlab.AnalyzeAllocation(obs); a.Answers >= 4 && a.Block < int(info.Hi-info.Lo) && int(a.Max)-int(a.Min) < a.Block {
		info.Lo = uint16(max(1024, int(a.Max)-a.Block))
		info.Hi = uint16(min(65535, int(a.Min)+a.Block))
	}
	return info
}

// Sweep es el lado que conserva su puerto: dispara a puertos al azar de las IPs
// del otro lado hasta acertar uno de sus mapeos. candidates son las direcciones
// normales del otro lado (se siguen probando entre rondas).
func (s *Session) Sweep(candidates string, info SweepInfo) error {
	if err := info.validate(); err != nil {
		return err
	}
	remote, err := ParseCandidates(candidates)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.connected {
		s.mu.Unlock()
		return nil
	}
	for _, r := range remote {
		if !slices.Contains(s.remote, r) {
			s.remote = append(s.remote, r)
		}
	}
	s.punching = true
	s.mu.Unlock()

	want, perIP := info.Probes()
	logx.For(logx.StagePunch).Info("camino iterativo: barriendo puertos del otro lado",
		"ips", info.IPs, "lo", info.Lo, "hi", info.Hi, "por_ip", perIP, "para_99", want, "rondas", SweepRounds)
	s.emit(Event{Kind: EvPunching, Addr: netip.AddrPortFrom(info.IPs[0], info.Lo)})
	go s.sweepLoop(info, perIP)
	return nil
}

func (s *Session) sweepLoop(info SweepInfo, perIP int) {
	r := int(info.Hi) - int(info.Lo) + 1
	// found: ya nos habló alguien (o conectamos): el resto lo hace punchLoop.
	found := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.connected || s.peer.IsValid()
	}
	pause := func(d time.Duration) bool { // false = sesión cerrada
		select {
		case <-time.After(d):
			return true
		case <-s.closeCh:
			return false
		}
	}
	for round := 0; round < SweepRounds; round++ {
		s.mu.Lock()
		s.seq++
		seq := s.seq
		s.sentAt[seq] = time.Now()
		s.mu.Unlock()
		pkt := append(append([]byte(nil), magic...), fmt.Sprintf("PING %s %d", s.id, seq)...)
		conn := s.sock()

		sent := 0
		for _, off := range rand.Perm(r)[:perIP] {
			for _, ip := range info.IPs {
				conn.WriteToUDPAddrPort(pkt, netip.AddrPortFrom(ip, info.Lo+uint16(off)))
				if sent++; sent%SweepBatch == 0 {
					if found() {
						s.punchLoop(0)
						return
					}
					if !pause(SweepPause) {
						return
					}
				}
			}
		}
		// Margen para que vuelva una respuesta antes de gastar otra ronda.
		for i := 0; i < 4; i++ {
			if found() {
				s.punchLoop(0)
				return
			}
			if !pause(PunchInterval) {
				return
			}
		}
		logx.For(logx.StagePunch).Info("camino iterativo: ronda sin acierto", "ronda", round+1, "disparos", sent)
	}
	s.failed("el camino iterativo no acertó ningún puerto")
}
