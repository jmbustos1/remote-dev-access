# remote-dev-access — Plan

Abrir una terminal desde el celular hacia mis PCs (detrás de wifi/NAT) para retomar sesiones de
Claude Code que viven en `tmux`. Conexión **P2P** (hole punching UDP) con señalización serverless
en AWS y relay de respaldo bajo demanda.

**Caso de uso:** dejo el PC prendido, salgo, abro la APK, toco `pc-casa` y estoy en mi sesión.
**Uso estimado:** ~4 h/semana. **PCs:** 2 personales + trabajo (ver riesgos).

---

## Arquitectura

```
┌────────── APK (Android) ──────────┐                          ┌──────── PC (x3) ─────────┐
│ Pestaña Terminal │ Pestaña Logs   │                          │ agente (servicio systemd) │
│        └── core Go (gomobile) ────┼══ canal P2P (WebRTC) ════┼── core Go ──► sshd :22    │
└───────────────┬───────────────────┘                          └────────────┬──────────────┘
                │ WebSocket (wss)                                           │ WebSocket (ping 5 min)
                └──────────► API Gateway WebSocket + Lambda + DynamoDB ◄────┘
                                     (señalización)
                    STUN: Google (gratis) · Relay: EC2 t4g.nano bajo demanda (pion/turn)
```

**Flujo de una conexión**

1. El agente del PC mantiene un WebSocket abierto con API Gateway ("soy `pc-casa`, disponible").
2. La APK abre su WebSocket y pide `connect pc-casa`.
3. Ambos consultan STUN (UDP) para descubrir su `IP pública:puerto`.
4. Intercambian candidatos (ICE) vía la Lambda.
5. Hole punching → canal directo cifrado (DTLS). Si falla → relay TURN en la EC2.
6. Sobre el canal: data channel WebRTC ↔ tubería TCP ↔ `sshd` del PC → `tmux attach` → `claude`.

**Notas de diseño**

- API Gateway tiene tres tipos de API: REST, HTTP y **WebSocket**. Usamos la WebSocket (conexión
  persistente sobre TCP/TLS, la Lambda se invoca por mensaje). Corta tras 10 min sin tráfico y a
  las 2 h de vida → ping cada 5 min y reconexión automática. No soporta UDP, pero la señalización
  no lo necesita: el UDP va directo entre dispositivos y al STUN.
- SSH sigue siendo la capa de autenticación y terminal: el canal P2P solo mueve bytes. `sshd`
  nunca se expone a internet.
- Keepalive P2P cada ~25 s **solo mientras la APK está abierta**. Al cerrarla, el canal muere;
  la sesión sobrevive en `tmux` y reconectar toma ~1 s.
- Un único **core en Go** compartido por agente, CLI y APK (vía `gomobile bind` → `.aar`).

---

## Estructura del repo

```
core/        Go: señalización, STUN, P2P (pion/webrtc), tubería TCP, cliente SSH, logs
agent/       Go: binario del PC + unidad systemd
cli/         Go: cliente de prueba (Termux / laptop)
signaling/   Lambda (Go) + Terraform: API Gateway WebSocket, DynamoDB
relay/       Terraform + pion/turn en EC2, encendido/apagado por la Lambda
android/     Kotlin + Jetpack Compose: UI, terminal, logs; consume core.aar
docs/        diagramas, notas de experimentos (resultados NAT 4G, etc.)
```

---

## Fases

Cada fase termina en algo probable por sí solo.

### Fase 0 — Base
- `go.mod` (Go ≥ 1.22), Makefile (`build`, `test`, `aar`, `apk`), `.gitignore`.
- **Hecho cuando:** `make build` compila `agent` y `cli`.

### Fase 1 — STUN
- `core/stun`: preguntar a `stun.l.google.com:19302` y reportar `IP:puerto` público.
- `cli stun` corre en PC y en Termux.
- **Hecho cuando:** ves tu IP/puerto público desde ambos lados.

### Fase 2 — Hole punching manual
- `cli punch --peer <ip:puerto>`: ambos disparan UDP simultáneo, intercambian "hola".
- Detectar tipo de NAT (mismo puerto hacia 2 servidores STUN distintos → no simétrico).
- **Hecho cuando:** llega el "hola" PC ⇄ celular por **wifi y por 4G**. Registrar resultado
  del NAT 4G en `docs/` (define cuánto se usará el relay).

### Fase 3 — Señalización
- Terraform: API Gateway WebSocket (`$connect`, `$disconnect`, `register`, `connect`, `signal`),
  Lambda en Go (`provided.al2023`, arm64), DynamoDB on-demand (`device_id → connection_id`).
- Auth: token por dispositivo (hash en DynamoDB), validado en `$connect`.
- **Hecho cuando:** `cli connect pc-casa` arma el canal sin copiar direcciones a mano.

### Fase 4 — Terminal por el canal
- `pion/webrtc` (ICE + DTLS + data channels) reemplaza el UDP crudo de la fase 2.
- Tubería: data channel ↔ `localhost:22` en el PC; `localhost:2222` ↔ data channel en el cliente.
- Agente como servicio systemd con reconexión automática al WebSocket.
- **Hecho cuando:** desde Termux `ssh -p 2222 tecob@localhost` → `tmux attach` → Claude.
  **Primer hito usable.**

### Fase 5 — Relay
- `pion/turn` en EC2 `t4g.nano`, credenciales TURN efímeras emitidas por la Lambda.
- La Lambda enciende la EC2 si el ICE falla; apagado automático tras N min sin sesiones.
- **Hecho cuando:** conecta desde 4G aun con NAT simétrico (primer arranque ~40 s).

