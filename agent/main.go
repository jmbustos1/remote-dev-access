// rda-agent corre en cada PC: queda conectado a la señalización y atiende las
// offers de los celulares abriendo una sesión de hole punching. Sobre cada sesión
// monta el canal QUIC con un servidor SSH embebido que entrega tmux (fase 4).
// Los mensajes RDA1 MSG siguen respondiéndose con eco (diagnóstico).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/jmbustos1/remote-dev-access/core/logx"
	"github.com/jmbustos1/remote-dev-access/core/punch"
	"github.com/jmbustos1/remote-dev-access/core/rendezvous"
	"github.com/jmbustos1/remote-dev-access/core/signaling"
	"github.com/jmbustos1/remote-dev-access/core/sshserver"
	"github.com/jmbustos1/remote-dev-access/core/tunnel"
)

var version = "dev"

func main() {
	home, _ := os.UserHomeDir()
	cfgDir := filepath.Join(home, ".config", "rda")

	url := flag.String("url", os.Getenv("RDA_URL"), "URL de la señalización (wss://…); env RDA_URL")
	device := flag.String("device", os.Getenv("RDA_DEVICE"), "nombre de este PC; env RDA_DEVICE")
	token := flag.String("token", os.Getenv("RDA_TOKEN"), "token de este PC; env RDA_TOKEN")
	useUPnP := flag.Bool("upnp", true, "pedirle al router un puerto reenviado (UPnP)")
	port := flag.Uint("port", envUint("RDA_PORT"), "puerto UDP fijo, para un reenvío de puerto manual en el router (0 = al azar); env RDA_PORT")
	hostKeyPath := flag.String("host-key", filepath.Join(cfgDir, "host_ed25519"), "llave SSH de este PC (se genera si no existe)")
	authKeys := flag.String("authorized-keys", filepath.Join(cfgDir, "authorized_keys"), "llaves públicas autorizadas")
	authorize := flag.String("authorize", "", "agrega esta llave pública (\"ssh-ed25519 AAAA… nombre\") a -authorized-keys y sale")
	tmuxSession := flag.String("tmux", "rda", "sesión de tmux a la que se engancha la terminal")
	showVersion := flag.Bool("version", false, "imprime la versión y sale")
	verbose := flag.Bool("v", false, "logs detallados (nivel debug)")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if *authorize != "" {
		fp, added, err := sshserver.Authorize(*authKeys, strings.TrimSpace(*authorize))
		switch {
		case err != nil:
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		case added:
			fmt.Printf("llave %s autorizada en %s\n", fp, *authKeys)
		default:
			fmt.Printf("la llave %s ya estaba autorizada\n", fp)
		}
		return
	}
	logx.Setup(os.Stderr, *verbose)
	if *port > 65535 {
		fmt.Fprintln(os.Stderr, "-port debe estar entre 1 y 65535")
		os.Exit(2)
	}
	if *url == "" || *device == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "faltan -url, -device o -token (o RDA_URL, RDA_DEVICE, RDA_TOKEN)")
		os.Exit(2)
	}
	log := logx.For(logx.StageSignaling)
	log.Info("rda-agent iniciando", "version", version, "device", *device, "port", *port)

	hostKey, err := sshserver.LoadOrCreateHostKey(*hostKeyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "llave del PC:", err)
		os.Exit(1)
	}
	if _, err := os.Stat(*authKeys); err != nil {
		log.Warn("todavía no hay llaves autorizadas: copia la llave pública desde Ajustes de la app y córrela con -authorize", "file", *authKeys)
	}
	sshserver.DefaultCommand = []string{"tmux", "new-session", "-A", "-s", *tmuxSession}
	ssh := sshserver.New(sshserver.Config{HostKey: hostKey, AuthorizedKeysFile: *authKeys})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := rendezvous.Listen(rendezvous.ListenerConfig{
		Signaling: signaling.ClientConfig{URL: *url, Device: *device, Token: *token},
		UPnP:      *useUPnP,
		Port:      uint16(*port),
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
		OnSession: func(from string, s *punch.Session) {
			// Termina cuando la sesión se cierra (reconexión o Close del agente).
			if err := tunnel.Serve(ctx, s, func(st tunnel.Stream) { ssh.Handle(st) }); err != nil && !tunnel.IsClosed(err) {
				logx.For(logx.StageTunnel).Info("canal QUIC terminado", "from", from, "motivo", err)
			}
		},
	})
	defer l.Close()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Info("rda-agent detenido")
}

// envUint lee un entero sin signo de la variable de entorno (0 si no está o no es válido).
func envUint(name string) uint {
	n, _ := strconv.ParseUint(os.Getenv(name), 10, 16)
	return uint(n)
}
