package rendezvous

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/natsim"
	"github.com/jmbustos1/remote-dev-access/core/punch"
	"github.com/jmbustos1/remote-dev-access/core/signaling"
)

// Las mismas redes de core/punch/iterative_test.go (mediciones en docs/experimentos.md).
var (
	casa    = natsim.NATConfig{IPs: []string{"203.0.113.10"}, Mapping: natsim.EIM, Filtering: natsim.Strict, Preserve: true}
	oficina = natsim.NATConfig{IPs: []string{"198.51.100.20", "198.51.100.21"}, Mapping: natsim.Symmetric}
	otraSym = natsim.NATConfig{IPs: []string{"198.51.100.50"}, Mapping: natsim.Symmetric}
)

// simPair es pair, pero con el UDP por una red simulada: el agente detrás de
// agentNAT y el cliente detrás de dialerNAT. La señalización es la de siempre.
func simPair(t *testing.T, agentNAT, dialerNAT natsim.NATConfig) *Dialer {
	t.Helper()
	punch.SweepPause = time.Millisecond
	n := natsim.New(1)
	t.Cleanup(n.Close)
	var servers []string
	for _, ip := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5", "192.0.2.6"} {
		servers = append(servers, n.STUN(ip))
	}
	listen := func(h *natsim.Host) func(uint16) (punch.Conn, error) {
		return func(port uint16) (punch.Conn, error) { return h.ListenUDP(port) }
	}

	store := signaling.NewMemStore(
		signaling.Device{Name: "pc-casa", Kind: signaling.KindPC, TokenHash: signaling.HashToken("tok-pc")},
		signaling.Device{Name: "macbook", Kind: signaling.KindPC, TokenHash: signaling.HashToken("tok-mac")},
	)
	ts := httptest.NewServer(signaling.NewServer(store))
	t.Cleanup(ts.Close)
	url := "ws" + strings.TrimPrefix(ts.URL, "http")

	l := Listen(ListenerConfig{
		Signaling:   signaling.ClientConfig{URL: url, Device: "pc-casa", Token: "tok-pc"},
		STUNServers: servers,
		ListenUDP:   listen(n.NAT(agentNAT).Host("192.168.1.10")),
		OnEvent: func(from string, s *punch.Session, e punch.Event) {
			if e.Kind == punch.EvMessage && s != nil {
				s.Send("eco: " + e.Text)
			}
		},
	})
	t.Cleanup(l.Close)
	d := NewDialer(DialerConfig{
		Signaling:   signaling.ClientConfig{URL: url, Device: "macbook", Token: "tok-mac"},
		STUNServers: servers,
		ListenUDP:   listen(n.NAT(dialerNAT).Host("10.0.0.2")),
	})
	t.Cleanup(d.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		devs, err := d.Devices(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(devs) == 2 && devs[1].Name == "pc-casa" && devs[1].Online {
			return d
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// connectAndEcho conecta y comprueba ida y vuelta de datos por la sesión.
func connectAndEcho(t *testing.T, d *Dialer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msgs := make(chan string, 16)
	start := time.Now()
	s, err := d.Connect(ctx, "pc-casa", func(e punch.Event) {
		if e.Kind == punch.EvMessage {
			msgs <- e.Text
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("tardó %s: no debería esperar el reintento con el orden invertido", took)
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := s.Send("hola"); err != nil {
			t.Fatal(err)
		}
		select {
		case m := <-msgs:
			if m != "eco: hola" {
				t.Errorf("respuesta = %q", m)
			}
			return
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("no llegó el eco")
		}
	}
}

// El caso real: el Mac en el wifi de la oficina (simétrico, 2 IPs) entra al
// agente de la casa. El cliente abre los sockets y el agente barre.
func TestIterativeDialerSymmetric(t *testing.T) {
	connectAndEcho(t, simPair(t, casa, oficina))
}

// Roles invertidos: el simétrico es el agente (un PC en la oficina atendiendo
// a otro en la casa). El agente abre los sockets y el cliente barre.
func TestIterativeListenerSymmetric(t *testing.T) {
	connectAndEcho(t, simPair(t, oficina, casa))
}

// Los dos simétricos: no hay camino directo. Tiene que fallar una sola vez, con
// un mensaje que lo explique, sin el reintento con el orden invertido.
func TestBothSymmetricFailsWithReason(t *testing.T) {
	old := ConnectTimeout
	ConnectTimeout = 500 * time.Millisecond
	defer func() { ConnectTimeout = old }()

	d := simPair(t, otraSym, oficina)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err := d.Connect(ctx, "pc-casa", nil)
	if err == nil || !strings.Contains(err.Error(), "relay") {
		t.Fatalf("err = %v; quería que nombrara el relay", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("tardó %s: no debía reintentar", took)
	}
}
