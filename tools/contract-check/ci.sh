#!/usr/bin/env bash
# Контрактная проверка без Docker: собирает restaurant/menu/orders/payment и mock шлюза, запускает их на Postgres
# и гоняет tools/contract-check/check.py для сценариев catalog, orders и payment.
# Kafka в CI нет: заказ для сценария payment засевается прямо в БД payment (в проде он приходит событием orders.state).
#   DB_ADMIN_URL  — админский URL Postgres (по умолчанию postgres://postgres:postgres@localhost:5432/postgres)
#   PYTHON        — интерпретатор с pyyaml/jsonschema (по умолчанию python3)
set -euo pipefail
cd "$(dirname "$0")/../.."

ADMIN="${DB_ADMIN_URL:-postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable}"
PY="${PYTHON:-python3}"
WORK="$(mktemp -d)"
PIDS=()

cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

dburl() { echo "${ADMIN%/*}/$1?sslmode=disable"; }

for db in restaurant menu orders payment; do
  psql "$ADMIN" -qc "DROP DATABASE IF EXISTS $db WITH (FORCE)" -c "CREATE DATABASE $db"
done

for svc in restaurant menu orders payment; do
  go build -o "$WORK/$svc" "./services/$svc/cmd/$svc-service"
done
go build -o "$WORK/mock" ./tools/mock-externals

start() { # имя порт [env...]
  local name=$1 port=$2; shift 2
  env APP_ENV=dev AUTH_MODE=dev LOG_LEVEL=warn HTTP_PORT="$port" DATABASE_URL="$(dburl "$name")" "$@" \
    "$WORK/$name" >"$WORK/$name.log" 2>&1 &
  PIDS+=("$!")
}

start restaurant 8082 SEED_ON_START=true
start menu 8083 SEED_ON_START=true
start orders 8084 MENU_URL=http://localhost:8083 RESTAURANT_URL=http://localhost:8082

start payment 8085 GATEWAY_URL=http://localhost:9091 RECONCILE_BACKOFF_MIN=1s
env MOCKS=payment MOCK_DELAY=1s MOCK_PAYMENT_WEBHOOK_URL=http://localhost:8085/webhooks/payments/gateway "$WORK/mock" >"$WORK/mock.log" 2>&1 &
PIDS+=("$!")

for port in 8082 8083 8084 8085; do
  for i in $(seq 1 60); do
    curl -fsS "http://localhost:$port/actuator/health" >/dev/null 2>&1 && break
    [ "$i" = 60 ] && { echo "сервис на :$port не поднялся"; tail -n 30 "$WORK"/*.log; exit 1; }
    sleep 1
  done
done

psql "$(dburl payment)" -qc "INSERT INTO payment_orders (order_id, revision, order_number, restaurant_id, fulfillment, method, amount, currency)
  VALUES ('a1000000-0000-4000-8000-0000000000a1', 1, 'R1-CI-0001', 'a1000000-0000-4000-8000-000000000004', 'pickup', 'online_card', 34900, 'RUB')"

rc=0
for scenario in catalog orders payment; do
  echo "== contract-check $scenario"
  CC_PAYMENT_ORDER=a1000000-0000-4000-8000-0000000000a1 CC_PAYMENT_AMOUNT=34900 \
    "$PY" tools/contract-check/check.py "$scenario" || rc=$?
done
[ "$rc" = 0 ] || { echo "--- логи сервисов"; tail -n 40 "$WORK"/*.log; }
exit "$rc"
