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
| | wifi casa | 4G | pendiente | |
| | wifi casa | wifi casa | pendiente (necesita hairpinning del router o usar la dirección local) | |
