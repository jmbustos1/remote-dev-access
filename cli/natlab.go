package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jmbustos1/remote-dev-access/core/natlab"
)

// cmdNatlab mide cómo reparte puertos el NAT de esta red (ver core/natlab).
func cmdNatlab(args []string) int {
	fs := flag.NewFlagSet("natlab", flag.ExitOnError)
	sockets := fs.Int("sockets", 32, "cuántos sockets abrir en la parte B")
	interval := fs.Duration("interval", 100*time.Millisecond, "pausa entre sockets")
	label := fs.String("label", "", "nombre de la red para el reporte (p. ej. \"wifi casa\")")
	anon := fs.Bool("anon", false, "reemplaza nuestras IPs por LOCAL/PUB1… (para publicar el reporte)")
	fs.Parse(args)

	res, err := natlab.Run(context.Background(), natlab.Config{Sockets: *sockets, Interval: *interval, Label: *label})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if *anon {
		fmt.Print(res.TextAnon())
	} else {
		fmt.Print(res.Text())
	}
	return 0
}
