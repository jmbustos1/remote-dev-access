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
| | wifi casa (UPnP + IPv6) | 4G | pendiente | |
