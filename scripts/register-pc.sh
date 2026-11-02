#!/usr/bin/env bash
# register-pc.sh <nombre> — registra un PC nuevo en la señalización de AWS.
#
# Genera su token, guarda SOLO el hash en DynamoDB y deja el archivo listo para
# el agente en ~/.config/rda/<nombre>.agent.env (permisos 600). El token nunca se
# imprime: el archivo se copia tal cual al PC nuevo como ~/.config/rda/agent.env.
set -euo pipefail

name=${1:?uso: scripts/register-pc.sh <nombre>   (p. ej. macbook, legion)}
[[ $name =~ ^[a-z0-9-]+$ ]] || { echo "nombre inválido: solo minúsculas, números y guiones" >&2; exit 2; }
ROOT=$(cd "$(dirname "$0")/.." && pwd)
CFG=$HOME/.config/rda
TABLE=remote-dev-access-signaling
out=$CFG/$name.agent.env
[ -e "$out" ] && { echo "$out ya existe: ese PC ya está registrado" >&2; exit 1; }

url=$(grep '^RDA_URL=' "$CFG/agent.env" | cut -d= -f2-)
[ -n "$url" ] || { echo "no encontré RDA_URL en $CFG/agent.env" >&2; exit 1; }

gen=$("$ROOT/bin/rda" token)
token=$(awk '/^token:/ {print $2}' <<<"$gen")
hash=$(awk '/^hash:/ {print $2}' <<<"$gen")

aws dynamodb put-item --table-name "$TABLE" \
  --condition-expression "attribute_not_exists(pk)" \
  --item "{\"pk\":{\"S\":\"dev#$name\"},\"kind\":{\"S\":\"pc\"},\"token_hash\":{\"S\":\"$hash\"}}"

umask 077
printf 'RDA_URL=%s\nRDA_DEVICE=%s\nRDA_TOKEN=%s\n' "$url" "$name" "$token" > "$out"
echo "✓ $name registrado. Copia $out al PC nuevo como ~/.config/rda/agent.env"
