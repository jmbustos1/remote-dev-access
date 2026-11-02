// rda es el cliente de prueba: corre en la laptop o en Termux.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/logx"
	"github.com/jmbustos1/remote-dev-access/core/stun"
)

var version = "dev"

const usage = `rda — cliente de remote-dev-access

Uso:
  rda stun [flags]    pregunta a servidores STUN cómo te ve internet
  rda punch [flags]   hole punching manual + mini chat P2P
  rda devices         lista los dispositivos de la señalización
  rda connect <pc>    conecta con un PC vía señalización + chat
  rda ssh <pc> [cmd]  terminal en el PC (P2P → QUIC → SSH → tmux)
  rda keygen          crea la llave SSH de este dispositivo
  rda token           genera un token de dispositivo y su hash
  rda signal-local    servidor de señalización en memoria (desarrollo)
  rda version

Ejecuta "rda <comando> -h" para ver los flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "stun":
		os.Exit(cmdSTUN(os.Args[2:]))
	case "punch":
		os.Exit(cmdPunch(os.Args[2:]))
	case "devices":
		os.Exit(cmdDevices(os.Args[2:]))
	case "connect":
		os.Exit(cmdConnect(os.Args[2:]))
	case "ssh":
		os.Exit(cmdSSH(os.Args[2:]))
	case "keygen":
		os.Exit(cmdKeygen(os.Args[2:]))
	case "token":
		os.Exit(cmdToken(os.Args[2:]))
	case "signal-local":
		os.Exit(cmdSignalLocal(os.Args[2:]))
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func cmdSTUN(args []string) int {
	fs := flag.NewFlagSet("stun", flag.ExitOnError)
	servers := fs.String("servers", strings.Join(stun.DefaultServers, ","), "servidores STUN separados por coma")
	port := fs.Int("port", 0, "puerto UDP local (0 = el que asigne el sistema)")
	timeout := fs.Duration("timeout", 2*time.Second, "espera por respuesta, por intento")
	verbose := fs.Bool("v", false, "logs detallados (nivel debug)")
	fs.Parse(args)
	logx.Setup(os.Stderr, *verbose)

	rep, err := stun.Probe(context.Background(), strings.Split(*servers, ","), *port, *timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	fmt.Printf("Socket local:  %s\n\n", rep.Local)
	ok := 0
	for _, r := range rep.Results {
		if r.Err != nil {
			fmt.Printf("  %-28s  ✗ %v\n", r.Server, r.Err)
			continue
		}
		ok++
		fmt.Printf("  %-28s  te ve como %-22s (%s)\n", r.Server, r.Mapped, r.RTT.Round(time.Millisecond))
	}
	fmt.Printf("\nNAT: %s\n", rep.Kind)
	if rep.HasIPv6() {
		fmt.Println("\nIPv6:")
		for _, r := range rep.IPv6 {
			if r.Err == nil {
				fmt.Printf("  %-28s  te ve como %s\n", r.Server, r.Mapped)
			}
		}
		fmt.Println("  → hay IPv6: sin NAT de por medio, el hole punching por IPv6 debería funcionar")
	} else {
		fmt.Println("\nIPv6: no disponible en esta red")
	}
	if ok == 0 {
		return 1
	}
	return 0
}
