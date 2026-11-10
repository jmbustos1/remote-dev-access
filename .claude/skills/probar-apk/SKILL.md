---
name: probar-apk
description: Prueba la APK de remote-dev-access en un emulador Android de punta a punta (señalización local + agente de prueba + terminal tmux), viendo la pantalla con capturas y tocando el teclado como un usuario. Usar después de cambiar algo de la app Android (sobre todo la terminal, el teclado o el layout) y ANTES de pasarle una APK al usuario, o cuando el usuario reporte algo que se ve mal en el celular.
---

# Probar la APK en el emulador

Todo pasa por `scripts/emu-test.sh` (desde la raíz del repo). No toca AWS, ni el
celular del usuario, ni su agente real: usa señalización local (:8791), un agente
`pc-test` con su propio `authorized_keys` en `/tmp/rda-emu/` y la sesión de tmux
`rda-test`.

## Flujo

1. `make build apk` (el script usa `bin/rda`, `bin/rda-agent` y la APK más nueva de `bin/`).
2. `scripts/emu-test.sh up` → levanta el AVD `Pixel_7` en **modo solo lectura**, instala,
   configura la app buscando botones por su texto, genera y autoriza la llave, conecta
   y abre la terminal. Imprime `tmux ve la terminal de CxF` y deja una captura en
   `/tmp/rda-emu/shot.png`. Si falta espacio: `MAKE_SPACE=1 scripts/emu-test.sh up`.
3. Mirar `/tmp/rda-emu/shot.png` con Read y verificar lo que cambió.
4. `scripts/emu-test.sh down` al terminar (siempre).

## Ver y tocar

- `scripts/emu-test.sh ui` → textos visibles con su centro `x y` en **coordenadas reales**
  (1080×2400 en Pixel_7). Las capturas que muestra Read vienen escaladas (900×2000):
  multiplicar por 1,2 para tocar lo que se ve en una captura.
- `scripts/emu-test.sh tap-text "Abrir terminal (tmux)"` espera a que aparezca y lo toca.
- `scripts/emu-test.sh size` → columnas×filas que ve tmux. Es la prueba más barata de que
  el layout de la terminal está bien (con teclado cerrado ~50×47; con teclado ~50×27).
- Logs de la app: pestaña Logs → activar "Debug" → `ui` para leerlos (líneas
  "tamaño de terminal", "abriendo terminal", etc.). Logs del agente: `scripts/emu-test.sh logs`.

## Probar el teclado como usuario

`scripts/emu-test.sh type` usa `adb input text`, que inyecta teclas **sin pasar por el
teclado en pantalla**: no detecta problemas del IME. **Antes de `type`, tocar el área negra
de la terminal** (`tap 540 1000`): sin foco, el texto se pierde y un `KEYCODE_ENTER` "presiona"
el primer botón enfocable (‹ PCs) y la app vuelve a la lista. Para probar el IME:

1. Tocar el área negra de la terminal (abre Gboard).
2. `scripts/emu-test.sh shot` y ubicar las teclas en la captura.
3. Tocar cada tecla con `scripts/emu-test.sh tap X Y` (coordenadas reales).
4. Verificar en otra captura que llegó exactamente lo escrito.

Prueba mínima: escribir `seq 1 5` + Enter de Gboard (deben salir 1..5), borrar una
letra, y Ctrl (barra) + c → `^C`.

## Probar el scroll táctil

```
tmux send-keys -t rda-test 'seq 1 200' Enter          # historial
adb -s emulator-5560 shell input swipe 540 700 540 1700 600   # dedo hacia abajo
tmux display -p -t rda-test '#{pane_in_mode} #{scroll_position}'   # → "1 <n>": modo copia
adb -s emulator-5560 shell input swipe 540 1700 540 400 400   # dedo hacia arriba (repetir)
tmux display -p -t rda-test '#{pane_in_mode}'                   # → "0": volvió al final
```
Luego un toque simple debe seguir abriendo el teclado (`size` → ~50×25).

## Si `up` falla en "configurando la app"

Pasó una vez (2026-11-10) que la app quedó "Señalización: sin configurar" aunque la llave se
generó; la repetición pasó completa. Probablemente el texto se escribió antes de que el campo
tuviera foco. Si se repite: `shot` después de cada `tap_text` de Ajustes para ver cuál campo
quedó vacío.

## Reglas

- **No usar el emulador que el usuario tenga abierto** (suele ser `Medium_Phone_API_36.1`
  en `emulator-5554`, casi sin espacio): el script usa otro AVD en el puerto 5560.
- El AVD corre con `-read-only`: desinstalar apps ahí (MAKE_SPACE) no afecta al AVD real,
  todo se descarta al apagar. Nunca usar `-wipe-data` sobre AVDs del usuario.
- `tmux-continuum` del usuario restaura sus sesiones guardadas (p. ej. "trabajo") cuando
  el servidor de tmux arranca; es esperado, no borrarlas.
- Al reportar al usuario: decir qué se probó en el emulador y qué queda por probar en
  su celular real (Redmi 9 con MIUI: teclado y WebView pueden diferir).

## Bugs que esta prueba ya atrapó (no repetirlos)

- WebView sin `LayoutParams(MATCH_PARENT, MATCH_PARENT)` dentro de Compose → el 100% de la
  página vale ~0 y xterm.js calcula **1 fila**.
- `safeDrawingPadding()` ya incluye el teclado: sumarle `imePadding()` lo resta dos veces.
- Teclado vía Chromium → Gboard compone palabras y xterm.js **duplica letras**. Se resolvió
  con `TermWebView` (InputConnection propia con `TYPE_NULL`, como Termux).
- Tamaño informado mientras la terminal aún se abría → se perdía; se reenvía al abrir.
