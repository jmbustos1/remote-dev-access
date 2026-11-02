package punch

import (
	"net"
	"os"
	"sync"
	"time"
)

// PeerAddr es la dirección que ve QUIC para el otro lado. Es fija a propósito:
// la dirección real puede cambiar (el NAT del celular reasigna puerto, pasa de
// wifi a 4G y el keepalive reconfirma otro camino) y QUIC no tiene por qué
// enterarse. 192.0.2.0/24 es un rango de documentación: nunca se enruta.
var PeerAddr = &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 1}

// PacketConn presenta el socket de la sesión como un net.PacketConn para montar
// encima un protocolo confiable (QUIC). Comparte el socket con STUN y con los
// PING/PONG del punching: readLoop separa los paquetes por su forma
//
//	STUN  → primer byte 0x00/0x01 + magic cookie
//	RDA1  → empieza con "RDA1 "
//	resto → datos para este PacketConn (QUIC siempre trae el bit 0x40 encendido)
//
// y WriteTo siempre manda al peer vigente, ignorando la dirección que pida QUIC.
func (s *Session) PacketConn() net.PacketConn { return &packetConn{s: s} }

type packetConn struct {
	s *Session

	mu       sync.Mutex
	deadline time.Time
	changed  chan struct{} // se cierra cada vez que cambia el deadline
}

func (c *packetConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		c.mu.Lock()
		dl := c.deadline
		if c.changed == nil {
			c.changed = make(chan struct{})
		}
		changed := c.changed
		c.mu.Unlock()

		var timer *time.Timer
		var timeout <-chan time.Time
		if !dl.IsZero() {
			d := time.Until(dl)
			if d <= 0 {
				return 0, nil, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(d)
			timeout = timer.C
		}
		n, err, again := 0, error(nil), false
		select {
		case pkt := <-c.s.data:
			n = copy(p, pkt)
		case <-c.s.closeCh:
			err = net.ErrClosed
		case <-timeout:
			err = os.ErrDeadlineExceeded
		case <-changed: // nuevo deadline: volver a calcular
			again = true
		}
		if timer != nil {
			timer.Stop()
		}
		if again {
			continue
		}
		if err != nil {
			return 0, nil, err
		}
		return n, PeerAddr, nil
	}
}

func (c *packetConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	select {
	case <-c.s.closeCh:
		return 0, net.ErrClosed
	default:
	}
	c.s.mu.Lock()
	peer := c.s.peer
	c.s.mu.Unlock()
	if !peer.IsValid() {
		// Sin camino todavía (o perdido): QUIC lo trata como un paquete perdido y
		// lo retransmite; para entonces el keepalive puede haber reconectado.
		return len(p), nil
	}
	if _, err := c.s.conn.WriteToUDPAddrPort(p, peer); err != nil {
		// Mismo criterio: un error de red transitorio (p. ej. Android cortando la
		// red unos segundos) no debe matar la conexión QUIC.
		return len(p), nil
	}
	return len(p), nil
}

func (c *packetConn) Close() error { return nil } // el socket es de la Session
func (c *packetConn) LocalAddr() net.Addr {
	return c.s.conn.LocalAddr()
}

func (c *packetConn) SetDeadline(t time.Time) error    { return c.SetReadDeadline(t) }
func (c *packetConn) SetWriteDeadline(time.Time) error { return nil }
func (c *packetConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	if c.changed != nil {
		close(c.changed)
	}
	c.changed = make(chan struct{})
	c.mu.Unlock()
	return nil
}
