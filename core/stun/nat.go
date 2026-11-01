package stun

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/logx"
	"github.com/jmbustos1/remote-dev-access/core/netx"
)

// DefaultServers son STUN públicos y gratuitos. Usamos operadores/IPs distintos
// para poder comparar el mapeo que el NAT asigna hacia destinos diferentes.
var DefaultServers = []string{
	"stun.l.google.com:19302",
	"stun.cloudflare.com:3478",
	"global.stun.twilio.com:3478",
}

// Result es lo que respondió un servidor.
type Result struct {
	Server   string
	Resolved string
	Mapped   netip.AddrPort
	RTT      time.Duration
	Err      error
}

// NATKind resume qué tan amigable es el NAT para el hole punching.
type NATKind int

const (
	NATUnknown     NATKind = iota
	NATNone                // la IP local ya es pública
	NATEndpointInd         // mismo IP:puerto público hacia cualquier destino → punching OK
	NATSymmetric           // puerto distinto por destino → probablemente haga falta relay
)

func (k NATKind) String() string {
	switch k {
	case NATNone:
		return "sin NAT (IP pública directa)"
	case NATEndpointInd:
		return "mapeo independiente del destino: el hole punching debería funcionar"
	case NATSymmetric:
		return "NAT simétrico: el puerto cambia según el destino, probablemente haga falta relay"
	}
	return "desconocido (se necesitan al menos 2 respuestas)"
}

// Report es el resultado de consultar varios servidores desde UN mismo socket.
type Report struct {
	Local   netip.AddrPort
	Results []Result
	Kind    NATKind
	// IPv6: mismas consultas por IPv6 (vacío si la red no tiene IPv6). Si la
	// dirección mapeada es la propia del equipo, no hay NAT en el camino.
	IPv6 []Result
}

// Probe abre un socket UDP en localPort (0 = cualquiera), consulta a todos los
// servidores desde ese mismo socket y clasifica el NAT.
func Probe(ctx context.Context, servers []string, localPort int, timeout time.Duration) (*Report, error) {
	log := logx.For(logx.StageSTUN)

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: localPort})
	if err != nil {
		return nil, fmt.Errorf("abriendo socket UDP: %w", err)
	}
	defer conn.Close()

	rep := &Report{Local: netx.LocalAddr(uint16(conn.LocalAddr().(*net.UDPAddr).Port))}
	log.Debug("socket abierto", "local", rep.Local)

	for _, s := range servers {
		r := Result{Server: s}
		addr, err := netx.ResolveUDP4(ctx, s)
		if err != nil {
			r.Err = err
			log.Warn("no se pudo resolver", "server", s, "err", err)
			rep.Results = append(rep.Results, r)
			continue
		}
		r.Resolved = addr.String()
		log.Debug("binding request", "server", s, "to", addr)
		r.Mapped, r.RTT, r.Err = Query(conn, addr, timeout, 3)
		if r.Err != nil {
			log.Warn("sin respuesta", "server", s, "err", r.Err)
		} else {
			log.Debug("binding response", "server", s, "mapped", r.Mapped, "rtt", r.RTT)
		}
		rep.Results = append(rep.Results, r)
	}
	rep.Kind = classify(rep)
	log.Info("clasificación NAT", "kind", rep.Kind.String())

	if c6, err := net.ListenUDP("udp6", &net.UDPAddr{}); err != nil {
		log.Info("sin socket IPv6", "err", err)
	} else {
		defer c6.Close()
		for _, s := range servers {
			addr, err := netx.ResolveUDP(ctx, s, "ip6")
			if err != nil {
				continue // el servidor no tiene IPv6
			}
			r := Result{Server: s, Resolved: addr.String()}
			r.Mapped, r.RTT, r.Err = Query(c6, addr, timeout, 2)
			if r.Err != nil {
				log.Debug("sin respuesta IPv6", "server", s, "err", r.Err)
			} else {
				log.Info("IPv6 pública", "server", s, "mapped", r.Mapped)
			}
			rep.IPv6 = append(rep.IPv6, r)
		}
	}
	return rep, nil
}

// HasIPv6 dice si al menos un servidor respondió por IPv6.
func (r *Report) HasIPv6() bool {
	for _, x := range r.IPv6 {
		if x.Err == nil {
			return true
		}
	}
	return false
}

func classify(rep *Report) NATKind {
	var mapped []netip.AddrPort
	seenIP := map[string]bool{}
	for _, r := range rep.Results {
		if r.Err == nil {
			mapped = append(mapped, r.Mapped)
			ip, _, _ := net.SplitHostPort(r.Resolved)
			seenIP[ip] = true
		}
	}
	if len(mapped) == 0 {
		return NATUnknown
	}
	if mapped[0].Addr() == rep.Local.Addr() {
		return NATNone
	}
	if len(mapped) < 2 || len(seenIP) < 2 {
		return NATUnknown
	}
	for _, m := range mapped[1:] {
		if m != mapped[0] {
			return NATSymmetric
		}
	}
	return NATEndpointInd
}
