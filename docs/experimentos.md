# Experimentos

Resultados de red por fase. No anotar IPs públicas reales.

## Fase 1 — STUN

| Fecha | Dispositivo / red | Puerto local → público | Clasificación |
|---|---|---|---|
| 2026-11-01 | PC casa, wifi | 48957 → 48957 (mismo puerto hacia Google y Cloudflare) | independiente del destino ✅ |
| 2026-11-01 | Celular (APK 5971e9c), wifi casa | — (log sin debug) | independiente del destino ✅ |
| 2026-11-01 | Celular (APK 5971e9c), datos móviles | — (log sin debug) | independiente del destino ✅ |

Conclusión: los tres escenarios tienen mapeo independiente del destino, así que el hole punching
debería funcionar directo y el relay (fase 5) queda como respaldo poco frecuente. Ojo: el mapeo
es solo la mitad; el filtrado de cada NAT recién se prueba con el punching real (fase 2).

Observación: el router de la casa conserva el puerto local (48957 → 48957). No todos los NAT
hacen eso; lo importante para el hole punching es que el puerto público sea el mismo hacia
destinos distintos.

## Fase 2 — Hole punching manual

PC con `rda punch -v`, celular con la APK (Pruebas → Hole punching).

| Fecha | PC | Celular | Resultado | RTT |
|---|---|---|---|---|
| 2026-11-01 | wifi casa | 4G (CGNAT, IP local 10.x) | ✗ 3 intentos: PC → celular llega, celular → PC no | — |
| | wifi casa | wifi casa | pendiente (necesita hairpinning del router o usar la dirección local) | |

Observaciones (2026-11-01):
- En 4G la IP pública cambió entre sesiones (dos IPs distintas del mismo bloque): CGNAT con un pool de IPs.
- Hipótesis: hacia el PC, el CGNAT sale con otro puerto/IP que el que vio el STUN, y el router de
  la casa solo deja entrar respuestas desde el ip:puerto exacto al que escribió el PC → descarta.
  El CGNAT, en cambio, deja entrar al PC por el mapeo que abrió el STUN (filtrado laxo).
- `sendto: operation not permitted` en el celular: probablemente Android bloqueando red con la
  app en segundo plano (mientras se copiaban direcciones en otra app). Por confirmar.
- Router de la casa: MiniUPnPd 1.9 responde (UPnP IGD); NAT-PMP no. El PC tiene IPv6 global.

## Fase 2b — IPv6 y UPnP

- PC: IPv6 global sin NAT (STUN por IPv6 devuelve la propia dirección del equipo).
- Router: UPnP abre y cierra el puerto UDP sin problemas; reenvía el mismo número de puerto que
  ya usaba el NAT (candidato `stun+upnp`). No hay doble NAT (IP externa del router = IP de STUN).

| Fecha | PC | Celular | Camino ganador | RTT |
|---|---|---|---|---|
| 2026-11-01 | wifi casa (UPnP + IPv6) | 4G (sin IPv6, NAT simétrico) | ✅ `stun+upnp` (IPv4 vía puerto reenviado), chat en ambos sentidos | ~40 ms (picos 190 ms) |

Observaciones (2026-11-01, prueba exitosa):
- El celular conectó ~1 s después de tocar Conectar; el PC no necesitó la línea del celular:
  recibió "paquete entrante antes de conectar" gracias al puerto UPnP y respondió a la dirección real.
- En 4G la prueba STUN dio **NAT simétrico** esta vez: confirma por qué falló la fase 2 sin UPnP.
- La compañía móvil **no entrega IPv6** (el error "no such host" de Twilio es solo el último intento;
  Google y Cloudflare tampoco respondieron por IPv6).
- **Android corta la red de la app en segundo plano:** "app en segundo plano" 20:59:09.9 →
  `sendto: operation not permitted` 20:59:18.5 → "primer plano" 20:59:18.6. En el PC se vio como
  "conexión perdida" (21:00:22) y reconexión sola por keepalive al volver la app (21:00:32).
  → Para la terminal hará falta un *foreground service* mientras haya sesión.

Conclusiones:
- PCs de la casa: UPnP resuelve el CGNAT simétrico del celular → conexión directa, sin relay.
- PC del trabajo (sin UPnP) + 4G simétrico: casi seguro necesitará el relay (fase 5).

## Fase 3 — Señalización en AWS

Desplegada 2026-11-01 (16 recursos, cuenta personal, us-east-1). Dispositivos registrados:
`pc-casa` (pc) y `celular` (phone); tokens en `~/.config/rda/` (600), en la tabla solo el hash.

