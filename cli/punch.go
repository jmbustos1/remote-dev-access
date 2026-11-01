package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/logx"
	"github.com/jmbustos1/remote-dev-access/core/punch"
	"github.com/jmbustos1/remote-dev-access/core/stun"
)

func cmdPunch(args []string) int {
	fs := flag.NewFlagSet("punch", flag.ExitOnError)
	peer := fs.String("peer", "", "dirección pública del otro lado (ip:puerto); si se omite, se pide por teclado")
	verbose := fs.Bool("v", false, "logs detallados (cada PING/PONG)")
	fs.Parse(args)
	logx.Setup(os.Stderr, *verbose)

	var connected atomic.Bool
	s, err := punch.Open(context.Background(), punch.Config{
		STUNServers: stun.DefaultServers,
		OnEvent: func(e punch.Event) {
			switch e.Kind {
			case punch.EvPublic:
				fmt.Printf("\n  Tu dirección pública:  %s   ← pásasela al otro lado\n\n", e.Addr)
			case punch.EvPunching:
				fmt.Printf("→ disparando hacia %s ...\n", e.Addr)
			case punch.EvInbound:
				fmt.Printf("← llegó el primer paquete desde %s\n", e.Addr)
			case punch.EvConnected:
				connected.Store(true)
				fmt.Printf("\n✓ CONECTADO DIRECTO con %s (RTT %s)\n  Escribe y presiona Enter para chatear. Ctrl+C para salir.\n\n",
					e.Addr, e.RTT.Round(time.Millisecond))
			case punch.EvMessage:
				fmt.Printf("otro> %s\n", e.Text)
			case punch.EvLost:
				connected.Store(false)
				fmt.Println("✗ conexión perdida, reintentando con keepalive...")
			case punch.EvFailed:
				fmt.Printf("✗ falló: %s\n", e.Text)
			}
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer s.Close()
	fmt.Printf("  (misma red local: %s)\n", s.Local())

	if *peer != "" {
		if err := s.Connect(*peer); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	} else {
		fmt.Println("Pega la dirección del otro lado y presiona Enter (mientras tanto mantengo la puerta abierta):")
	}

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

	started := *peer != ""
	for {
		select {
		case <-sig:
			fmt.Println()
			return 0
		case line, ok := <-lines:
			if !ok {
				return 0
			}
			line = strings.TrimSpace(line)
			switch {
			case line == "":
			case !started && !connected.Load():
				if err := s.Connect(line); err != nil {
					fmt.Println("✗", err)
					continue
				}
				started = true
			case connected.Load():
				if err := s.Send(line); err != nil {
					fmt.Println("✗", err)
				}
			default:
				fmt.Println("(todavía conectando, espera el ✓)")
			}
		}
	}
}
