# Operar RDA: manual del día a día

Cómo usar la solución una vez instalada. Para instalarla en un PC nuevo, ver
[onboarding-pc.md](onboarding-pc.md). Aquí no hay secretos: viven en `~/.config/rda/` de cada equipo.

## Mapa

| Equipo | Sistema | Agente (para que entren a él) | Cliente (para entrar desde él a otros) | Binarios |
|---|---|---|---|---|
| ThinkPad | Ubuntu (GNOME) | `pc-casa` → `~/.config/rda/agent.env` | `pc-casa-cli` → `~/.config/rda/pc-casa-cli.agent.env` | `bin/` del repo |
| MacBook Pro M4 | macOS | `macbook` → `~/.config/rda/agent.env` | `macbook-cli` → `~/.config/rda/macbook-cli.agent.env` | `~/rda/` |
| Celular (Redmi 9) | Android | — | `celular` (Ajustes de la app) | APK |

El repo está en `~/Documents/personal_projects/remote-dev-access` en los dos PCs.

**Dos reglas que explican casi todo:**
1. **El agente tiene que estar corriendo en el PC al que quieres entrar.** En el PC desde el que te
   conectas no hace falta. Sin agente, ese PC no es accesible por ningún medio.
2. **La señalización guarda una conexión por dispositivo.** Un cliente nunca usa el nombre del agente
   de su propio PC (le quitaría los mensajes al agente): por eso existen `pc-casa-cli` y `macbook-cli`.

## 1. Levantar el agente

ThinkPad:
```bash
cd ~/Documents/personal_projects/remote-dev-access && set -a; . ~/.config/rda/agent.env; set +a; ./bin/rda-agent
```
Mac:
```bash
cd ~/rda && set -a; . ~/.config/rda/agent.env; set +a; ./rda-agent
```
- `-v` agrega logs detallados (PING/PONG, candidatos), útiles para diagnosticar.
- Se apaga con Ctrl+C. No arranca solo al prender el PC (a propósito: no hay systemd ni launchd).
- Para revisar si está corriendo: `pgrep -af rda-agent` (Linux) o `pgrep -fl rda-agent` (Mac). Por el
  túnel, en el Mac usar `ps -axo pid,command | grep '[r]da-agent'` (`pgrep` excluye a sus ancestros, y
  el agente lo es).

## 2. Mantener el PC despierto

Bloquear la sesión y dormir son cosas distintas. **Dormido** (suspendido), el PC se congela, se cae
la red y RDA no entra. **Bloqueado**, solo se tapa la pantalla con el PIN: el agente, tmux y la red
siguen funcionando, y RDA entra igual.

### En casa, con la tapa abierta

ThinkPad (evita la suspensión por inactividad; **no** cubre el cierre de la tapa):
```bash
gnome-session-inhibit --inhibit idle:suspend --reason "Mantener Ubuntu despierto" sleep infinity
```
Mac (evita el reposo por inactividad; **no** cubre el cierre de la tapa):
```bash
caffeinate -i
```

### Saliendo, con la tapa cerrada (bloqueado pero accesible por RDA)

**ThinkPad:** el cierre de la tapa no suspende, y al cerrarla se bloquea la sesión (al abrir pide el PIN):
```bash
systemd-inhibit --what=handle-lid-switch:sleep --who="despierto" --why="Seguir procesando con la tapa cerrada" \
  bash -c 'p=open; while sleep 2; do s=$(awk "{print \$2}" /proc/acpi/button/lid/LID/state); [ "$s" = closed ] && [ "$p" = open ] && loginctl lock-session; p=$s; done'
```
- Usa `systemd-inhibit` porque, con la tapa, logind ignora el bloqueo de `gnome-session-inhibit`
  (`LidSwitchIgnoreInhibited=yes` por defecto): con ese comando el PC se dormiría igual.
- No inhibe "idle", así que también aplica el bloqueo automático de pantalla tras la inactividad.
- Se termina con Ctrl+C.