| Fecha | Prueba | Resultado |
|---|---|---|
| 2026-11-01 | `rda-agent` (pc-casa) + `rda connect` (celular) desde el mismo PC | ✅ conectado en 3,1 s vía señalización AWS, eco OK |
| 2026-11-01 | token inválido | ✅ rechazado en `$connect` |
| 2026-11-01 | celular (APK 1afa6ff) en 4G → pc-casa, botón Conectar | ✅ conectado directo en 2,6 s (`stun+upnp`), RTT 39 ms, eco OK |
| 2026-11-01 | pantalla bloqueada con sesión activa (foreground service + wake lock) | ✅ sin corte (49 s en los logs; el usuario reporta ~10 min sin corte) |

Observación: el primer minuto tras el `apply`, las conexiones del agente recibían 403 sin llegar
a la Lambda (probablemente propagación de la API recién creada); un minuto después, todo normal.

Observaciones APK (2026-11-01):
- Tocar Conectar → "conectado directo" en 2,6 s. El paquete del PC llegó **antes** que la answer
  (22:54:28.501 vs .708): el PC dispara apenas recibe la offer y el celular lo adopta como peer.
- Con el foreground service, bloquear la pantalla ya **no** produce `sendto: operation not permitted`
  ni "conexión perdida" (en la fase 2b la red se cortaba a los ~8 s).
- Pendiente: repetir el bloqueo largo con Debug activado (para ver los keepalive) y probar en
  otros fabricantes con optimización de batería agresiva.

## Fase 4 — Terminal

| Fecha | Prueba | Resultado |
|---|---|---|
| 2026-11-02 | APK 3632e45 (Redmi 9) → pc-casa: terminal → tmux → Claude Code | ✅ **primer hito usable**: el usuario le escribió a Claude Code desde el celular |

## Fase 5 — Router sin UPnP

Contexto (2026-11-09): el router nuevo de la casa no responde UPnP, NAT-PMP ni PCP (probado contra
el gateway). Un equipo Huawei de la red se anuncia por SSDP como InternetGatewayDevice, pero es un
punto de acceso (`upnpap.xml`, responde 403): no abre puertos. Desde 4G ya no conecta.
Teoría y opciones en [nat-traversal.md](nat-traversal.md).

### Experimento NAT (`rda natlab`)

Reportes anónimos (`-anon` / botón Copiar de la app): nuestras IPs aparecen como `PUB1`, `LOCAL`.

| Fecha | Red | Mapeo (A) | IP pública (B) | Asignación (B) | Conclusión |
|---|---|---|---|---|---|
| 2026-11-10 | wifi casa (router nuevo) | independiente del destino, conserva el puerto | siempre la misma | conserva el puerto local (32/32) | lado "fácil": sirve como el lado EIM de la fuerza bruta |
| 2026-11-10 | 4G del celular (Redmi 9) | **independiente del destino** (mismo `PUB1:35524` hacia 5 destinos, incluidos 2 puertos de la misma IP) | siempre la misma | **por bloque**: 32 sockets en 35330–35784 (bloque estimado de 512), al azar dentro del bloque, no conserva el puerto local | el 4G **no es simétrico**: la fuerza bruta no hace falta |

Notas:
- **4G:** el CGNAT es EIM con asignación por bloque (port block allocation, típico de un CGNAT que
  sigue RFC 6888). Esto **refuta** la hipótesis de la fase 2 ("hacia el PC sale con otro puerto").
  La falla del 2026-11-01 se explica mejor por el `sendto: operation not permitted` de ese día
  (Android cortando la red con la app en segundo plano, arreglado después con el foreground service).
- **Confirmado en la práctica** (tabla de abajo): con los dos lados EIM, el punching normal conecta
  sin UPnP. La falla de la tarde del 2026-11-09 quedó sin explicar (no hubo logs).

### Intento real 4G → casa sin UPnP

| Fecha | PC | Celular | Camino | RTT |
|---|---|---|---|---|
| 2026-11-10 01:00 | wifi casa, router nuevo **sin UPnP** | 4G | ✅ `stun` directo (`PUB1:35331` ⇄ `CASA:41976`) | 20–60 ms |

Observaciones (logs de ambos lados con `-v` / Debug):
- El puerto del celular (35331) cae dentro del bloque que midió `natlab` (35330–35784).
- **Filtrado del 4G = independiente del origen:** el celular recibió el PING del PC (42.212) *antes*
  de la answer (42.380), o sea, antes de haberle escrito nunca a esa dirección, y el CGNAT lo dejó
  entrar. Era lo único que `natlab` no puede medir.
