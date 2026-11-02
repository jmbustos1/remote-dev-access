#!/usr/bin/env bash
# emu-test.sh — prueba la APK en un emulador Android sin tocar AWS ni el celular.
#
# Levanta un AVD en modo SOLO LECTURA (-read-only: nada de lo que pase queda
# guardado en el AVD), una señalización local, un agente de prueba (pc-test, con
# su propio authorized_keys y la sesión de tmux "rda-test"), instala la APK y la
# configura sola buscando los botones por su texto en la pantalla.
#
# Uso:
#   scripts/emu-test.sh up [apk]     todo lo anterior, hasta dejar la terminal abierta
#   scripts/emu-test.sh shot [png]   captura de pantalla (por defecto $DIR/shot.png)
#   scripts/emu-test.sh ui           textos visibles con sus coordenadas (centro x,y)
#   scripts/emu-test.sh tap-text T   toca el elemento cuyo texto es T (el más bajo si hay varios)
#   scripts/emu-test.sh tap X Y      toca en coordenadas reales del dispositivo
#   scripts/emu-test.sh type TEXTO   escribe por adb (¡no pasa por el teclado en pantalla!)
#   scripts/emu-test.sh size         tamaño que ve tmux (columnas×filas de la terminal)
#   scripts/emu-test.sh logs         logs del agente de prueba
#   scripts/emu-test.sh down         apaga emulador, agente y señalización
#
# Variables: AVD (Pixel_7), EMU_PORT (5560), SIG_PORT (8791), RDA_EMU_DIR (/tmp/rda-emu),
# MAKE_SPACE=1 desinstala apps de terceros del emulador si falta espacio (solo
# tiene sentido porque el AVD corre en modo solo lectura).
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
SDK=${ANDROID_HOME:-$HOME/Android/Sdk}
AVD=${AVD:-Pixel_7}
EMU_PORT=${EMU_PORT:-5560}
SIG_PORT=${SIG_PORT:-8791}
DIR=${RDA_EMU_DIR:-/tmp/rda-emu}
SERIAL=emulator-$EMU_PORT
ADB() { "$SDK/platform-tools/adb" -s "$SERIAL" "$@"; }
PKG=dev.jmbustos.rda
mkdir -p "$DIR"

