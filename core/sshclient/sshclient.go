// Package sshclient abre una terminal SSH sobre un stream del canal P2P. Lo usan
// la CLI (`rda ssh`) y la APK (vía core/mobile).
package sshclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/jmbustos1/remote-dev-access/core/logx"
)

// GenerateKey crea un par ed25519. Devuelve la privada en PEM (formato OpenSSH)
// y la pública en una línea estilo authorized_keys ("ssh-ed25519 AAAA… comentario").
func GenerateKey(comment string) (privPEM, authorized string, err error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", "", err
	}
	privPEM = string(pem.EncodeToMemory(block))
	authorized, err = PublicKey(privPEM, comment)
	return privPEM, authorized, err
}

// PublicKey deriva la línea authorized_keys de una llave privada PEM.
func PublicKey(privPEM, comment string) (string, error) {
	signer, err := ssh.ParsePrivateKey([]byte(privPEM))
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if comment != "" {
		line += " " + comment
	}
	return line, nil
}

// Fingerprint de una línea authorized_keys ("SHA256:…").
func Fingerprint(authorized string) string {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorized))
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(k)
}

// Options de una terminal.
type Options struct {
	PrivateKeyPEM string
	// KnownHostKey es la llave del PC recordada de la vez anterior (línea
	// authorized_keys). Vacía = primera vez: se acepta y se informa en HostKey
	// para guardarla ("trust on first use", como el known_hosts de OpenSSH).
	KnownHostKey string
	Term         string // por defecto xterm-256color
	Cols, Rows   int    // 0 = sin pty
	Command      string // vacío = shell (el agente engancha tmux)
	// Session pide una sesión de tmux propia (vacío = la por defecto del agente).
	// Solo aplica sin Command.
	Session string
	Stdout  io.Writer
	Stderr  io.Writer // por defecto = Stdout
}

// Terminal es una sesión SSH abierta.
type Terminal struct {
	client  *ssh.Client
	sess    *ssh.Session
	stdin   io.WriteCloser
	hostKey string
}

// Open hace el handshake SSH sobre conn, autentica con la llave y arranca la shell.
func Open(conn net.Conn, o Options) (*Terminal, error) {
	log := logx.For(logx.StageSSH)
	signer, err := ssh.ParsePrivateKey([]byte(o.PrivateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("llave privada inválida: %w", err)
	}
	t := &Terminal{}
	cfg := &ssh.ClientConfig{
		User: "rda",
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
			t.hostKey = line
			fp := ssh.FingerprintSHA256(key)
			if o.KnownHostKey == "" {
				log.Info("llave del PC (primera vez: se recuerda)", "fingerprint", fp)
				return nil
			}
			known, _, _, _, err := ssh.ParseAuthorizedKey([]byte(o.KnownHostKey))
			if err != nil || string(known.Marshal()) != string(key.Marshal()) {
				log.Error("¡la llave del PC cambió! posible suplantación; conexión rechazada", "fingerprint", fp)
				return fmt.Errorf("la llave del PC cambió (ahora %s): si reinstalaste el agente, olvida la llave en la app", fp)
			}
			log.Info("llave del PC verificada", "fingerprint", fp)
			return nil
		},
		ClientVersion: "SSH-2.0-rda",
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, "rda", cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	t.client = ssh.NewClient(c, chans, reqs)
	fail := func(err error) (*Terminal, error) { t.client.Close(); return nil, err }

	if t.sess, err = t.client.NewSession(); err != nil {
		return fail(err)
	}
	t.sess.Stdout = o.Stdout
	t.sess.Stderr = o.Stderr
	if t.sess.Stderr == nil {
		t.sess.Stderr = o.Stdout
	}
	if t.stdin, err = t.sess.StdinPipe(); err != nil {
		return fail(err)
	}
	if o.Cols > 0 && o.Rows > 0 {
		term := o.Term
		if term == "" {
			term = "xterm-256color"
		}
		modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 115200, ssh.TTY_OP_OSPEED: 115200}
		if err := t.sess.RequestPty(term, o.Rows, o.Cols, modes); err != nil {
			return fail(err)
		}
	}
	t.sess.Setenv("LANG", "es_CL.UTF-8")
	if o.Session != "" && o.Command == "" {
		// Mismo nombre que sshserver.SessionEnv (no se importa: el servidor trae pty).
		// Un agente antiguo lo ignora y engancha su sesión por defecto.
		if err := t.sess.Setenv("RDA_TMUX", o.Session); err != nil {
			return fail(fmt.Errorf("el PC rechazó la sesión tmux %q (nombre inválido): %w", o.Session, err))
		}
	}
	if o.Command != "" {
		err = t.sess.Start(o.Command)
	} else {
		err = t.sess.Shell()
	}
	if err != nil {
		return fail(err)
	}
	log.Info("terminal abierta", "cols", o.Cols, "rows", o.Rows, "cmd", o.Command)
	return t, nil
}

// HostKey es la llave del PC vista en el handshake (para recordarla).
func (t *Terminal) HostKey() string { return t.hostKey }

func (t *Terminal) Write(p []byte) (int, error) { return t.stdin.Write(p) }

func (t *Terminal) Resize(cols, rows int) error { return t.sess.WindowChange(rows, cols) }

// Wait bloquea hasta que termina el programa remoto o se corta la conexión.
func (t *Terminal) Wait() error { return t.sess.Wait() }

func (t *Terminal) Close() error {
	t.sess.Close()
	return t.client.Close()
}
