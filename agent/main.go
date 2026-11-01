// rda-agent corre en cada PC y acepta conexiones P2P hacia su sshd.
// Fase 0: solo esqueleto; la lógica llega en las fases 3 y 4.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/jmbustos1/remote-dev-access/core/logx"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "imprime la versión y sale")
	verbose := flag.Bool("v", false, "logs detallados (nivel debug)")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	logx.Setup(os.Stderr, *verbose)
	logx.For(logx.StageSignaling).Info("rda-agent aún no implementado (fase 3)", "version", version)
}
