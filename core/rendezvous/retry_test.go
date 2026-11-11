package rendezvous

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/punch"
	"github.com/jmbustos1/remote-dev-access/core/signaling"
)

// pair levanta señalización en memoria, un agente "pc-casa" y un cliente
// "celular", y espera a que el agente aparezca en línea.
func pair(t *testing.T, onSession func(), closeIdle time.Duration) (*Listener, *Dialer) {
	t.Helper()
	store := signaling.NewMemStore(
		signaling.Device{Name: "pc-casa", Kind: signaling.KindPC, TokenHash: signaling.HashToken("tok-pc")},
		signaling.Device{Name: "celular", Kind: signaling.KindPhone, TokenHash: signaling.HashToken("tok-cel")},
	)
	ts := httptest.NewServer(signaling.NewServer(store))
	t.Cleanup(ts.Close)
	url := "ws" + strings.TrimPrefix(ts.URL, "http")

	l := Listen(ListenerConfig{
		Signaling:   signaling.ClientConfig{URL: url, Device: "pc-casa", Token: "tok-pc"},
		STUNServers: []string{},
		OnSession:   func(string, *punch.Session) { onSession() },
		CloseIdle:   closeIdle,
	})
	t.Cleanup(l.Close)
	d := NewDialer(DialerConfig{
		Signaling:   signaling.ClientConfig{URL: url, Device: "celular", Token: "tok-cel"},
		STUNServers: []string{},
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
			return l, d
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Si el primer intento no conecta, el cliente reintenta con el orden
// invertido (una offer nueva: el agente abre una segunda sesión) y conecta.
func TestRetryWithReversedOrder(t *testing.T) {
	old := FirstAttempt
	FirstAttempt = 0 // el primer intento "vence" al instante: fuerza el reintento
	defer func() { FirstAttempt = old }()

	var sessions atomic.Int32
	_, d := pair(t, func() { sessions.Add(1) }, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := d.Connect(ctx, "pc-casa", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := sessions.Load(); n != 2 {
		t.Errorf("el agente abrió %d sesiones; con el reintento esperaba 2", n)
	}
}

// El agente cierra (y olvida) la sesión de un cliente que se fue.
func TestListenerClosesAbandonedSession(t *testing.T) {
	l, d := pair(t, func() {}, 300*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := d.Connect(ctx, "pc-casa", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Close() // el cliente se va sin avisar

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		n := len(l.sessions)
		l.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("el agente sigue con la sesión del cliente que se fue")
}