**Mac:** macOS siempre se duerme al cerrar la tapa, salvo que se fuerce:
```bash
sudo pmset -a disablesleep 1     # al salir
sudo pmset -a disablesleep 0     # al volver (si se olvida, el Mac no se duerme nunca)
```
Para que pida contraseña al cerrarlo: Ajustes del Sistema → Pantalla de bloqueo → "Solicitar
contraseña tras activarse el salvapantallas o apagarse la pantalla" → **Inmediatamente**.

**Antes de salir:**
- [ ] Agente corriendo (`pgrep`) y en línea (en el celular: PCs → Actualizar).
- [ ] El comando de la tapa corriendo (ThinkPad) o `pmset disablesleep 1` (Mac).
- [ ] Cargador conectado y espacio para ventilar.
- [ ] Cerrar la tapa y comprobar desde el celular por 4G que entra.

## 3. Conectarse

**Celular:** PCs → Actualizar → Conectar → Abrir terminal. Entra a la sesión de tmux `rda`.

**Desde un PC**, con su identidad de cliente (el paréntesis hace que las variables no queden en tu terminal):
```bash
# desde el ThinkPad
(set -a; . ~/.config/rda/pc-casa-cli.agent.env; set +a; ~/Documents/personal_projects/remote-dev-access/bin/rda ssh macbook)
# desde el Mac
(set -a; . ~/.config/rda/macbook-cli.agent.env; set +a; ~/rda/rda ssh pc-casa)
```

Atajo opcional para `~/.bashrc` (ThinkPad) o `~/.zshrc` (Mac, cambiando la identidad y la ruta):
```bash
rdas() { (set -a; . ~/.config/rda/pc-casa-cli.agent.env; set +a; ~/Documents/personal_projects/remote-dev-access/bin/rda ssh "$@"); }
# uso: rdas macbook · rdas -tmux rda macbook · rdas macbook 'uptime' < /dev/null
```

**Sesiones de tmux:** cada dispositivo tiene la suya y sobreviven a las desconexiones.
- El celular entra a `rda`; `rda ssh` entra a la del nombre de su dispositivo (`pc-casa-cli`, `macbook-cli`).
- `rda ssh -tmux rda <pc>` entra a la del celular, para retomar algo empezado desde el teléfono.
- Salir sin cerrar la sesión: `Ctrl+b` y luego `d`.
- Si el PC se apaga, las sesiones se pierden. En el ThinkPad, tmux-resurrect las restaura al
  arrancar tmux, pero sin los programas que corrían. Para retomar Claude Code: `claude --continue`.

## 4. Comandos y archivos sin abrir una terminal

```bash
rda ssh macbook 'uptime; df -h ~' < /dev/null           # ejecutar y volver
rda ssh macbook 'cat > ~/destino.zip' < origen.zip       # subir un archivo
rda ssh macbook 'cat ~/archivo.zip' > copia.zip          # traer un archivo
rda ssh macbook 'tar -C ~/proyecto -cz .' > proyecto.tgz # traer una carpeta
```
- `< /dev/null` en los comandos sin entrada: así `rda ssh` no espera nada del teclado.
- Viajan **bytes**, no texto: sirve con binarios (probado bit a bit con 1 MB al azar). No se usa una
  terminal (pty) cuando la entrada o la salida están redirigidas.
- El comando corre en la shell de login del otro PC (`zsh -lc` en el Mac, `bash -lc` en Linux).
- Los avisos de `rda` (`level=INFO…`, `✓ terminal…`) van a stderr, no se mezclan con el archivo.
- Cada llamada tarda ~3 s en conectar: para muchos pasos, juntarlos en un solo comando.

## 5. Administrar el otro PC por el túnel

Ejemplo: actualizar el Mac desde el ThinkPad (todo con la identidad `pc-casa-cli`).

