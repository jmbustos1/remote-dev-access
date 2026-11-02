# remote-dev-access

Terminal desde el celular hacia mis PCs detrás de NAT, **P2P y sin servicios de terceros**
(nada de Tailscale/ngrok): hole punching escrito a mano, señalización serverless en AWS, QUIC
sobre el mismo agujero UDP, un servidor SSH embebido en el agente y `tmux` en el PC. Pensado
para retomar sesiones de Claude Code desde el teléfono.

```
APK (xterm.js) ─ SSH ─ QUIC ─ UDP (hole punching / UPnP) ─ rda-agent ─ pty ─ tmux
        └──────── WebSocket (señalización: API Gateway + Lambda + DynamoDB) ────────┘
```

**Estado:** fase 4 funcionando (terminal en el celular por 4G hacia un PC en casa, conexión
directa). Pendiente: relay para redes sin UPnP (fase 5), arranque automático del agente.
Ver [PLAN.md](PLAN.md) y la bitácora [docs/experimentos.md](docs/experimentos.md).

## Piezas

| Carpeta | Qué es |
|---|---|
| `core/stun` | STUN (RFC 5389) a mano + clasificación de NAT |
| `core/punch` | Hole punching: candidatos (STUN, UPnP, IPv6, LAN), PING/PONG, keepalive |
| `core/tunnel` | QUIC (`quic-go`) sobre el socket del punching |
| `core/sshserver` / `core/sshclient` | SSH embebido (solo llave pública, pty, tmux) |
| `core/signaling`, `core/rendezvous` | Protocolo de señalización y offer/answer |
| `core/mobile` | API para la APK (gomobile) |
| `agent/` | `rda-agent`, corre en cada PC |
| `cli/` | `rda`: stun, punch, devices, connect, ssh, keygen, token, signal-local |
| `signaling/` | Lambda + Terraform de la señalización |
| `android/` | APK (Kotlin + Compose) |

## Uso rápido

```
make build test      # bin/rda y bin/rda-agent
make apk             # APK firmada (requiere android/keystore.properties)
make agent-dist      # agente para macOS arm64 y Linux amd64
./bin/rda stun -v    # ¿cómo me ve internet? + tipo de NAT
```

- Desplegar la señalización: [signaling/README.md](signaling/README.md)
- Sumar un PC: [docs/onboarding-pc.md](docs/onboarding-pc.md)
- Compilar y firmar la APK: [docs/android.md](docs/android.md)
- Probar la APK en un emulador: `scripts/emu-test.sh up`

## Configuración y secretos

Nada sensible está en el repo. Cada valor vive fuera de git:

| Valor | Dónde | Plantilla |
|---|---|---|
| Email de alertas, región AWS | `signaling/terraform/terraform.tfvars` | `terraform.tfvars.example` |
| URL de la señalización, nombre y token del PC | `~/.config/rda/agent.env` (600) o env `RDA_URL`, `RDA_DEVICE`, `RDA_TOKEN` | lo genera `scripts/register-pc.sh` |
| Llaves autorizadas / llave del PC | `~/.config/rda/authorized_keys`, `~/.config/rda/host_ed25519` | `rda-agent -authorize`; la llave del PC se genera sola |
| Firma de la APK | `android/keystore.properties` + keystore `.jks` | ver `docs/android.md` |
| URL, token y llave SSH del celular | Ajustes de la app (cifrados con el Android Keystore) | — |

DynamoDB guarda solo el SHA-256 de cada token. La APK no trae ningún secreto.
