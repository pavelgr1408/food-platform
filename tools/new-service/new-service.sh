#!/usr/bin/env bash
# Создаёт сервис из tools/service-template:  tools/new-service/new-service.sh NAME PORT
# Порты и БД сервисов — реестр в contracts/README.md. Скрипт переносимый (bash 3.2+/macOS).
set -euo pipefail
NAME="${1:-}"; PORT="${2:-}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
T="$ROOT/tools/service-template"
MODPATH="$(awk '/^module /{print $2}' "$ROOT/go.mod")"

[[ "$NAME" =~ ^[a-z][a-z0-9]{1,20}$ ]] || { echo "NAME must match ^[a-z][a-z0-9]{1,20}\$ (e.g. orders)" >&2; exit 2; }
[[ "$PORT" =~ ^[0-9]{4,5}$ ]] || { echo "PORT is required (see ports in contracts/README.md)" >&2; exit 2; }
DST="$ROOT/services/$NAME"
[[ ! -e "$DST" ]] || { echo "services/$NAME already exists" >&2; exit 1; }
if grep -rlE "^port: $PORT\$" "$ROOT/deploy/values" >/dev/null 2>&1; then echo "port $PORT is already used in deploy/values" >&2; exit 1; fi

render() { # render SRC DST
  mkdir -p "$(dirname "$2")"
  sed -e "s#__MODPATH__#$MODPATH#g" -e "s#__NAME__#$NAME#g" -e "s#__PORT__#$PORT#g" "$1" > "$2"
}

render "$T/cmd/__NAME__-service/main.go.tmpl"        "$DST/cmd/$NAME-service/main.go"
render "$T/internal/config/config.go.tmpl"           "$DST/internal/config/config.go"
render "$T/internal/httpapi/api.go.tmpl"             "$DST/internal/httpapi/api.go"
render "$T/internal/httpapi/api_test.go.tmpl"        "$DST/internal/httpapi/api_test.go"
render "$T/migrations/embed.go.tmpl"                 "$DST/migrations/embed.go"
render "$T/migrations/001_init.sql"                  "$DST/migrations/001_init.sql"
render "$T/README.md.tmpl"                           "$DST/README.md"
render "$T/postman/collection.json.tmpl"             "$ROOT/postman/$NAME/$NAME.postman_collection.json"
render "$T/values.yaml.tmpl"                         "$ROOT/deploy/values/$NAME.yaml"
render "$T/compose.yml.tmpl"                         "$ROOT/deploy/compose/services/$NAME.yml"

echo "created services/$NAME (port $PORT)"
echo "next: 1) contracts/openapi/$NAME.yaml  2) make test  3) make up SLICE=\"$NAME\""
[[ -f "$ROOT/contracts/openapi/$NAME.yaml" ]] || echo "warning: contracts/openapi/$NAME.yaml not found — contract-first: write it before the code" >&2