- El PC pierde 2 s esperando UPnP (40.86 → 42.86) antes de mandar la answer: buscar UPnP en
  paralelo para no retrasarla.
- **Casa:** con IPv4 la casa es el lado EIM ideal. Sin UPnP, el problema es solo el filtrado
  estricto (ver nat-traversal.md §2). IPv6 no disponible con el router nuevo (el viejo sí tenía).

### Mac por la zona Wi-Fi del celular → pc-casa (2026-11-11)

Oficina: el celular por 4G y el Mac conectado a la zona Wi-Fi del celular. El celular entra a
pc-casa sin problemas; el Mac no. `rda natlab` en el Mac: EIM, bloque de ~512 (64523–64981),
siempre la misma IP. El firewall de macOS está desactivado.

| Prueba | Resultado |
|---|---|
| pc-casa → Mac (agente en el Mac), 3 rondas | ✅ 3/3 (RTT ~46 ms) |
| Mac → pc-casa (agente en pc-casa), 3 rondas | ❌ 0/3: ambos lados disparan ~150 PING, no cruza ninguno |
| `rda punch` manual, el Mac dispara primero | ✅ conectado |
| `rda punch` manual, pc-casa dispara primero | ❌ nada |
| Mac → agente con espera de 1 s (`ConnectAfter`) | ✅ 3/3 |

Causa: el NAT de Linux del celular (zona Wi-Fi) anota el PING entrante de pc-casa y, cuando el Mac
escribe después, sale por otro puerto. Explicación completa en nat-traversal.md §9.

### El 4G se volvió simétrico (2026-11-11, tarde)

Mismo montaje de la mañana (Mac por la zona Wi-Fi del celular → pc-casa, agente con
`c288357`), unas horas después. Ya no conecta ni el Mac ni el celular. pc-casa sigue en línea
y responde cada offer; su answer trae solo `stun` y `local` (el router nuevo no tiene UPnP).

`rda natlab -anon`, tres mediciones del 4G el mismo día:

| Cuándo | Red | A. un socket → 5 destinos | B. 32 sockets → google | Mac → pc-casa |
|---|---|---|---|---|
| 11/11 mañana | zona Wi-Fi de mi celular | **EIM** | bloque ~512 (64523–64981) | ✅ 3/3 |
| 11/11 14:05 | zona Wi-Fi de mi celular | **simétrico** (saltos +117 −29 −50 +197) | bloque ~256 (33794–34039) | ❌ 0/2 intentos |
| 11/11 14:09 | zona Wi-Fi del celular de un colega (otra IP pública, la misma operadora: AS7418) | **simétrico** (saltos −126 +63 +81 −198) | bloque ~256 (12294–12533) | ❌ 0/1 intento |

Cada intento prueba los dos órdenes (normal y `listener-first`). El celular, directo por 4G,
tampoco conecta (sin logs de esa prueba).

En los intentos fallidos los dos lados disparan PING durante 30 s y no cruza ninguno, ni
el primero con el orden normal ni el reintento con `listener-first`. A diferencia de §9 de
nat-traversal.md, aquí el orden no importa: el CGNAT asigna un puerto nuevo para cada destino,
así que el Mac le escribe a pc-casa desde un puerto que nadie anunció, y el router de la casa
(filtrado estricto) lo bota. Es la traza de nat-traversal.md §2, que el 10/11 parecía refutada.

Observaciones:
- **El tipo de NAT de la operadora no es fijo.** Mañana EIM, tarde simétrico, con la misma
  operadora. Hipótesis (sin confirmar): salir por otro CGNAT (otra antena u otro pool), con otra
  política de mapeo.
- Las dos redes simétricas mantienen la asignación por bloque, aunque más chico (~256): el
  CGNAT sigue reservando un rango por cliente, pero dentro de él elige un puerto por destino.
- Para la fuerza bruta este es el caso bueno: el otro lado (pc-casa) conserva el puerto y el
  bloque es chico. Según `natlab` §C, con 64 sockets acá, pc-casa necesitaría ~19 disparos para
  99 %. Pero quien barre los puertos es el agente de pc-casa, así que sin actualizarlo no se
  puede probar a distancia.
- `rda stun` (un socket, 3 servidores) ya lo muestra en 2 s: si los puertos difieren entre
  servidores, no vale la pena intentar sin UPnP, reenvío manual o relay.
- **Lección:** una medición de `natlab` describe la red en ese momento, no a la operadora.
  Medir justo antes de cada prueba, y que la escalera de la fase 5 no asuma el tipo de NAT por
  la red.
