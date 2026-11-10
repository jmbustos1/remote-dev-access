# Agregar otro PC

Guía corta para sumar un PC (MacBook Pro M4, Legion 5 Pro…) y abrirlo desde el celular.
Por ahora el agente se arranca a mano en una terminal (sin systemd/launchd).

Resumen: **registrar → copiar 3 cosas → instalar tmux → autorizar el celular → correr el agente.**

## Qué hace cada paso (la idea)

1. **Registrar** = darle al PC un nombre y una contraseña (token) ante el "portero" en AWS
   (la señalización). Sin eso, el portero no lo deja entrar ni lo presenta al celular.
2. **Copiar** = llevar al PC el programa (el agente) y su contraseña.
3. **tmux** = la "sala" donde quedan vivas tus terminales aunque el celular se desconecte.
4. **Autorizar el celular** = decirle al PC "este celular es de confianza" (su llave pública).
   Es un segundo candado, distinto del token: el token es para entrar al portero; la llave
   es para abrir la terminal.
5. **Correr el agente** = el PC se queda conectado con el portero esperando que el celular
   lo llame. Cuando llamas, se abre el túnel directo entre los dos.

## 1. En pc-casa: registrar y preparar (una vez por PC)

```bash
cd ~/Documents/personal_projects/remote-dev-access
scripts/register-pc.sh macbook   # o: legion   (usa tus credenciales de AWS)
make agent-dist                  # agente para Mac (darwin-arm64) y Linux (linux-amd64)
```

`register-pc.sh` guarda el hash del token en DynamoDB y deja el archivo
`~/.config/rda/macbook.agent.env` (permisos 600). El token no se muestra en pantalla.

## 2. Copiar al PC nuevo

| Qué | Desde pc-casa | En el PC nuevo |
|---|---|---|
| El agente | `bin/dist/darwin-arm64/rda-agent` (Mac) o `bin/dist/linux-amd64/rda-agent` (Linux) | `~/rda/rda-agent` |
| Su configuración (**secreta**) | `~/.config/rda/macbook.agent.env` | `~/.config/rda/agent.env` |
| La llave del celular (pública) | `~/.config/rda/authorized_keys` | se agrega en el paso 4 |

Usa `scp`, un pendrive o AirDrop. **No** subas el `agent.env` a Drive, ni lo pegues en un
chat o en el repo: tiene el token. Si se filtra, revócalo (ver `signaling/README.md`)
y vuelve a registrar el PC.

En el PC nuevo:
```bash
mkdir -p ~/.config/rda && chmod 700 ~/.config/rda
chmod 600 ~/.config/rda/agent.env
chmod +x ~/rda/rda-agent
```

## 3. Instalar tmux

- **MacBook:** `brew install tmux`
- **Legion con Linux:** `sudo apt install tmux` (o el gestor de tu distro)

## 4. Autorizar el celular

La llave pública del celular es la misma para todos los PCs. Cópiala de pc-casa
(`cat ~/.config/rda/authorized_keys`) y en el PC nuevo:
```bash
~/rda/rda-agent -authorize 'ssh-ed25519 AAAA… rda@celular'
```

## 5. Correr el agente

```bash
cd ~/rda && set -a; . ~/.config/rda/agent.env; set +a; ./rda-agent
```

**El PC tiene que estar despierto.** Si se duerme, se corta todo. El agente no lo
evita: deja corriendo en paralelo, en otra terminal, lo que ya usas para eso
(`caffeinate` en el Mac, tu comando de siempre en Linux).

En el celular: PCs → **Actualizar** → aparece `macbook — en línea` → **Conectar** →
**Abrir terminal**. La primera vez la app guarda la llave del PC (en Ajustes →
"PCs recordados" aparece su huella).

## Sesiones de tmux: una por dispositivo

Cada cliente entra a su propia sesión de tmux en el PC, y las sesiones sobreviven a las desconexiones:

| Cliente | Sesión | Cómo cambiarla |
|---|---|---|
| Celular (APK) | `rda` | `rda-agent -tmux otra` |
| `rda ssh <pc>` desde otro PC | el nombre de ese dispositivo (p. ej. `pc-casa-cli`) | `rda ssh -tmux rda <pc>` entra a la del celular |

Desde otro PC, con un dispositivo cliente propio (no uses el nombre del agente de ese PC: la
señalización guarda una conexión por dispositivo y le quitarías los mensajes):
```bash
scripts/register-pc.sh pc-casa-cli        # una vez
(set -a; . ~/.config/rda/pc-casa-cli.agent.env; set +a; ./bin/rda ssh macbook)
```
`-v` deja los logs en `~/.config/rda/ssh.log` (no encima de la terminal).

## Particularidades

**MacBook Pro M4**
- Si macOS bloquea el binario ("no se puede verificar el desarrollador"):
  `xattr -d com.apple.quarantine ~/rda/rda-agent`. Pasa solo si llegó por descarga
  o AirDrop; con `scp` no aparece.
- La primera vez que corre, el firewall de macOS puede preguntar si acepta conexiones
  entrantes: **Permitir** (el hole punching las necesita).

**Legion 5 Pro**
- **Con Linux** es igual que pc-casa.
- **Con Windows** el agente todavía no sirve: no hay pty ni tmux nativos. La vía sería
  WSL2 (Ubuntu) con `networkingMode=mirrored` para que el hole punching vea la red real.
  Esto no está probado: avísame antes de intentarlo.

**Router sin UPnP (reenvío de puerto manual)**
- Síntoma: desde 4G no conecta y el agente dice `UPnP no disponible`.
- En el router: reserva por DHCP la IP local del PC (la que muestra `./rda stun`, p. ej.
  `192.168.1.20`) y reenvía un puerto **UDP** (p. ej. `47900`) a esa IP, mismo puerto adentro y afuera.
- En el PC: agrega `RDA_PORT=47900` a `~/.config/rda/agent.env` (o corre `./rda-agent -port 47900`).
  El agente anuncia `IP pública:47900` como candidato `fijo`.
- Un puerto por PC: si tienes dos PCs en la misma casa, usa un puerto distinto para cada uno.

**Redes fuera de la casa (oficina, café)**
- Los PCs en tu casa usan el UPnP del router y conectan directo.
- En otra red sin UPnP, con el celular en 4G (NAT simétrico), lo más probable es que
  **no conecte** hasta que exista el relay (fase 5). Para saber de antemano qué tipo
  de NAT tiene esa red: `./rda stun` en ese PC (el `rda` está en la misma carpeta de `bin/dist`).
- Si es el PC del trabajo, revisa antes la política de seguridad de la empresa.

## Si algo falla

| Síntoma | Revisar |
|---|---|
| El PC no aparece en línea | ¿el agente sigue corriendo? ¿`RDA_DEVICE` coincide con el nombre registrado? |
| "llave … no autorizada" en el agente | El paso 4. Compara la huella con la de Ajustes en el celular. |
| "la llave del PC cambió" en el celular | Se regeneró `~/.config/rda/host_ed25519` en ese PC: Ajustes → PCs recordados → Olvidar. |
| Conecta pero no abre la terminal | ¿tmux instalado? Mira la salida del agente. |
| No conecta (fuera de casa) | Lo de "Redes fuera de la casa" de arriba. |
