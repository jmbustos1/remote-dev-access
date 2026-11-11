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

### Fase 2b — Candidatos: IPv6 y UPnP
- Resultado de la fase 2: en 4G (CGNAT) el camino celular → PC se pierde: el CGNAT sale con otro
  puerto/IP y el router de la casa lo descarta. PC → celular sí funciona.
- Socket dual-stack; candidatos `stun` (IPv4), `upnp` (puerto reenviado por el router de la casa),
  `ipv6` (sin NAT) y `local`. Se dispara a todos los candidatos del otro lado y gana el primero
  que completa ida y vuelta (mini ICE). Aceptar direcciones "peer reflexive".
- Tras el disparo rápido, el keepalive sigue intentando (el "falló" deja de ser definitivo).
- **Hecho cuando:** PC (casa) ⇄ celular (4G) conectan por `upnp` o por `ipv6`. ✅ 2026-11-01 por
  `upnp` (~40 ms). La red móvil no tiene IPv6.

### Fase 3 — Señalización
- Terraform: API Gateway WebSocket (`$connect`, `$disconnect`, `register`, `connect`, `signal`),
  Lambda en Go (`provided.al2023`, arm64), DynamoDB on-demand (`device_id → connection_id`).
- Auth: token por dispositivo (hash en DynamoDB), validado en `$connect`.
- **Hecho cuando:** `cli connect pc-casa` arma el canal sin copiar direcciones a mano.

### Fase 4 — Terminal por el canal ✅ (código listo 2026-11-01; falta probar en el celular)
- Cambio de plan: en vez de `pion/webrtc` (que traería su propio ICE y tiraría el
  hole punching hecho a mano), **QUIC (`quic-go`) sobre el mismo socket del punching**.
  `punch.Session.PacketConn()` separa STUN / RDA1 / QUIC por la forma del paquete.
  QUIC da retransmisiones y streams; su dirección del peer es fija, así que un cambio de
  camino (NAT que reasigna puerto, wifi→4G) no lo afecta.
- **Servidor SSH embebido en el agente** (`core/sshserver`): no hace falta `sshd` ni abrir
  el puerto 22; solo se llega por el canal P2P. Solo llave pública
  (`~/.config/rda/authorized_keys`, se autoriza con `rda-agent -authorize '<llave>'`).
  Llave del PC en `~/.config/rda/host_ed25519`; el celular la recuerda (TOFU).
- La shell es `tmux new-session -A -s rda`: al cortarse la conexión, tmux sigue vivo.
- APK: xterm.js en un WebView + barra de teclas (Esc, Tab, Ctrl, flechas, ^B…).
  La llave SSH se genera en el celular (privada cifrada con el Keystore).
- CLI: `rda keygen` y `rda ssh <pc> [comando]` (probado en local: pty + tmux OK).
- **Hecho cuando:** desde la APK en 4G → terminal → tmux → Claude. **Primer hito usable.**
- Pendiente: agente como servicio systemd.

### Fase 5 — Conectar en cualquier red (escalera de caminos)
> Motivo (2026-11-09): el router nuevo de la casa no tiene UPnP/NAT-PMP/PCP y desde 4G dejó de
> conectar. **Corrección 2026-11-10:** `rda natlab` mostró que el 4G es EIM (no simétrico), igual
> que la casa: el punching normal debería bastar y la causa real está por diagnosticar. La
> escalera sigue siendo el diseño para redes ajenas (oficinas, PCs de terceros).
> Teoría completa y cuentas en [docs/nat-traversal.md](docs/nat-traversal.md).

Regla: **P2P primero, relay solo de respaldo** (como WebRTC/TURN o Tailscale/DERP).

```
t = 0 s     offer/answer (cada lado manda también su tipo de NAT medido con STUN)
            ├─ Escalón 1: directo (LAN, IPv6, UPnP, puerto fijo, STUN) — PING cada 0,5 s
t = 3 s     ¿PONG? sí → conectado (lo normal: 0,1–1 s) · no → fracasa el escalón 1
            ├─ en paralelo: la Lambda pide arrancar el relay (Fargate tarda 30–60 s)
            ├─ Escalón 2: fuerza bruta de puertos (solo si un lado conserva el puerto)
            │    ronda 1: 256 sockets en el lado simétrico × 1024 disparos del otro (~2 s)
            │    ronda 2: puertos nuevos al azar (~2 s)
t = 8 s     ¿PONG? sí → conectado directo, el relay se apaga solo sin usarse · no → fracasa el 2
            ├─ Escalón 3: relay
t ≈ 35–65 s relay arriba → ambos lados se conectan a él
t = 90 s    nada → FRACASO TOTAL: la app muestra el motivo de cada escalón
```

