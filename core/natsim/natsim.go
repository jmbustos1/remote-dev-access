// Package natsim es una red UDP simulada en memoria, con NATs configurables,
// para probar el hole punching sin tocar la red real: los tests reproducen la
// casa (EIM, conserva el puerto, filtrado estricto) o la oficina (simétrico,
// puertos al azar, dos IPs públicas) de forma determinista.
//
// No simula tiempos, pérdidas ni la expiración de los mapeos: solo qué paquete
// llega a quién y con qué dirección de origen. Vocabulario (mapeo y filtrado)
// en docs/nat-traversal.md §1.
package natsim

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"net"
	"net/netip"
	"sync"

	"github.com/jmbustos1/remote-dev-access/core/stun"
)

// Mapping: qué IP:puerto público recibe un paquete que sale.
type Mapping int

const (
	EIM       Mapping = iota // el mismo hacia cualquier destino
	Symmetric                // uno nuevo por cada destino (IP y puerto)
)

// Filtering: quién puede entrar por un mapeo EIM ya creado. Un mapeo simétrico
// solo deja entrar a su único destino.
type Filtering int

const (
	Open   Filtering = iota // cualquiera ("cono completo")
	Strict                  // solo el IP:puerto exacto al que el interno ya le escribió
)

// NATConfig describe un NAT.
type NATConfig struct {
	// IPs públicas. Con más de una, se elige según la IP destino (y siempre la
	// misma para el mismo destino), como el CGNAT medido en la oficina.
	IPs       []string
	Mapping   Mapping
	Filtering Filtering
	// Preserve: el puerto público es el local, si está libre.
	Preserve bool
	// Rango del que salen los puertos públicos (por defecto 1024–65535). Un rango
	// chico simula la asignación por bloque de un CGNAT.
	PortLo, PortHi uint16
}

// Net es la "internet" simulada.
type Net struct {
	mu      sync.Mutex
	rng     *rand.Rand
	hosts   map[netip.Addr]*Host // equipos con IP pública
	nats    map[netip.Addr]*NAT  // por cada IP pública de un NAT
	conns   []*Conn
	dropped int
}

// New crea una red vacía. seed fija los puertos "al azar" de los NAT.
func New(seed int64) *Net {
	return &Net{rng: rand.New(rand.NewSource(seed)), hosts: map[netip.Addr]*Host{}, nats: map[netip.Addr]*NAT{}}
}

// Close cierra todos los sockets (termina los servidores STUN simulados).
func (n *Net) Close() {
	n.mu.Lock()
	conns := n.conns
	n.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

// Dropped: cuántos paquetes descartó la red (filtro de un NAT o destino inexistente).
func (n *Net) Dropped() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.dropped
}

// Host agrega un equipo con IP pública propia (sin NAT).
func (n *Net) Host(ip string) *Host {
	n.mu.Lock()
	defer n.mu.Unlock()
	h := &Host{net: n, ip: netip.MustParseAddr(ip), conns: map[uint16]*Conn{}}
	n.hosts[h.ip] = h
	return h
}

// STUN agrega un servidor STUN en ip:3478 y devuelve su dirección.
func (n *Net) STUN(ip string) string {
	c, _ := n.Host(ip).ListenUDP(3478)
	go func() {
		buf := make([]byte, 1500)
		for {
			k, from, err := c.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			if stun.IsMessage(buf[:k]) {
				c.WriteToUDPAddrPort(stun.EncodeBindingResponse(stun.TransactionID(buf[:k]), from), from)
			}
		}
	}()
	return netip.AddrPortFrom(c.host.ip, 3478).String()
}

// NAT agrega un NAT con sus IPs públicas.
func (n *Net) NAT(cfg NATConfig) *NAT {
	n.mu.Lock()
	defer n.mu.Unlock()
	nat := &NAT{net: n, cfg: cfg, byKey: map[natKey]*mapping{}, byPub: map[netip.AddrPort]*mapping{}, hosts: map[netip.Addr]*Host{}}
	if nat.cfg.PortLo == 0 {
		nat.cfg.PortLo = 1024
	}
	if nat.cfg.PortHi == 0 {
		nat.cfg.PortHi = 65535
	}
	for _, s := range cfg.IPs {
		ip := netip.MustParseAddr(s)
		nat.ips = append(nat.ips, ip)
		n.nats[ip] = nat
	}
	return nat
}

// NAT traduce direcciones entre sus equipos internos e internet.
type NAT struct {
	net   *Net
	cfg   NATConfig
	ips   []netip.Addr
	hosts map[netip.Addr]*Host
	byKey map[natKey]*mapping
	byPub map[netip.AddrPort]*mapping
}

type natKey struct {
	internal netip.AddrPort
	pubIP    netip.Addr
	dst      netip.AddrPort // solo en mapeo simétrico
}

type mapping struct {
	internal netip.AddrPort
	public   netip.AddrPort
	dst      netip.AddrPort          // simétrico: el único destino del mapeo
	allowed  map[netip.AddrPort]bool // EIM: a quién le escribió el interno
}

