package sshserver_test

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/sshclient"
	"github.com/jmbustos1/remote-dev-access/core/sshserver"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestValidSession(t *testing.T) {
	for name, want := range map[string]bool{
		"rda": true, "pc-casa-cli": true, "Mi_PC2": true,
		"": false, "a b": false, "x;rm -rf ~": false, "$(id)": false, "../x": false,
		strings.Repeat("a", 33): false,
	} {
		if got := sshserver.ValidSession(name); got != want {
			t.Errorf("ValidSession(%q) = %v", name, got)
		}
	}
}

// open conecta un cliente con Options o al servidor (sin Command: usa tmux, que
// en el test es echo) y devuelve lo que imprimió el programa remoto.
func open(t *testing.T, o sshclient.Options) (string, error) {
	t.Helper()
	sshserver.TmuxBin = "echo"
	t.Cleanup(func() { sshserver.TmuxBin = "tmux" })

	dir := t.TempDir()
	hostKey, err := sshserver.LoadOrCreateHostKey(filepath.Join(dir, "host"))
	if err != nil {
		t.Fatal(err)
	}
	priv, pub, err := sshclient.GenerateKey("test")
	if err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(auth, []byte(pub+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := sshserver.New(sshserver.Config{HostKey: hostKey, AuthorizedKeysFile: auth})

	// TCP local (net.Pipe no sirve: en SSH los dos lados escriben primero).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		if c, err := ln.Accept(); err == nil {
			srv.Handle(c)
		}
	}()
	b, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	out := &syncBuf{}
	o.PrivateKeyPEM, o.Stdout, o.Cols, o.Rows = priv, out, 80, 24
	term, err := sshclient.Open(b, o)
	if err != nil {
		return "", err
	}
	defer term.Close()
	done := make(chan struct{})
	go func() { term.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("el programa remoto no terminó")
	}
	return strings.TrimSpace(out.String()), nil
}

func TestSessionPerClient(t *testing.T) {
	got, err := open(t, sshclient.Options{Session: "pc-casa-cli"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "new-session -A -s pc-casa-cli" {
		t.Errorf("con sesión pedida: %q", got)
	}

	got, err = open(t, sshclient.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "new-session -A -s rda" {
		t.Errorf("sin sesión (celular): %q", got)
	}
}

func TestInvalidSessionRejected(t *testing.T) {
	if _, err := open(t, sshclient.Options{Session: "x; rm -rf ~"}); err == nil {
		t.Fatal("debería rechazar un nombre de sesión con caracteres raros")
	}
}
