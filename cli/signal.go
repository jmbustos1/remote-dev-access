package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/logx"
	"github.com/jmbustos1/remote-dev-access/core/punch"
	"github.com/jmbustos1/remote-dev-access/core/rendezvous"
	"github.com/jmbustos1/remote-dev-access/core/signaling"
)

// signalingFlags agrega -url, -device y -token (con fallback a RDA_URL, etc.).
func signalingFlags(fs *flag.FlagSet) func() signaling.ClientConfig {
	url := fs.String("url", os.Getenv("RDA_URL"), "URL de la señalización; env RDA_URL")
	device := fs.String("device", os.Getenv("RDA_DEVICE"), "nombre de este dispositivo; env RDA_DEVICE")
	token := fs.String("token", os.Getenv("RDA_TOKEN"), "token de este dispositivo; env RDA_TOKEN")
	return func() signaling.ClientConfig {
		if *url == "" || *device == "" || *token == "" {
			fmt.Fprintln(os.Stderr, "faltan -url, -device o -token (o RDA_URL, RDA_DEVICE, RDA_TOKEN)")
			os.Exit(2)
		}
		return signaling.ClientConfig{URL: *url, Device: *device, Token: *token}
	}
}

// rda devices: lista los dispositivos registrados y si están en línea.
func cmdDevices(args []string) int {
	fs := flag.NewFlagSet("devices", flag.ExitOnError)
	cfg := signalingFlags(fs)
	verbose := fs.Bool("v", false, "logs detallados")
	fs.Parse(args)
	logx.Setup(os.Stderr, *verbose)

	d := rendezvous.NewDialer(rendezvous.DialerConfig{Signaling: cfg()})
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	devs, err := d.Devices(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	for _, dv := range devs {
		state := "desconectado"
		if dv.Online {
			state = "EN LÍNEA"
		}
		fmt.Printf("  %-16s %-6s %s\n", dv.Name, dv.Kind, state)
	}
	return 0
}

// rda connect <pc>: señalización + hole punching + chat, sin copiar direcciones.
func cmdConnect(args []string) int {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	cfg := signalingFlags(fs)
	useUPnP := fs.Bool("upnp", false, "pedir puerto UPnP también de este lado")
	verbose := fs.Bool("v", false, "logs detallados")
	fs.Parse(args)
	logx.Setup(os.Stderr, *verbose)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "uso: rda connect [flags] <pc>")
		return 2
	}
	pc := fs.Arg(0)

	d := rendezvous.NewDialer(rendezvous.DialerConfig{Signaling: cfg(), UPnP: *useUPnP})
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	start := time.Now()
	s, err := d.Connect(ctx, pc, func(e punch.Event) {
		switch e.Kind {
		case punch.EvMessage:
			fmt.Printf("%s> %s\n", pc, e.Text)
		case punch.EvLost:
			fmt.Println("✗ conexión perdida, reintentando con keepalive...")
		case punch.EvConnected:
			// la primera conexión la informa Connect; esto cubre las reconexiones
		}
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer s.Close()
	fmt.Printf("✓ conectado con %s en %s. Escribe y presiona Enter. Ctrl+C para salir.\n",
		pc, time.Since(start).Round(time.Millisecond))

	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	for {
		select {
		case <-sig:
			return 0
		case line, ok := <-lines:
			if !ok {
				return 0
			}
			if line = strings.TrimSpace(line); line != "" {
				if err := s.Send(line); err != nil {
					fmt.Println("✗", err)
				}
			}
		}
	}
}

// rda token: genera un token nuevo y su hash (lo que guarda el servidor).
func cmdToken(args []string) int {
	b := make([]byte, 32)
	rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	fmt.Printf("token: %s\nhash:  %s\n", tok, signaling.HashToken(tok))
	return 0
}

// rda signal-local: servidor de señalización en memoria para desarrollo.
func cmdSignalLocal(args []string) int {
	fs := flag.NewFlagSet("signal-local", flag.ExitOnError)
	listen := fs.String("listen", ":8790", "dirección donde escuchar")
	var devices multiFlag
	fs.Var(&devices, "device", "nombre:tipo:token (repetible), p. ej. pc-casa:pc:secreto")
	verbose := fs.Bool("v", false, "logs detallados")
	fs.Parse(args)
	logx.Setup(os.Stderr, *verbose)

	var devs []signaling.Device
	for _, d := range devices {
		parts := strings.SplitN(d, ":", 3)
		if len(parts) != 3 {
			fmt.Fprintf(os.Stderr, "dispositivo inválido %q (formato nombre:tipo:token)\n", d)
			return 2
		}
		devs = append(devs, signaling.Device{Name: parts[0], Kind: parts[1], TokenHash: signaling.HashToken(parts[2])})
	}
	if len(devs) == 0 {
		fmt.Fprintln(os.Stderr, "agrega al menos un -device")
		return 2
	}
	fmt.Printf("señalización local en ws://%s\n", *listen)
	if err := http.ListenAndServe(*listen, signaling.NewServer(signaling.NewMemStore(devs...))); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