// Host agrega un equipo detrás del NAT, con una IP privada.
func (nat *NAT) Host(ip string) *Host {
	nat.net.mu.Lock()
	defer nat.net.mu.Unlock()
	h := &Host{net: nat.net, nat: nat, ip: netip.MustParseAddr(ip), conns: map[uint16]*Conn{}}
	nat.hosts[h.ip] = h
	return h
}

// Mappings: cuántos mapeos tiene abiertos el NAT.
func (nat *NAT) Mappings() int {
	nat.net.mu.Lock()
	defer nat.net.mu.Unlock()
	return len(nat.byPub)
}

// out traduce un paquete que sale. Llamar con net.mu tomado.
func (nat *NAT) out(src, dst netip.AddrPort) netip.AddrPort {
	h := fnv.New32a()
	h.Write(dst.Addr().AsSlice())
	key := natKey{internal: src, pubIP: nat.ips[int(h.Sum32())%len(nat.ips)]}
	if nat.cfg.Mapping == Symmetric {
		key.dst = dst
	}
	m := nat.byKey[key]
	if m == nil {
		m = &mapping{internal: src, dst: key.dst, allowed: map[netip.AddrPort]bool{}}
		port := src.Port()
		for try := 0; ; try++ {
			if !nat.cfg.Preserve || try > 0 {
				port = nat.cfg.PortLo + uint16(nat.net.rng.Intn(int(nat.cfg.PortHi-nat.cfg.PortLo)+1))
			}
			m.public = netip.AddrPortFrom(key.pubIP, port)
			if nat.byPub[m.public] == nil {
				break
			}
		}
		nat.byKey[key] = m
		nat.byPub[m.public] = m
	}
	m.allowed[dst] = true
	return m.public
}

// in decide si un paquete que llega a una dirección pública entra, y a quién.
// Llamar con net.mu tomado.
func (nat *NAT) in(src, dst netip.AddrPort) (netip.AddrPort, bool) {
	m := nat.byPub[dst]
	switch {
	case m == nil:
		return netip.AddrPort{}, false
	case nat.cfg.Mapping == Symmetric:
		return m.internal, src == m.dst
	case nat.cfg.Filtering == Strict:
		return m.internal, m.allowed[src]
	}
	return m.internal, true
}

// Host es un equipo: con IP pública (Net.Host) o detrás de un NAT (NAT.Host).
type Host struct {
	net   *Net
	nat   *NAT
	ip    netip.Addr
	conns map[uint16]*Conn
}

// ListenUDP abre un socket en port (0 = uno libre al azar).
func (h *Host) ListenUDP(port uint16) (*Conn, error) {
	h.net.mu.Lock()
	defer h.net.mu.Unlock()
	if port == 0 {
		for port == 0 || h.conns[port] != nil {
			port = 49152 + uint16(h.net.rng.Intn(65536-49152))
		}
	} else if h.conns[port] != nil {
		return nil, fmt.Errorf("natsim: %s:%d ya está en uso", h.ip, port)
	}
	c := &Conn{host: h, port: port, in: make(chan packet, 4096), closed: make(chan struct{})}
	h.conns[port] = c
	h.net.conns = append(h.net.conns, c)
	return c, nil
}

type packet struct {
	from netip.AddrPort
	data []byte
}

// Conn es un socket UDP simulado. Cumple punch.Conn.
type Conn struct {
	host   *Host
	port   uint16
	in     chan packet
	closed chan struct{}
	once   sync.Once
}

func (c *Conn) LocalAddr() net.Addr {
	return net.UDPAddrFromAddrPort(netip.AddrPortFrom(c.host.ip, c.port))
}

func (c *Conn) Close() error {
	c.once.Do(func() {
		close(c.closed)
		c.host.net.mu.Lock()
		delete(c.host.conns, c.port)
		c.host.net.mu.Unlock()
	})
	return nil
}

func (c *Conn) ReadFromUDPAddrPort(b []byte) (int, netip.AddrPort, error) {
	select {
	case p := <-c.in:
		return copy(b, p.data), p.from, nil
	case <-c.closed:
		return 0, netip.AddrPort{}, net.ErrClosed
	}
}

func (c *Conn) WriteToUDPAddrPort(b []byte, dst netip.AddrPort) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	dst = netip.AddrPortFrom(dst.Addr().Unmap(), dst.Port())
	n := c.host.net
	n.mu.Lock()
	defer n.mu.Unlock()

	src := netip.AddrPortFrom(c.host.ip, c.port)
	if c.host.nat != nil {
		src = c.host.nat.out(src, dst)
	}
	var target *Host
	port := dst.Port()
	if nat := n.nats[dst.Addr()]; nat != nil {
		if inner, ok := nat.in(src, dst); ok {
			target, port = nat.hosts[inner.Addr()], inner.Port()
		}
	} else {
		target = n.hosts[dst.Addr()]
	}
	var to *Conn
	if target != nil {
		to = target.conns[port]
	}
	if to == nil {
		n.dropped++
		return len(b), nil // UDP: el que envía no se entera
	}
	select {
	case to.in <- packet{from: src, data: append([]byte(nil), b...)}:
	default:
		n.dropped++ // cola llena
	}
	return len(b), nil
}
