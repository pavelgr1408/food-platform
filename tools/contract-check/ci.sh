#!/usr/bin/env bash
# Контрактная проверка без Docker: собирает restaurant/menu/orders/payment/delivery и mock шлюза, запускает их на Postgres
# и гоняет tools/contract-check/check.py для сценариев catalog, orders, payment, staff и delivery.
# Kafka в CI нет: заказы для сценариев payment и staff засеваются прямо в БД payment, доставки для delivery — в БД delivery
# (в проде они приходят событием orders.state);
# возврат при отмене (нужен orders.status через Kafka) проверяется только на живом стенде: make contract-check SCENARIO=staff.
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

for db in restaurant menu orders payment delivery; do
  psql "$ADMIN" -qc "DROP DATABASE IF EXISTS $db WITH (FORCE)" -c "CREATE DATABASE $db"
done

for svc in restaurant menu orders payment delivery; do
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

start payment 8085 GATEWAY_URL=http://localhost:9091 POS_URL=http://localhost:9093 RECONCILE_BACKOFF_MIN=1s
start delivery 8087 RESTAURANT_URL=http://localhost:8082
env MOCKS=payment,pos MOCK_DELAY=1s MOCK_PAYMENT_WEBHOOK_URL=http://localhost:8085/webhooks/payments/gateway \
  MOCK_POS_WEBHOOK_URL=http://localhost:8085/webhooks/payments/pos "$WORK/mock" >"$WORK/mock.log" 2>&1 &
PIDS+=("$!")

for port in 8082 8083 8084 8085 8087; do
  for i in $(seq 1 60); do
    curl -fsS "http://localhost:$port/actuator/health" >/dev/null 2>&1 && break
    [ "$i" = 60 ] && { echo "сервис на :$port не поднялся"; tail -n 30 "$WORK"/*.log; exit 1; }
    sleep 1
  done
done

psql "$(dburl payment)" -qc "INSERT INTO payment_orders (order_id, revision, order_number, restaurant_id, fulfillment, method, amount, currency)
  VALUES ('a1000000-0000-4000-8000-0000000000a1', 1, 'R1-CI-0001', 'a1000000-0000-4000-8000-000000000004', 'pickup', 'online_card', 34900, 'RUB'),
         ('a1000000-0000-4000-8000-0000000000a2', 1, 'R4-CI-0002', 'a1000000-0000-4000-8000-000000000004', 'pickup', 'pos', 34900, 'RUB')"

# Доставки для сценария delivery: курьер назначен / доставлено (контакты курьера после вручения не отдаются)
psql "$(dburl delivery)" -qc "INSERT INTO deliveries (id, order_id, order_number, restaurant_id, revision, status, address, dest_lat, dest_lon, contact_name, contact_phone,
    amount, courier_ref, courier_id, courier_name, courier_phone, eta_at, search_started_at, created_at, updated_at)
  VALUES ('d1000000-0000-4000-8000-0000000000d1', 'a1000000-0000-4000-8000-0000000000b1', 'R4-CI-0003', 'a1000000-0000-4000-8000-000000000004', 1, 'assigned',
          'Невский пр., 30', 59.94, 30.33, 'Иван', '+79990000001', 34900, 'cr_ci1', 'c01', 'Анна', '+79990000002', now() + interval '20 minutes', now(), now(), now()),
         ('d1000000-0000-4000-8000-0000000000d2', 'a1000000-0000-4000-8000-0000000000b2', 'R4-CI-0004', 'a1000000-0000-4000-8000-000000000004', 1, 'delivered',
          'Невский пр., 30', 59.94, 30.33, 'Иван', '+79990000001', 34900, 'cr_ci2', 'c01', 'Анна', '+79990000002', NULL, now(), now(), now())"

rc=0
for scenario in catalog orders payment staff delivery; do
  echo "== contract-check $scenario"
  CC_PAYMENT_ORDER=a1000000-0000-4000-8000-0000000000a1 CC_PAYMENT_AMOUNT=34900 CC_POS_ORDER=a1000000-0000-4000-8000-0000000000a2 \
    CC_DELIVERY_ASSIGNED=a1000000-0000-4000-8000-0000000000b1 CC_DELIVERY_DELIVERED=a1000000-0000-4000-8000-0000000000b2 \
    "$PY" tools/contract-check/check.py "$scenario" || rc=$?
done
[ "$rc" = 0 ] || { echo "--- логи сервисов"; tail -n 40 "$WORK"/*.log; }
exit "$rc"
