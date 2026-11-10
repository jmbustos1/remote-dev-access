package punch

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/jmbustos1/remote-dev-access/core/stun"
)

// remappingSTUN responde como un NAT que sale por otro puerto (+1000).
func remappingSTUN(t *testing.T) string {
	t.Helper()
	srv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := srv.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			if stun.IsMessage(buf[:n]) {
				mapped := netip.AddrPortFrom(from.Addr(), from.Port()+1000)
				srv.WriteToUDPAddrPort(stun.EncodeBindingResponse(stun.TransactionID(buf[:n]), mapped), from)
			}
		}
	}()
	return srv.LocalAddr().String()
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return uint16(c.LocalAddr().(*net.UDPAddr).Port)
}

func TestFixedPortAnnouncedEvenIfNATRemaps(t *testing.T) {
	port := freePort(t)
	srv := remappingSTUN(t)

	s, err := Open(context.Background(), Config{STUNServers: []string{srv}, Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Local().Port() != port {
		t.Fatalf("socket en %d, se pidió %d", s.Local().Port(), port)
	}
	want := Candidate{CandFixed, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port)}
	found := false
	for _, c := range s.Candidates() {
		if c == want {
			found = true
		}
	}
	if !found {
		t.Errorf("falta %v en %v", want, s.Candidates())
	}

	// Segunda sesión con el puerto ocupado: cae a uno al azar y no anuncia el fijo.
	s2, err := Open(context.Background(), Config{STUNServers: []string{srv}, Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.Local().Port() == port {
		t.Fatal("la segunda sesión no debería tener el puerto fijo")
	}
	for _, c := range s2.Candidates() {
		if c.Kind == CandFixed {
			t.Errorf("la segunda sesión anuncia %v", c)
		}
	}
}
