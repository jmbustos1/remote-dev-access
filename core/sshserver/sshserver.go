// Package sshserver es un servidor SSH mínimo que vive dentro del agente.
//
// ¿Por qué no el sshd del sistema? Porque no hace falta instalarlo ni abrir el
// puerto 22: este servidor no escucha en ninguna interfaz de red, solo atiende
// los streams que llegan por el canal P2P (paquete tunnel). Corre con el mismo
// usuario que el agente y su única función es darle una terminal (pty) con tmux.
//
// Autenticación: solo llave pública. Las llaves autorizadas están en un archivo
// estilo authorized_keys (por defecto ~/.config/rda/authorized_keys), que se relee
// en cada intento: autorizar un celular nuevo no requiere reiniciar el agente.
package sshserver

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"

	"github.com/jmbustos1/remote-dev-access/core/logx"
)

// DefaultSession es la sesión de tmux que engancha un cliente que pide una
// "shell" sin elegir sesión (el celular). Al cortarse la conexión, tmux sigue vivo.
var DefaultSession = "rda"

// SessionEnv es la variable que el cliente manda (pedido "env" de SSH) para
// elegir su propia sesión de tmux en vez de la por defecto. Así cada dispositivo
// tiene la suya (el celular "rda", un PC la de su nombre) y cualquiera puede
// entrar a la de otro pidiéndola.
const SessionEnv = "RDA_TMUX"

// TmuxBin es el ejecutable de tmux (los tests lo cambian por echo).
var TmuxBin = "tmux"

// TmuxCommand engancha (o crea) la sesión de tmux name.
func TmuxCommand(name string) []string {
	return []string{TmuxBin, "new-session", "-A", "-s", name}
}

// ValidSession dice si name es aceptable como nombre de sesión: solo letras,
// números, '-' y '_' (va como argumento de tmux, nunca por una shell).
func ValidSession(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Config del servidor.
type Config struct {
	HostKey            ssh.Signer
	AuthorizedKeysFile string
	// Command reemplaza a tmux (tests).
	Command []string
}

// Server atiende conexiones SSH ya establecidas (no escucha por sí mismo).
type Server struct {
	cfg  Config
	sshc *ssh.ServerConfig
}

func New(cfg Config) *Server {
	s := &Server{cfg: cfg}
	s.sshc = &ssh.ServerConfig{PublicKeyCallback: s.checkKey}
	s.sshc.AddHostKey(cfg.HostKey)
	return s
}

func (s *Server) checkKey(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	log := logx.For(logx.StageSSH)
	fp := ssh.FingerprintSHA256(key)
	data, err := os.ReadFile(s.cfg.AuthorizedKeysFile)
	if err != nil {
		log.Warn("no se pudo leer authorized_keys", "file", s.cfg.AuthorizedKeysFile, "err", err)
		return nil, errors.New("sin llaves autorizadas")
	}
	for len(data) > 0 {
		k, comment, _, rest, err := ssh.ParseAuthorizedKey(data)
		if err != nil {
			break
		}
		data = rest
		if bytes.Equal(k.Marshal(), key.Marshal()) {
			log.Info("llave autorizada", "fingerprint", fp, "comentario", comment)
			return &ssh.Permissions{Extensions: map[string]string{"comment": comment}}, nil
		}
	}
	log.Warn("llave NO autorizada (agrégala con: rda-agent -authorize '<llave pública>')", "fingerprint", fp)
	return nil, fmt.Errorf("llave %s no autorizada", fp)
}

// Handle hace el handshake SSH sobre conn y atiende sus canales. Bloquea.
func (s *Server) Handle(conn net.Conn) {
	log := logx.For(logx.StageSSH)
	sc, chans, reqs, err := ssh.NewServerConn(conn, s.sshc)
	if err != nil {
		log.Warn("handshake SSH falló", "err", err)
		conn.Close()
		return
	}
	defer sc.Close()
	log.Info("sesión SSH iniciada", "cliente", string(sc.ClientVersion()), "llave", sc.Permissions.Extensions["comment"])
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "solo sesiones")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go s.session(ch, creqs)
	}
	log.Info("sesión SSH terminada")
}

type ptyReq struct {
	Term          string
	Cols, Rows    uint32
	Width, Height uint32
	Modes         string
}

type winChange struct {
	Cols, Rows    uint32
	Width, Height uint32
}