log() { printf '\033[36m▸ %s\033[0m\n' "$*" >&2; }
die() { printf '\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

# dump → líneas "x y texto" (centro del elemento), una por elemento con texto.
ui() {
  ADB shell uiautomator dump /sdcard/rda-ui.xml >/dev/null 2>&1 || true
  ADB shell cat /sdcard/rda-ui.xml | python3 -c '
import re, sys, html
for m in re.finditer(r"<node [^>]*?text=\"([^\"]*)\"[^>]*?bounds=\"\[(\d+),(\d+)\]\[(\d+),(\d+)\]\"", sys.stdin.read()):
    t, x1, y1, x2, y2 = m.group(1), *map(int, m.groups()[1:])
    if t: print((x1 + x2) // 2, (y1 + y2) // 2, html.unescape(t))'
}

# tap_text TEXTO [timeout_s]: espera a que aparezca y lo toca (el más bajo en pantalla).
tap_text() {
  local want=$1 timeout=${2:-15} line
  for _ in $(seq "$timeout"); do
    line=$(ui | awk -v t="$want" '{ x=$1; y=$2; $1=""; $2=""; sub(/^  /, ""); if ($0 == t) print x, y }' | sort -k2 -n | tail -1)
    if [ -n "$line" ]; then ADB shell input tap $line; sleep 0.7; return 0; fi
    sleep 1
  done
  die "no apareció \"$want\" en pantalla (mira: $0 shot)"
}

wait_text() { # wait_text REGEX [timeout_s]
  for _ in $(seq "${2:-30}"); do ui | grep -qE "$1" && return 0; sleep 1; done
  die "no apareció /$1/ en pantalla"
}

stop_local() {
  for p in $(pgrep -x rda-agent || true) $(pgrep -x rda || true); do
    tr '\0' ' ' < /proc/"$p"/cmdline | grep -qE ":$SIG_PORT|signal-local -listen :$SIG_PORT" && kill "$p" || true
  done
  tmux kill-session -t rda-test 2>/dev/null || true
}

cmd_up() {
  local apk=${1:-$(ls -t "$ROOT"/bin/rda-*.apk 2>/dev/null | head -1)}
  [ -f "$apk" ] || die "no hay APK: corre make apk"
  [ -x "$ROOT/bin/rda" ] && [ -x "$ROOT/bin/rda-agent" ] || die "faltan bin/rda y bin/rda-agent: corre make build"

  if ! "$SDK/platform-tools/adb" devices | grep -q "^$SERIAL"; then
    log "levantando $AVD en modo solo lectura (puerto $EMU_PORT)"
    nohup "$SDK/emulator/emulator" -avd "$AVD" -read-only -no-window -no-audio -no-boot-anim \
      -port "$EMU_PORT" > "$DIR/emulator.log" 2>&1 &
  fi
  log "esperando que arranque Android"
  for _ in $(seq 100); do
    [ "$(ADB shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = 1 ] && break
    sleep 3
  done
  [ "$(ADB shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = 1 ] || die "el emulador no arrancó (ver $DIR/emulator.log)"

  log "señalización local :$SIG_PORT + agente pc-test"
  stop_local
  : > "$DIR/authorized_keys"
  nohup "$ROOT/bin/rda" signal-local -listen ":$SIG_PORT" \
    -device pc-test:pc:t1 -device cel-test:phone:t2 > "$DIR/signal.log" 2>&1 &
  sleep 0.5
  nohup "$ROOT/bin/rda-agent" -url "ws://127.0.0.1:$SIG_PORT" -device pc-test -token t1 -upnp=false \
    -authorized-keys "$DIR/authorized_keys" -tmux rda-test > "$DIR/agent.log" 2>&1 &

  log "instalando $(basename "$apk") (desde cero: el AVD es de solo lectura)"
  ADB uninstall "$PKG" >/dev/null 2>&1 || true
  if ! out=$(ADB install "$apk" 2>&1); then
    if grep -q "not enough space" <<<"$out" && [ "${MAKE_SPACE:-0}" = 1 ]; then
      for p in $(ADB shell pm list packages -3 | sed 's/package://' | tr -d '\r'); do
        log "sin espacio: desinstalo $p (solo en esta sesión de solo lectura)"
        ADB uninstall "$p" >/dev/null || true
      done
      ADB install "$apk" >/dev/null || die "sigue sin espacio"
    else
      grep -q "not enough space" <<<"$out" && die "el emulador no tiene espacio: repite con MAKE_SPACE=1"
      die "$out"
    fi
  fi
  ADB shell pm grant "$PKG" android.permission.POST_NOTIFICATIONS 2>/dev/null || true

  log "configurando la app"
  ADB shell am start -n "$PKG/$PKG.app.MainActivity" >/dev/null
  tap_text "Ajustes"
  tap_text "URL del API Gateway";        ADB shell input text "ws://10.0.2.2:$SIG_PORT"
  tap_text "Nombre de este dispositivo"; ADB shell input text "cel-test"
  tap_text "Token del dispositivo";      ADB shell input text "t2"
  ADB shell input keyevent KEYCODE_BACK; sleep 0.5 # esconder el teclado
  tap_text "Guardar"
  tap_text "Generar llave"
  local key
  key=$(ui | grep -oE "ssh-ed25519 [A-Za-z0-9+/=]+ rda@cel-test" | head -1)
  [ -n "$key" ] || die "no encontré la llave pública en pantalla"
  "$ROOT/bin/rda-agent" -authorized-keys "$DIR/authorized_keys" -authorize "$key" >&2

  log "conectando y abriendo la terminal"
  tap_text "PCs"
  wait_text "pc-test" 20
  tap_text "Conectar" 20
  tap_text "Abrir terminal (tmux)" 40
  sleep 3
  cmd_size
  cmd_shot
  log "listo: terminal abierta. Toca el área negra para abrir el teclado en pantalla."
}

cmd_shot() {
  local f=${1:-$DIR/shot.png}
  ADB exec-out screencap -p > "$f"
  echo "$f"
}

cmd_size() {
  tmux list-clients -t rda-test -F 'tmux ve la terminal de #{client_width}x#{client_height}' 2>/dev/null ||
    echo "no hay cliente en la sesión rda-test (¿terminal cerrada?)"
}

cmd_down() {
  ADB emu kill >/dev/null 2>&1 || true
  stop_local
  log "emulador, agente y señalización detenidos (el AVD no guardó cambios)"
}

case ${1:-} in
  up) shift; cmd_up "$@" ;;
  shot) shift; cmd_shot "$@" ;;
  ui) ui ;;
  tap-text) shift; tap_text "$*" ;;
  tap) ADB shell input tap "$2" "$3" ;;
  type) shift; ADB shell input text "$(sed 's/ /%s/g' <<<"$*")" ;;
  size) cmd_size ;;
  logs) cat "$DIR/agent.log" ;;
  down) cmd_down ;;
  *) sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
