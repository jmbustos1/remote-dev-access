package punch

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/stun"
)

func init() {
	PunchInterval = 20 * time.Millisecond
	KeepaliveInterval = 50 * time.Millisecond
}

// open crea una sesión sin STUN y canaliza sus eventos.
func open(t *testing.T) (*Session, chan Event) {
	t.Helper()
	ch := make(chan Event, 64)
	s, err := Open(context.Background(), Config{OnEvent: func(e Event) { ch <- e }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, ch
}

// loopback devuelve la dirección de la sesión en 127.0.0.1 (en tests no hay NAT).
func loopback(s *Session) string {
	return netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), s.Local().Port()).String()
}

func waitFor(t *testing.T, ch chan Event, kind EventKind) Event {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Kind == kind {
				return e
			}
		case <-timeout:
			t.Fatalf("no llegó el evento %v", kind)
		}
	}
}

func TestBothSidesConnectAndChat(t *testing.T) {
	a, aev := open(t)
	b, bev := open(t)
	waitFor(t, aev, EvPublic)
	waitFor(t, bev, EvPublic)

	if err := a.Connect(loopback(b)); err != nil {
		t.Fatal(err)
	}
	if err := b.Connect(loopback(a)); err != nil {
		t.Fatal(err)
	}
	if e := waitFor(t, aev, EvConnected); e.RTT <= 0 {
		t.Errorf("RTT = %v", e.RTT)
	}
	waitFor(t, bev, EvConnected)

	if err := a.Send("hola desde a"); err != nil {
		t.Fatal(err)
	}
	if e := waitFor(t, bev, EvMessage); e.Text != "hola desde a" {
		t.Errorf("mensaje = %q", e.Text)
	}
}

// Si solo un lado conoce la dirección del otro y el NAT del receptor deja
// entrar todo (aquí no hay NAT), el receptor adopta al remitente.
func TestOneSidedConnectAdoptsPeer(t *testing.T) {
	a, aev := open(t)
	b, bev := open(t)
	if err := a.Connect(loopback(b)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, bev, EvInbound)
	waitFor(t, aev, EvConnected)
	waitFor(t, bev, EvConnected) // b también confirma la vuelta con sus propios PING
	if err := b.Send("te encontré"); err != nil {
		t.Fatal(err)
	}
	if e := waitFor(t, aev, EvMessage); e.Text != "te encontré" {
		t.Errorf("mensaje = %q", e.Text)
	}
}

func TestSendBeforeConnectFails(t *testing.T) {
	a, _ := open(t)
	if err := a.Send("x"); err == nil {
		t.Fatal("Send sin conexión debería fallar")
	}
}

func TestPunchTimesOut(t *testing.T) {
	old := PunchTimeout
	PunchTimeout = 200 * time.Millisecond
	defer func() { PunchTimeout = old }()

	a, aev := open(t)
	// Un puerto donde nadie escucha.
	dead, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	addr := dead.LocalAddr().String()
	dead.Close()

	if err := a.Connect(addr); err != nil {
		t.Fatal(err)
	}
	waitFor(t, aev, EvFailed)
}

// La sesión obtiene su dirección pública de un STUN (falso) usando el mismo
// socket por el que después hace punching.
func TestDiscoverViaSharedSocket(t *testing.T) {
	srv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := srv.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			if stun.IsMessage(buf[:n]) {
				srv.WriteToUDPAddrPort(stun.EncodeBindingResponse(stun.TransactionID(buf[:n]), from), from)
			}
		}
	}()

	s, err := Open(context.Background(), Config{STUNServers: []string{srv.LocalAddr().String()}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.Public(); got.Port() != s.Local().Port() || got.Addr() != netip.MustParseAddr("127.0.0.1") {
		t.Errorf("public = %s, local = %s", got, s.Local())
	}
}
