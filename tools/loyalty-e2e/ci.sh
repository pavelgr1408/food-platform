#!/usr/bin/env bash
# Сквозная проверка S8 без Docker: loyalty → Kafka (kfake, in-memory) → orders → скидка в корзине и заказе.
# Реальные бинарники restaurant, menu, orders, loyalty (AUTH_MODE=dev) на Postgres + tools/kfake-broker; затем newman:
# папка «12. S8» коллекции postman/e2e.
#   DB_ADMIN_URL — админский URL Postgres (по умолчанию postgres://postgres:postgres@localhost:5432/postgres)
#   NEWMAN       — команда запуска newman (по умолчанию: newman, иначе npx -y newman@6)
set -euo pipefail
cd "$(dirname "$0")/../.."

ADMIN="${DB_ADMIN_URL:-postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable}"
if [ -n "${NEWMAN:-}" ]; then NEW="$NEWMAN"; elif command -v newman >/dev/null; then NEW=newman; else NEW="npx -y newman@6"; fi
WORK="$(mktemp -d)"
PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

dburl() { echo "${ADMIN%/*}/$1?sslmode=disable"; }
BROKER=127.0.0.1:19092

for db in restaurant menu orders loyalty; do
  psql "$ADMIN" -qc "DROP DATABASE IF EXISTS $db WITH (FORCE)" -c "CREATE DATABASE $db"
done
for svc in restaurant menu orders loyalty; do
  go build -o "$WORK/$svc" "./services/$svc/cmd/$svc-service"
done
go build -o "$WORK/kfake" ./tools/kfake-broker

"$WORK/kfake" -port 19092 >"$WORK/kfake.log" 2>&1 &
PIDS+=("$!")
sleep 1

start() { # имя порт [env...]
  local name=$1 port=$2; shift 2
  env APP_ENV=dev AUTH_MODE=dev LOG_LEVEL=warn HTTP_PORT="$port" DATABASE_URL="$(dburl "$name")" KAFKA_BROKERS="$BROKER" "$@" \
    "$WORK/$name" >"$WORK/$name.log" 2>&1 &
  PIDS+=("$!")
}
start restaurant 8082 SEED_ON_START=true
start menu 8083 SEED_ON_START=true
start orders 8084 MENU_URL=http://localhost:8083 RESTAURANT_URL=http://localhost:8082 PROMOTION_CACHE_TTL=500ms
start loyalty 8086

for port in 8082 8083 8084 8086; do
  for i in $(seq 1 60); do
    curl -fsS "http://localhost:$port/actuator/health" >/dev/null 2>&1 && break
    [ "$i" = 60 ] && { echo "сервис на :$port не поднялся"; tail -n 30 "$WORK"/*.log; exit 1; }
    sleep 1
  done
done

rc=0
$NEW run postman/e2e/e2e.postman_collection.json -e postman/e2e/local.postman_environment.json \
  --folder "12. S8: акция → скидка в корзине и заказе (loyalty → Kafka → orders)" || rc=$?
[ "$rc" = 0 ] || { echo "--- логи сервисов"; tail -n 40 "$WORK"/*.log; }
exit "$rc"
