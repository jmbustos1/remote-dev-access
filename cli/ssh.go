package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/jmbustos1/remote-dev-access/core/logx"
	"github.com/jmbustos1/remote-dev-access/core/rendezvous"
	"github.com/jmbustos1/remote-dev-access/core/sshclient"
	"github.com/jmbustos1/remote-dev-access/core/tunnel"
)

func rdaDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "rda")
}

// rda keygen: crea la llave SSH de este dispositivo (lo mismo que hace la app).
func cmdKeygen(args []string) int {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	path := fs.String("key", filepath.Join(rdaDir(), "id_ed25519"), "dónde guardar la llave privada")
	comment := fs.String("comment", "", "comentario de la llave (por defecto el nombre del host)")
	fs.Parse(args)
	if _, err := os.Stat(*path); err == nil {
		fmt.Fprintf(os.Stderr, "%s ya existe; no la sobreescribo\n", *path)
		return 1
	}
	if *comment == "" {
		h, _ := os.Hostname()
		*comment = "rda@" + h
	}
	priv, pub, err := sshclient.GenerateKey(*comment)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	os.MkdirAll(filepath.Dir(*path), 0o700)
	if err := os.WriteFile(*path, []byte(priv), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	os.WriteFile(*path+".pub", []byte(pub+"\n"), 0o644)
	fmt.Printf("llave guardada en %s\nautorízala en cada PC con:\n  rda-agent -authorize '%s'\n", *path, pub)
	return 0
}

// rda ssh <pc> [comando]: terminal en el PC por el canal P2P (señalización →
// hole punching → QUIC → SSH). Sin comando, el agente engancha tmux.
func cmdSSH(args []string) int {
	fs := flag.NewFlagSet("ssh", flag.ExitOnError)
	cfg := signalingFlags(fs)
	keyPath := fs.String("key", filepath.Join(rdaDir(), "id_ed25519"), "llave privada (crearla con rda keygen)")
	useUPnP := fs.Bool("upnp", false, "pedir puerto UPnP también de este lado")
	verbose := fs.Bool("v", false, "logs detallados")
	fs.Parse(args)
	logx.Setup(os.Stderr, *verbose)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "uso: rda ssh [flags] <pc> [comando]")
		return 2
	}
	pc, command := fs.Arg(0), strings.Join(fs.Args()[1:], " ")
	priv, err := os.ReadFile(*keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "no hay llave en %s: créala con `rda keygen`\n", *keyPath)
		return 1
	}
	knownPath := filepath.Join(rdaDir(), "known_hosts")
	known := readKnown(knownPath, pc)

	d := rendezvous.NewDialer(rendezvous.DialerConfig{Signaling: cfg(), UPnP: *useUPnP})
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	start := time.Now()
	s, err := d.Connect(ctx, pc, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer s.Close()
	conn, err := tunnel.Dial(ctx, s)
	if err != nil {
		fmt.Fprintln(os.Stderr, "canal QUIC:", err)
		return 1
	}
	defer conn.Close()
	st, err := conn.OpenStream(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "stream:", err)
		return 1
	}

	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	o := sshclient.Options{PrivateKeyPEM: string(priv), KnownHostKey: known, Command: command, Stdout: os.Stdout, Stderr: os.Stderr}
	if interactive {
		o.Cols, o.Rows, _ = term.GetSize(int(os.Stdout.Fd()))
		o.Term = os.Getenv("TERM")
	}
	t, err := sshclient.Open(st, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ssh:", err)
		return 1
	}
	defer t.Close()
	if known == "" {
		appendKnown(knownPath, pc, t.HostKey())
	}
	fmt.Fprintf(os.Stderr, "✓ terminal en %s en %s\r\n", pc, time.Since(start).Round(time.Millisecond))

	// De aquí en adelante la pantalla es del programa remoto: solo avisos.
	if !*verbose {
		logx.Setup(io.Discard, false)
	}
	if interactive {
		old, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err == nil {
			defer term.Restore(int(os.Stdin.Fd()), old)
		}
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		go func() {
			for range winch {
				if c, r, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
					t.Resize(c, r)
				}
			}
		}()
	}
	go io.Copy(t, os.Stdin)

	wait := make(chan error, 1)
	go func() { wait <- t.Wait() }()
	select {
	case err = <-wait:
	case <-conn.Done():
		err = errors.New("se cortó el canal con el PC")
	}
	if err != nil {
		var exit interface{ ExitStatus() int }
		if errors.As(err, &exit) {
			return exit.ExitStatus()
		}
		fmt.Fprintln(os.Stderr, "\r\nconexión terminada:", err)
		return 1
	}
	return 0
}

// known_hosts propio: una línea "pc ssh-ed25519 AAAA…" por PC.
func readKnown(path, pc string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if name, key, ok := strings.Cut(sc.Text(), " "); ok && name == pc {
			return key
		}
	}
	return ""
}

func appendKnown(path, pc, key string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", pc, key)
}
