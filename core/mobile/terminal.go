package mobile

import (
	"context"
	"sync"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/sshclient"
	"github.com/jmbustos1/remote-dev-access/core/tunnel"
)

// SSHKey es un par de llaves recién generado.
type SSHKey struct {
	PrivatePEM  string // se guarda cifrada en el celular; nunca sale de él
	PublicKey   string // "ssh-ed25519 AAAA… comentario": va al authorized_keys del PC
	Fingerprint string
}

// GenerateSSHKey crea la llave SSH del celular.
func GenerateSSHKey(comment string) (*SSHKey, error) {
	priv, pub, err := sshclient.GenerateKey(comment)
	if err != nil {
		return nil, err
	}
	return &SSHKey{PrivatePEM: priv, PublicKey: pub, Fingerprint: sshclient.Fingerprint(pub)}, nil
}

// PublicKeyOf deriva la llave pública de una privada guardada.
func PublicKeyOf(privPEM, comment string) (string, error) {
	return sshclient.PublicKey(privPEM, comment)
}

// Fingerprint de una llave pública ("SHA256:…").
func Fingerprint(pub string) string { return sshclient.Fingerprint(pub) }

// TerminalListener lo implementa Kotlin para recibir la pantalla remota.
type TerminalListener interface {
	OnOutput(data []byte)
	// OnExit: la terminal terminó (el programa salió o se cortó el canal).
	OnExit(reason string)
}

// Terminal es una sesión SSH con tmux en el PC.
type Terminal struct {
	t    *sshclient.Terminal
	once sync.Once
	l    TerminalListener
}

type listenerWriter struct{ l TerminalListener }

func (w listenerWriter) Write(p []byte) (int, error) {
	w.l.OnOutput(append([]byte(nil), p...))
	return len(p), nil
}

// tunnelConn devuelve el canal QUIC de la sesión, abriéndolo la primera vez (o
// de nuevo si el anterior murió). Varias terminales comparten el mismo canal.
func (p *PunchSession) tunnelConn(ctx context.Context) (*tunnel.Conn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tc != nil {
		select {
		case <-p.tc.Done():
			p.tc = nil
		default:
			return p.tc, nil
		}
	}
	c, err := tunnel.Dial(ctx, p.s)
	if err != nil {
		return nil, err
	}
	p.tc = c
	return c, nil
}

// OpenTerminal abre una terminal en el PC: canal QUIC → SSH → tmux. Bloquea
// unos segundos como máximo: llamarlo fuera del hilo de UI.
//
// knownHostKey: llave del PC recordada ("" la primera vez); tras conectar,
// HostKey() devuelve la que presentó el PC para guardarla. command vacío =
// tmux (la sesión que configure el agente).
func (p *PunchSession) OpenTerminal(privPEM, knownHostKey string, cols, rows int, command string, l TerminalListener) (*Terminal, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tc, err := p.tunnelConn(ctx)
	if err != nil {
		return nil, err
	}
	st, err := tc.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	t, err := sshclient.Open(st, sshclient.Options{
		PrivateKeyPEM: privPEM,
		KnownHostKey:  knownHostKey,
		Cols:          cols,
		Rows:          rows,
		Command:       command,
		Stdout:        listenerWriter{l},
	})
	if err != nil {
		return nil, err
	}
	term := &Terminal{t: t, l: l}
	go func() {
		reason := "la terminal terminó"
		done := make(chan error, 1)
		go func() { done <- t.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				reason = err.Error()
			}
		case <-tc.Done():
			reason = "se cortó el canal con el PC"
		}
		term.exit(reason)
	}()
	return term, nil
}

func (t *Terminal) exit(reason string) {
	t.once.Do(func() {
		t.t.Close()
		t.l.OnExit(reason)
	})
}

// HostKey es la llave que presentó el PC (para recordarla).
func (t *Terminal) HostKey() string { return t.t.HostKey() }

// HostFingerprint es la huella de la llave del PC, para mostrarla.
func (t *Terminal) HostFingerprint() string { return sshclient.Fingerprint(t.t.HostKey()) }

// Write manda teclas al PC.
func (t *Terminal) Write(data []byte) error {
	_, err := t.t.Write(data)
	return err
}

// Resize avisa el nuevo tamaño de la pantalla (columnas × filas).
func (t *Terminal) Resize(cols, rows int) error { return t.t.Resize(cols, rows) }

// Close cierra la terminal (tmux sigue vivo en el PC).
func (t *Terminal) Close() { t.exit("cerrada por el usuario") }
