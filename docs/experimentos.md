# Experimentos

Resultados de red por fase. No anotar IPs públicas reales.

## Fase 1 — STUN

| Fecha | Dispositivo / red | Puerto local → público | Clasificación |
|---|---|---|---|
| 2026-11-01 | PC casa, wifi | 48957 → 48957 (mismo puerto hacia Google y Cloudflare) | independiente del destino ✅ |
| | Celular, wifi casa | | pendiente |
| | Celular, 4G | | pendiente — define si el relay será la norma |

Observación: el router de la casa conserva el puerto local (48957 → 48957). No todos los NAT
hacen eso; lo importante para el hole punching es que el puerto público sea el mismo hacia
destinos distintos.
