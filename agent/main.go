// rda-agent corre en cada PC: queda conectado a la señalización y atiende las
// offers de los celulares abriendo una sesión de hole punching.
// Fase 3: la sesión responde con eco (la terminal llega en la fase 4).
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jmbustos1/remote-dev-access/core/logx"
	"github.com/jmbustos1/remote-dev-access/core/punch"
	"github.com/jmbustos1/remote-dev-access/core/rendezvous"
	"github.com/jmbustos1/remote-dev-access/core/signaling"
)

var version = "dev"

func main() {
	url := flag.String("url", os.Getenv("RDA_URL"), "URL de la señalización (wss://…); env RDA_URL")
	device := flag.String("device", os.Getenv("RDA_DEVICE"), "nombre de este PC; env RDA_DEVICE")
	token := flag.String("token", os.Getenv("RDA_TOKEN"), "token de este PC; env RDA_TOKEN")
	useUPnP := flag.Bool("upnp", true, "pedirle al router un puerto reenviado (UPnP)")
	showVersion := flag.Bool("version", false, "imprime la versión y sale")
	verbose := flag.Bool("v", false, "logs detallados (nivel debug)")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	logx.Setup(os.Stderr, *verbose)
	if *url == "" || *device == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "faltan -url, -device o -token (o RDA_URL, RDA_DEVICE, RDA_TOKEN)")
		os.Exit(2)
	}
	log := logx.For(logx.StageSignaling)
	log.Info("rda-agent iniciando", "version", version, "device", *device)

	l := rendezvous.Listen(rendezvous.ListenerConfig{
		Signaling: signaling.ClientConfig{URL: *url, Device: *device, Token: *token},
		UPnP:      *useUPnP,
		OnEvent: func(from string, s *punch.Session, e punch.Event) {
			switch e.Kind {
			case punch.EvConnected:
				log.Info("sesión conectada", "from", from, "peer", e.Addr, "rtt", e.RTT)
			case punch.EvMessage:
				if s != nil {
					s.Send("eco desde " + *device + ": " + e.Text)
				}
			}
		},
	})
	defer l.Close()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Info("rda-agent detenido")
}