1. **Mirar antes de tocar:**
   ```bash
   rda ssh macbook 'cd ~/Documents/personal_projects/remote-dev-access && git status --short && git log -1 --oneline && git fetch -q && git log -1 --oneline origin/main' < /dev/null
   ```
2. **Bajar y compilar** (si no hay cambios locales):
   ```bash
   rda ssh macbook 'cd ~/Documents/personal_projects/remote-dev-access && git pull --ff-only && make build && go test ./...' < /dev/null
   ```
3. **Instalar los binarios en `~/rda`** con copia y renombre (sobrescribir en el lugar un binario
   que está corriendo puede matar al agente en macOS):
   ```bash
   rda ssh macbook 'cd ~/Documents/personal_projects/remote-dev-access && for b in rda rda-agent; do cp bin/$b ~/rda/$b.new && mv -f ~/rda/$b.new ~/rda/$b; done && ~/rda/rda-agent -version' < /dev/null
   ```
4. **El agente en marcha sigue con la versión vieja** hasta que se reinicie. No reiniciarlo por el
   túnel: se corta la conexión y no vuelve a arrancar solo. Se reinicia en persona (Ctrl+C y levantarlo).

## 6. Después de cambiar el código

```bash
make build test      # bin/rda y bin/rda-agent del ThinkPad
make agent-dist      # binarios para Mac (darwin-arm64) y Linux (linux-amd64) en bin/dist/
make apk             # APK firmada en bin/rda-<versión>.apk
```
- La APK nueva se instala **encima** de la anterior. Desinstalar borra la URL, el token y la llave
  SSH del celular, y hay que volver a configurarlos y autorizar la llave en cada PC.
- Probar la APK en el emulador antes de pasarla: skill `probar-apk` (`scripts/emu-test.sh`).

## 7. Dispositivos y llaves

| Tarea | Comando |
|---|---|
| Registrar un dispositivo nuevo | `scripts/register-pc.sh <nombre>` (crea `~/.config/rda/<nombre>.agent.env`) |
| Crear la llave de cliente de un PC | `rda keygen` → imprime el `-authorize` para los otros PCs |
| Autorizar una llave en un PC | `rda-agent -authorize 'ssh-ed25519 AAAA… nombre'` (no hace falta reiniciar) |
| Ver quién puede entrar a un PC | `ssh-keygen -lf ~/.config/rda/authorized_keys` |
| Ver los dispositivos registrados | `aws dynamodb scan --table-name remote-dev-access-signaling --projection-expression pk` |
| Medir la red (¿conectará directo?) | `rda natlab -anon` |

Si se pierde el celular: borrar su línea de `authorized_keys` en cada PC y rotar su token.

## 8. Problemas frecuentes

| Síntoma | Causa probable |
|---|---|
| El PC aparece "desconectado" | El agente no está corriendo, o el PC se durmió. |
| `no llegó ningún paquete del otro lado` | La red bloquea el UDP entrante. En el Mac, revisar el firewall de macOS. Comparar con `rda natlab`. |
| La terminal se ve rara, con texto en escalera | Se usó `-v` en una versión vieja: actualizar (ahora los logs van a `~/.config/rda/ssh.log`). |
| `rda ssh` se queda esperando | Falta `< /dev/null` en un comando sin entrada. |
| Entra a la sesión de tmux equivocada | El agente del destino es anterior a las sesiones por dispositivo: actualizarlo y reiniciarlo. |
| "la llave del PC cambió" | Se regeneró `host_ed25519` en ese PC: olvidar la huella en el celular (Ajustes) o borrar la línea en `~/.config/rda/known_hosts`. |
| El celular dice "Señalización: sin configurar" | Se reinstaló la app: volver a cargar URL, nombre y token. |

## 9. Respaldo

Tokens, contraseñas del keystore de la APK y el estado de Terraform están respaldados en una página
privada de Notion, para reconstruir todo si se pierde un PC. Mantenerla al día al rotar un token o
al hacer `terraform apply`.