### Fase 6 — APK
> La APK se arma **desde ya** (no al final) como app de pruebas: cada fase le agrega un botón en la
> pestaña Pruebas y todo se depura en la pestaña Logs. Termux queda solo como alternativa.

- Kotlin + Jetpack Compose; `core` compilado con `gomobile bind` → `core.aar`.
- Pestañas: **Terminal · Logs · Ajustes** (+ **Pruebas** durante el desarrollo).
- Cliente SSH dentro del core (`golang.org/x/crypto/ssh`) → no depende de Termux.
- **Pestaña Terminal:** emulador (`terminal-view` / `terminal-emulator` de Termux, Apache 2.0),
  lista de PCs con estado online/offline, botón conectar.
- **Pestaña Logs:**
  - Eventos del core vía callback: timestamp, nivel, etapa
    (`signaling` · `stun` · `ice` · `punch` · `relay` · `ssh`).
  - Filtros por etapa y nivel; buffer circular en memoria + archivo rotativo.
  - Panel de estado del enlace: directo/relay, RTT, bytes in/out, candidatos ICE.
  - Botón "Compartir logs" (exporta archivo por intent de Android).
- **Pestaña Ajustes / seguridad:**
  - URL de señalización (`wss://…execute-api…`) y nombre del dispositivo.
  - Token del dispositivo → cifrado con Android Keystore. Nunca embebido en la APK.
  - Par de llaves SSH **generado en el celular**; la privada no sale del dispositivo. Botón
    "copiar llave pública" para agregarla a `authorized_keys` de cada PC.
  - v1: carga manual (pegar). v2 (fase 7): emparejamiento por QR con `rda-agent pair`.
  - Opcional: huella/PIN al abrir la app.
- Keepalive solo con la app en primer plano.
- **Distribución:** APK de release subida a Google Drive y descargada en el celular (sideload).
  - Keystore de release propio desde el día 1 (las actualizaciones exigen la misma firma).
    Fuera de git, con respaldo: perderlo obliga a desinstalar para actualizar.
  - `versionCode` autoincremental en el script de build.
  - La APK no contiene secretos: cualquiera con el link podría descargarla.
- **Hecho cuando:** instalo la APK, toco `pc-casa` y estoy en Claude.

### Fase 7 — Multi-PC y endurecimiento
- Registro de `pc-2`, `pc-trabajo`; rotación/revocación de tokens.
- Emparejamiento por QR: `rda-agent pair` muestra URL + código de un solo uso; la APK lo escanea,
  obtiene su token y envía su llave pública SSH al PC.
- Logs del agente en `journalctl`; opcional: enviar últimos logs del PC a la APK para debug.

**Orden:** 0 → 1 (hecho) → **APK mínima** (pestañas Pruebas + Logs + Ajustes, botón "Probar
STUN") → 2 → 3 → 4, agregando cada capacidad a la APK. Luego 5 (relay) según el resultado del NAT
4G, y 7.

---

## Decisiones

| Tema | Decisión | Por qué |
|---|---|---|
| P2P | WebRTC (`pion/webrtc`) | ICE/STUN/TURN/DTLS/data channels resueltos; Go ya conocido |
| Señalización | API Gateway WebSocket + Lambda + DynamoDB | push al PC sin polling, costo ~0 |
| Relay | `pion/turn` en EC2 bajo demanda | mismo stack Go; solo cuesta cuando se usa |
| APK | Kotlin nativo (no React Native) | el core Go sale como `.aar`, que se consume desde Kotlin; RN agregaría una capa (módulo nativo Kotlin igual) + Expo/EAS/nvm. Toolchain ya instalada (Android Studio, SDK 36, NDK 27) |
| Terminal | SSH sobre el canal | auth con llaves + PTY completo gratis; sshd nunca público |

---

## Costo estimado (USD/mes)

Supuestos: ~17 h/mes de uso, 3 PCs conectados 24/7 a la señalización, tráfico de terminal (MB/h).

| Pieza | Cálculo | USD/mes |
|---|---|---|
| API Gateway WebSocket | 3 × 43.800 min-conexión ($0,25/M) + pings ($1/M msgs) | ~0,06 |
| Lambda + DynamoDB | cientos de invocaciones / lecturas | ~0,00 |
| STUN | Google | 0 |
| Relay EC2 t4g.nano bajo demanda | ~17 h × (instancia + IPv4 pública) + EBS 8 GB | ~0,80 |
| Transferencia | muy por debajo de 100 GB/mes gratis | 0 |
| **Total** | | **~1** |

Relay siempre encendido: ~7,5 USD/mes. Play Store (opcional): 25 USD una vez.

---

## Riesgos y pendientes

- **PC del trabajo:** un agente que acepta conexiones vía hole punching puede violar la política
  de seguridad de la empresa. Consultar antes de instalar.
- **NAT simétrico en 4G:** si es el caso, la mayoría de las sesiones móviles irán por relay.
- **Go 1.22 local:** versiones recientes de `gomobile` / `pion` pueden pedir Go más nuevo →
  actualizar Go o fijar versiones.
- **Batería / Android:** el sistema mata conexiones en segundo plano; diseñado para reconectar
  rápido en vez de mantener vivo.
- **Seguridad de la señalización:** tokens por dispositivo; la Lambda solo empareja dispositivos
  del mismo dueño. SSH con llave, sin contraseña.