| Escalón | Fracasa si | Por qué ese tiempo |
|---|---|---|
| 1. Directo | ningún PONG en 3 s (~6 rondas) | cuando funciona conecta en ~1 s; reintentar no cambia cómo descarta el NAT |
| 2. Fuerza bruta | 2 rondas sin acierto (~5 s), o se salta | con ~98 % por ronda, dos fallos seguidos = falla una condición de fondo |
| 3. Relay | sin conexión a t = 90 s | margen para el arranque en frío de Fargate |

- **Saltar la fuerza bruta** si los dos lados son simétricos (probabilidad ~0), o si ya falló
  antes en esta red. Se recuerda por PC qué escalón ganó la última vez y se empieza por ahí.
- **Arranque especulativo del relay** apenas falla el escalón 1: Fargate cobra por segundo (mínimo
  1 min), así que arrancarlo "por si acaso" cuesta fracciones de centavo.
- **Sesión caída** (sin PONG 20 s): repetir la escalera desde el escalón que ganó la última vez.
- **Después:** estando en relay, seguir probando el directo en segundo plano y migrar sin cortar
  (QUIC no se entera del cambio de camino).

Piezas:
- [x] **5a. Puerto fijo** (`rda-agent -port` / `RDA_PORT`) para un reenvío manual en el router.
- [x] **5b. Experimento** `rda natlab` / Pruebas → "Experimento NAT" (core/natlab): mide mapeo por
  destino y asignación de puertos entre sockets. Resultados en docs/experimentos.md.
- [ ] **5c. IPv6:** revisar router nuevo y APN del celular (`IPv4/IPv6`). Sin código.
- [x] **5c'. Diagnóstico:** 4G → casa sin UPnP conecta directo (2026-11-10). La falla de la tarde
  anterior quedó sin explicar. Mejora pendiente: UPnP en paralelo (hoy retrasa la answer 2 s).
- [x] **Orden de los disparos** (2026-11-11): el agente espera 1 s para que el cliente dispare
  primero; si en 5 s no conecta, el cliente reintenta con sockets nuevos y el orden invertido.
  Cubre los NAT de Linux (zona Wi-Fi de Android) en cualquiera de los dos lados. Ver
  nat-traversal.md §9. El agente también cierra las sesiones sin noticias tras 4 min.
- [ ] **5d. Fuerza bruta** como candidato (multi-socket en `core/punch`, tipo de NAT en la offer).
  En pausa: el 4G medido es EIM. Útil solo si aparece una red simétrica (oficina, otra operadora).
- [ ] **5e. Relay propio en Fargate bajo demanda:** contenedor Go chico (no TURN completo);
  `RDA1 RELAY <sesión> <firma HMAC>` empareja a los dos lados y luego reenvía bytes tal cual
  (SSH va cifrado de punta a punta: el relay solo ve bytes cifrados). Tarea con IP pública
  propia (sin NLB ni NAT Gateway), Fargate Spot ARM, VPC por defecto, apagado tras 10 min
  sin tráfico. Verificar precios reales antes de construir.
- **Hecho cuando:** desde 4G conecta a la casa sin UPnP, y a un PC en una red cualquiera.

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
- **Foreground service** (notificación "Conectado a pc-casa") mientras haya sesión: Android corta
  la red de la app a los ~8 s en segundo plano (visto en la fase 2b). Sin sesión, nada corre.
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
| Relay | Relay propio en Fargate bajo demanda (antes: `pion/turn` en EC2) | sin servidor que mantener, se enciende solo si fallan los caminos directos; ver fase 5 |
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
| Relay Fargate bajo demanda | solo minutos de uso + IPv4 pública mientras corre (por verificar) | ~0,00–0,50 |
| Transferencia | muy por debajo de 100 GB/mes gratis | 0 |
| **Total** | | **~1** |

Relay siempre encendido: ~7 (EC2) a ~11 (Fargate) USD/mes, incluida la IPv4 pública (~3,65). Play Store (opcional): 25 USD una vez.

---

## Riesgos y pendientes

- **PC del trabajo:** un agente que acepta conexiones vía hole punching puede violar la política
  de seguridad de la empresa. Consultar antes de instalar.
- **NAT simétrico en 4G:** descartado para esta operadora (2026-11-10, `rda natlab`: EIM con
  bloque de ~512 puertos). Otras operadoras u oficinas pueden serlo: medir con `rda natlab`.
- **Go 1.22 local:** versiones recientes de `gomobile` / `pion` pueden pedir Go más nuevo →
  actualizar Go o fijar versiones.
- **Batería / Android:** el sistema mata conexiones en segundo plano; diseñado para reconectar
  rápido en vez de mantener vivo.
- **Seguridad de la señalización:** tokens por dispositivo; la Lambda solo empareja dispositivos
  del mismo dueño. SSH con llave, sin contraseña.
