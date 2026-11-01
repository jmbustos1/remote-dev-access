package stun

import (
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestBindingRequestFormat(t *testing.T) {
	id, msg, err := NewBindingRequest()
	if err != nil {
		t.Fatal(err)
	}
	if len(msg) != 20 {
		t.Fatalf("largo = %d, quiero 20", len(msg))
	}
	if msg[0] != 0x00 || msg[1] != 0x01 {
		t.Errorf("tipo = %x, quiero 0001", msg[0:2])
	}
	if got := [4]byte(msg[4:8]); got != [4]byte{0x21, 0x12, 0xA4, 0x42} {
		t.Errorf("cookie = %x", got)
	}
	if TxID(msg[8:20]) != id {
		t.Error("transaction ID no quedó en el mensaje")
	}
}

func TestRoundTrip(t *testing.T) {
	for _, s := range []string{"190.100.5.9:62000", "[2001:db8::1]:443"} {
		want := netip.MustParseAddrPort(s)
		id, _, _ := NewBindingRequest()
		got, err := ParseBindingResponse(EncodeBindingResponse(id, want), id)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
}

func TestRejectsWrongTxID(t *testing.T) {
	id, _, _ := NewBindingRequest()
	other, _, _ := NewBindingRequest()
	resp := EncodeBindingResponse(id, netip.MustParseAddrPort("1.2.3.4:5"))
	if _, err := ParseBindingResponse(resp, other); err == nil {
		t.Fatal("aceptó una respuesta con otro transaction ID")
	}
}

// TestQueryAgainstFakeServer levanta un STUN falso en loopback que responde con
// la dirección desde la que le llegó el paquete, igual que uno real.
func TestQueryAgainstFakeServer(t *testing.T) {
	srv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := srv.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 20 {
				continue
			}
			var id TxID
			copy(id[:], buf[8:20])
			srv.WriteToUDP(EncodeBindingResponse(id, from.AddrPort()), from)
		}
	}()

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	got, _, err := Query(conn, srv.LocalAddr().(*net.UDPAddr), time.Second, 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := conn.LocalAddr().(*net.UDPAddr).AddrPort(); got != want {
		t.Errorf("mapped = %s, want %s", got, want)
	}
}

func TestClassify(t *testing.T) {
	local := netip.MustParseAddrPort("192.168.1.50:41641")
	a := netip.MustParseAddrPort("190.100.5.9:62000")
	b := netip.MustParseAddrPort("190.100.5.9:62417")
	cases := []struct {
		name string
		res  []Result
		want NATKind
	}{
		{"mismo puerto", []Result{{Resolved: "1.1.1.1:1", Mapped: a}, {Resolved: "2.2.2.2:1", Mapped: a}}, NATEndpointInd},
		{"puerto distinto", []Result{{Resolved: "1.1.1.1:1", Mapped: a}, {Resolved: "2.2.2.2:1", Mapped: b}}, NATSymmetric},
		{"una sola respuesta", []Result{{Resolved: "1.1.1.1:1", Mapped: a}}, NATUnknown},
		{"sin NAT", []Result{{Resolved: "1.1.1.1:1", Mapped: netip.AddrPortFrom(local.Addr(), 41641)}}, NATNone},
	}
	for _, c := range cases {
		if got := classify(&Report{Local: local, Results: c.res}); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
