package tunnel_test

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/punch"
	"github.com/jmbustos1/remote-dev-access/core/sshclient"
	"github.com/jmbustos1/remote-dev-access/core/sshserver"
	"github.com/jmbustos1/remote-dev-access/core/tunnel"
)

// syncBuf junta la salida de la terminal (la escribe otra goroutine).
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func waitOutput(t *testing.T, out *syncBuf, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("no apareció %q en la salida:\n%s", want, out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// connectedPair arma dos sesiones de punching conectadas por loopback.
func connectedPair(t *testing.T) (pc, phone *punch.Session) {
	return pairVia(t, "127.0.0.1", "127.0.0.1")
}

// pairVia: el PC le dispara al celular por pcTo y el celular al PC por phoneTo.
// Con direcciones distintas cada lado queda con un camino propio (asimétrico).
func pairVia(t *testing.T, pcTo, phoneTo string) (pc, phone *punch.Session) {
	t.Helper()
	ready := make(chan struct{}, 2)
	open := func() *punch.Session {
		s, err := punch.Open(context.Background(), punch.Config{OnEvent: func(e punch.Event) {
			if e.Kind == punch.EvConnected {
				ready <- struct{}{}
			}
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		return s
	}
	pc, phone = open(), open()
	lo := func(ip string, s *punch.Session) string {
		return netip.AddrPortFrom(netip.MustParseAddr(ip), s.Local().Port()).String()
	}
	pc.Connect(lo(pcTo, phone))
	phone.Connect(lo(phoneTo, pc))
	for range 2 {
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			t.Fatal("el punching por loopback no conectó")
		}
	}
	return pc, phone
}

// Celular → QUIC → SSH → pty → programa, y vuelta. Es la tubería completa de la fase 4.
func TestTerminalOverPunchedSocket(t *testing.T) {
	pc, phone := connectedPair(t)
	dir := t.TempDir()

	hostKey, err := sshserver.LoadOrCreateHostKey(filepath.Join(dir, "host"))
	if err != nil {
		t.Fatal(err)
	}
	priv, pub, err := sshclient.GenerateKey("celular-test")
	if err != nil {
		t.Fatal(err)
	}
	authFile := filepath.Join(dir, "authorized_keys")
	if _, added, err := sshserver.Authorize(authFile, pub); err != nil || !added {
		t.Fatalf("Authorize: added=%v err=%v", added, err)
	}
	// Programa de prueba en lugar de tmux: muestra el tamaño de la pty y hace eco.
	srv := sshserver.New(sshserver.Config{
		HostKey: hostKey, AuthorizedKeysFile: authFile,
		Command: []string{"sh", "-c", "stty size; echo listo; while read l; do echo \"eco:$l\"; done"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tunnel.Serve(ctx, pc, func(st tunnel.Stream) { srv.Handle(st) })

	conn, err := tunnel.Dial(ctx, phone)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	st, err := conn.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := &syncBuf{}
	term, err := sshclient.Open(st, sshclient.Options{PrivateKeyPEM: priv, Cols: 100, Rows: 30, Stdout: out})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()

	waitOutput(t, out, "30 100")
	waitOutput(t, out, "listo")
	term.Write([]byte("hola\n"))
	waitOutput(t, out, "eco:hola")
	if term.HostKey() == "" {
		t.Error("no se informó la llave del PC")
	}

	// Segunda conexión recordando la llave: pasa. Con otra llave: se rechaza.
	known := term.HostKey()
	st2, _ := conn.OpenStream(ctx)
	t2, err := sshclient.Open(st2, sshclient.Options{PrivateKeyPEM: priv, KnownHostKey: known, Stdout: &syncBuf{}})
	if err != nil {
		t.Fatalf("con la llave conocida: %v", err)
	}
	t2.Close()
	_, otherPub, _ := sshclient.GenerateKey("otro")
	st3, _ := conn.OpenStream(ctx)
	if _, err := sshclient.Open(st3, sshclient.Options{PrivateKeyPEM: priv, KnownHostKey: otherPub, Stdout: &syncBuf{}}); err == nil {
		t.Error("aceptó una llave de PC distinta a la recordada")
	}
}

func TestUnauthorizedKeyRejected(t *testing.T) {
	pc, phone := connectedPair(t)
	dir := t.TempDir()
	hostKey, _ := sshserver.LoadOrCreateHostKey(filepath.Join(dir, "host"))
	authFile := filepath.Join(dir, "authorized_keys")
	os.WriteFile(authFile, nil, 0o600)
	srv := sshserver.New(sshserver.Config{HostKey: hostKey, AuthorizedKeysFile: authFile, Command: []string{"true"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tunnel.Serve(ctx, pc, func(st tunnel.Stream) { srv.Handle(st) })

	conn, err := tunnel.Dial(ctx, phone)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	st, _ := conn.OpenStream(ctx)
	priv, _, _ := sshclient.GenerateKey("intruso")
	if _, err := sshclient.Open(st, sshclient.Options{PrivateKeyPEM: priv, Stdout: &syncBuf{}}); err == nil {
		t.Fatal("una llave no autorizada abrió una terminal")
	}
}

// Regresión: cada lado eligió un camino distinto (uno por IPv4, el otro por IPv6)
// y QUIC no conectaba porque solo se aceptaban datos desde el camino propio.
func TestQUICOverAsymmetricPaths(t *testing.T) {
	pc, phone := pairVia(t, "127.0.0.1", "::1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	accepted := make(chan struct{}, 1)
	go tunnel.Serve(ctx, pc, func(st tunnel.Stream) { accepted <- struct{}{}; st.Close() })
	conn, err := tunnel.Dial(ctx, phone)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	st, err := conn.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.Write([]byte("x")) // el stream llega al PC con el primer byte
	select {
	case <-accepted:
	case <-ctx.Done():
		t.Fatal("el PC nunca recibió el stream")
	}
}