func (s *Server) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	log := logx.For(logx.StageSSH)
	defer ch.Close()

	var (
		mu      sync.Mutex
		term    = "xterm-256color"
		size    *pty.Winsize
		env     []string
		session string // sesión de tmux pedida con SessionEnv ("" = la por defecto)
		ptmx    *os.File
		started bool
		done    = make(chan struct{})
	)

	start := func(argv []string) error {
		home, _ := os.UserHomeDir()
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = home
		cmd.Env = append(os.Environ(), env...)
		cmd.Env = append(cmd.Env, "TERM="+term, "RDA_SESSION=1")
		log.Info("ejecutando", "cmd", strings.Join(argv, " "), "pty", size != nil)

		if size == nil {
			// Sin pty (p. ej. `rda ssh pc -- comando` sin terminal): tuberías simples.
			// stdin por StdinPipe y no cmd.Stdin = ch: con cmd.Stdin, Wait esperaría
			// a que el cliente cierre su entrada y la sesión nunca terminaría.
			cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
			in, err := cmd.StdinPipe()
			if err != nil {
				return err
			}
			if err := cmd.Start(); err != nil {
				return err
			}
			go func() { io.Copy(in, ch); in.Close() }()
			go func() { exitStatus(ch, cmd.Wait()); close(done) }()
			return nil
		}
		f, err := pty.StartWithSize(cmd, size)
		if err != nil {
			return err
		}
		mu.Lock()
		ptmx = f
		mu.Unlock()
		go func() {
			io.Copy(f, ch) // teclado → programa
			// El cliente se fue (cerró el canal o se cortó la red): tmux recibe
			// SIGHUP, se desengancha y la sesión sigue viva para la próxima vez.
			select {
			case <-done:
			default:
				log.Info("el cliente se desconectó: desenganchando (tmux sigue vivo)")
				cmd.Process.Signal(syscall.SIGHUP)
			}
		}()
		go func() {
			io.Copy(ch, f) // pantalla → celular (termina cuando el programa cierra la pty)
			exitStatus(ch, cmd.Wait())
			close(done)
		}()
		go func() {
			<-done
			f.Close()
		}()
		return nil
	}

	for {
		select {
		case <-done:
			return
		case req, ok := <-reqs:
			if !ok {
				if started {
					<-done // el cliente cerró su lado: esperar a que termine el programa
				}
				return
			}
			ok = true
			switch req.Type {
			case "pty-req":
				var p ptyReq
				if err := ssh.Unmarshal(req.Payload, &p); err != nil {
					ok = false
					break
				}
				if p.Term != "" {
					term = p.Term
				}
				size = &pty.Winsize{Cols: uint16(p.Cols), Rows: uint16(p.Rows)}
			case "env":
				var kv struct{ Name, Value string }
				switch {
				case ssh.Unmarshal(req.Payload, &kv) != nil:
					ok = false
				case kv.Name == SessionEnv:
					if ok = ValidSession(kv.Value); ok {
						session = kv.Value
					} else {
						log.Warn("nombre de sesión tmux inválido", "valor", kv.Value)
					}
				case kv.Name == "LANG" || strings.HasPrefix(kv.Name, "LC_"):
					env = append(env, kv.Name+"="+kv.Value)
				}
			case "window-change":
				var w winChange
				if ssh.Unmarshal(req.Payload, &w) == nil {
					mu.Lock()
					if ptmx != nil {
						pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(w.Cols), Rows: uint16(w.Rows)})
					}
					mu.Unlock()
					log.Debug("tamaño de terminal", "cols", w.Cols, "rows", w.Rows)
				}
			case "shell", "exec":
				if started {
					ok = false
					break
				}
				argv := s.cfg.Command
				if argv == nil {
					name := DefaultSession
					if session != "" {
						name = session
					}
					argv = TmuxCommand(name)
				}
				if req.Type == "exec" {
					var c struct{ Command string }
					if ssh.Unmarshal(req.Payload, &c) != nil {
						ok = false
						break
					}
					sh := os.Getenv("SHELL")
					if sh == "" {
						sh = "/bin/sh"
					}
					argv = []string{sh, "-lc", c.Command}
				}
				if err := start(argv); err != nil {
					log.Warn("no se pudo ejecutar", "cmd", argv, "err", err)
					fmt.Fprintf(ch.Stderr(), "rda-agent: %v\r\n", err)
					ok = false
				}
				started = ok
			default:
				ok = false
			}
			if req.WantReply {
				req.Reply(ok, nil)
			}
			if (req.Type == "shell" || req.Type == "exec") && !ok {
				return
			}
		}
	}
}

func exitStatus(ch ssh.Channel, err error) {
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
		if code < 0 {
			code = 1 // terminado por una señal (p. ej. el SIGHUP de arriba)
		}
	}
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(code))
	ch.SendRequest("exit-status", false, b)
	logx.For(logx.StageSSH).Info("programa terminado", "exit", code)
}

// LoadOrCreateHostKey lee la llave del PC o la genera la primera vez (permisos 600).
// Es la identidad del PC: el celular la recuerda y avisa si cambia.
func LoadOrCreateHostKey(path string) (ssh.Signer, error) {
	if data, err := os.ReadFile(path); err == nil {
		return ssh.ParsePrivateKey(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "rda-agent host key")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	logx.For(logx.StageSSH).Info("llave del PC generada", "file", path)
	return ssh.NewSignerFromKey(priv)
}

// Authorize agrega una llave pública (formato authorized_keys) al archivo, si no estaba.
func Authorize(path, line string) (fingerprint string, added bool, err error) {
	key, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return "", false, fmt.Errorf("llave pública inválida: %w", err)
	}
	fp := ssh.FingerprintSHA256(key)
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	for rest := data; len(rest) > 0; {
		k, _, _, r, err := ssh.ParseAuthorizedKey(rest)
		if err != nil {
			break
		}
		rest = r
		if bytes.Equal(k.Marshal(), key.Marshal()) {
			return fp, false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	out := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	if comment != "" {
		out += " " + comment
	}
	_, err = fmt.Fprintln(f, out)
	return fp, err == nil, err
}
