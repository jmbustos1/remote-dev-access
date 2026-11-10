# CLAUDE.md — remote-dev-access (RDA)

Terminal P2P propia: celular → PC y PC ⇄ PC, detrás de NAT y sin terceros. Arquitectura,
estado y teoría: [README.md](README.md), [PLAN.md](PLAN.md), [docs/nat-traversal.md](docs/nat-traversal.md).
**Manual de uso: [docs/operar.md](docs/operar.md)**. Léelo antes de operar otro PC.

## Cómo trabajar aquí

- Responder en **español**. El usuario aprende con explicaciones paso a paso, en términos de paquetes.
- **Commit y push solo cuando el usuario lo pida.** Sin líneas de atribución (`Co-Authored-By`,
  "Generated with"). En el clon del ThinkPad, un hook `commit-msg` las borra igual.
- **El repo es público:** nada de tokens, URL de la señalización, IPs reales ni llaves. Para los
  ejemplos, usar IPs de documentación (`203.0.113.x`, `198.51.100.x`, `2001:db8::`) o anonimizadas
  (`PUB1`, `rda natlab -anon`). Revisar el diff antes de cada commit.
- Los secretos viven fuera del repo (`~/.config/rda/`, `~/.keystores/`, `android/keystore.properties`,
  `signaling/terraform/*.tfvars|tfstate`). No imprimir tokens en el chat salvo que el usuario lo pida.
- `PENDINGS.md` es local (está en `.git/info/exclude`): pendientes operativos. Actualizarlo, nunca commitearlo.
- Verificar con `make build test`. Si cambia la app Android, probarla con la skill `probar-apk`
  antes de pasar una APK (nunca usar el emulador que el usuario tenga abierto).
- No borrar sesiones de tmux del usuario (tmux-continuum restaura las suyas, p. ej. `trabajo`).

## Operar otros PCs por el túnel

Pedidos como "pullea el repo en el Mac" o "trae tal archivo del ThinkPad" se hacen con `rda ssh`,
sin SSH del sistema (el Mac no tiene "Inicio de sesión remoto" activado).

**1. Saber dónde estás y con qué identidad conectarte** (`hostname`):

| Si estás en | Identidad de cliente | `rda` | Destinos |
|---|---|---|---|
| ThinkPad (`tecob-ThinkPad-…`) | `~/.config/rda/pc-casa-cli.agent.env` | `bin/rda` del repo | `macbook` |
| Mac (`MacBook-Pro-de-Jose…`) | `~/.config/rda/macbook-cli.agent.env` | `~/rda/rda` | `pc-casa` |

Nunca usar `~/.config/rda/agent.env` como cliente: es la identidad del agente de ese PC, y la
señalización solo guarda una conexión por dispositivo (le quitarías los mensajes al agente).

**2. Ejecutar sin terminal interactiva** (siempre `< /dev/null` si el comando no lee entrada):
```bash
(set -a; . ~/.config/rda/pc-casa-cli.agent.env; set +a; ./bin/rda ssh macbook 'cd ~/Documents/personal_projects/remote-dev-access && git status --short' < /dev/null)
```
- Cada llamada tarda ~3 s en conectar: juntar los pasos en un solo comando remoto.
- Los avisos de `rda` (`level=…`, `✓ terminal…`) salen por stderr. Para leer solo la salida remota: `2>/dev/null`
  (o `| grep -v 'level='` si se quieren ver los errores).
- Archivos: `'cat > destino' < origen` para subir y `'cat archivo' > copia` para traer. Son bytes exactos,
  sirve con binarios. No pasar secretos dentro del comando (quedan en los logs del agente): siempre por stdin.

**3. Si no conecta:** "no está en línea" o un timeout significa que el agente del destino no está
corriendo, o que ese PC se durmió. **Pedirle al usuario que lo levante** (comandos en docs/operar.md §1).
No hay otra forma de entrar.

Por el túnel, `pgrep rda-agent` en el Mac sale vacío aunque el agente esté corriendo: el agente es
ancestro del comando y el `pgrep` de macOS excluye a sus ancestros. Usar `ps -axo pid,lstart,command | grep '[r]da-agent'`.
(Si el comando llegó a ejecutarse, el agente obviamente está corriendo.)

**4. Precauciones en el otro PC:**
- Mirar antes de tocar: `git status`, cambios locales, stash, rama. Usar `git pull --ff-only`. Nada
  destructivo (`reset --hard`, borrar archivos) sin confirmación del usuario.
- Instalar binarios con copia y renombre (`cp x ~/rda/x.new && mv -f ~/rda/x.new ~/rda/x`): sobrescribir
  en el lugar un binario en ejecución puede matar al agente en macOS.
- **No reiniciar ni matar el agente remoto:** se corta el túnel y no vuelve solo. Decirle al usuario
  que lo reinicie en persona.
- No tocar sus sesiones de tmux ni su configuración (`~/.tmux.conf`, `~/.zshrc`) sin que lo pida.

Receta completa para actualizar el Mac: docs/operar.md §5.

## Mapa rápido del código

| Carpeta | Qué es |
|---|---|
| `core/punch`, `core/stun`, `core/natlab` | Hole punching, STUN, experimento NAT |
| `core/tunnel`, `core/sshserver`, `core/sshclient` | QUIC sobre el socket del punching, SSH embebido |
| `core/signaling`, `core/rendezvous`, `signaling/` | Señalización (cliente, offer/answer, Lambda + Terraform) |
| `core/mobile`, `android/` | API gomobile y la APK (Kotlin + Compose, terminal xterm.js) |
| `agent/`, `cli/` | `rda-agent` (en cada PC) y `rda` (stun, natlab, ssh, keygen…) |
| `scripts/` | `register-pc.sh`, `emu-test.sh` |
