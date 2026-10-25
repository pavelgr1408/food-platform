#!/usr/bin/env bash
# Сквозная проверка периметра (S5, S7, S8) без Docker: auth + restaurant + menu + orders + payment + delivery + loyalty (в режиме jwks)
# + gateway на реальных бинарниках и Postgres; затем newman: коллекции auth (напрямую) и gateway (через периметр).
# Kafka нет: orders/payment работают без relay; сценарии периметра её не требуют.
#   DB_ADMIN_URL — админский URL Postgres (по умолчанию postgres://postgres:postgres@localhost:5432/postgres)
#   NEWMAN       — команда запуска newman (по умолчанию: newman, иначе npx -y newman@6)
#   HOLD=1       — не запускать newman, оставить стенд поднятым (отладка), остановить по Ctrl+C
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

for db in auth restaurant menu orders payment delivery loyalty loyalty_dev; do
  psql "$ADMIN" -qc "DROP DATABASE IF EXISTS $db WITH (FORCE)" -c "CREATE DATABASE $db"
done

openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$WORK/private.pem" 2>/dev/null
openssl pkey -in "$WORK/private.pem" -pubout -out "$WORK/public.pem"

for svc in auth restaurant menu orders payment delivery loyalty gateway; do
  go build -o "$WORK/$svc" "./services/$svc/cmd/$svc-service"
done

JWKS=http://localhost:8081/auth/.well-known/jwks.json
start() { # имя порт [env...]
  local name=$1 port=$2; shift 2
  env APP_ENV=dev LOG_LEVEL=warn HTTP_PORT="$port" DATABASE_URL="$(dburl "$name")" "$@" \
    "$WORK/$name" >"$WORK/$name.log" 2>&1 &
  PIDS+=("$!")
}

start auth 8081 SEED_ON_START=true BCRYPT_COST=10 JWT_PRIVATE_KEY_PATH="$WORK/private.pem" JWT_PUBLIC_KEY_PATH="$WORK/public.pem" JWT_KEY_ID=ci-key
for entry in "restaurant 8082" "menu 8083" "orders 8084" "payment 8085" "delivery 8087" "loyalty 8086"; do
  set -- $entry
  extra=()
  case $1 in
    restaurant|menu) extra=(SEED_ON_START=true) ;;
    orders) extra=(MENU_URL=http://localhost:8083 RESTAURANT_URL=http://localhost:8082) ;;
    payment) extra=(GATEWAY_URL=http://localhost:9091) ;;
    delivery) extra=(RESTAURANT_URL=http://localhost:8082) ;;
    loyalty) extra=(AUTH_URL=http://localhost:8081) ;;
  esac
  start "$1" "$2" AUTH_MODE=jwks AUTH_JWKS_URL="$JWKS" "${extra[@]}"
done
# loyalty в режиме dev (личность из X-User-*, права по локальным правилам) — для коллекции postman/loyalty
cp "$WORK/loyalty" "$WORK/loyalty_dev"
start loyalty_dev 8186 AUTH_MODE=dev
# gateway без БД: DATABASE_URL ему не нужен, но лишняя переменная безвредна
env APP_ENV=dev LOG_LEVEL=warn HTTP_PORT=8080 METRICS_ADDR=:9100 \
  UPSTREAM_AUTH_URL=http://localhost:8081 UPSTREAM_RESTAURANT_URL=http://localhost:8082 UPSTREAM_MENU_URL=http://localhost:8083 \
  UPSTREAM_ORDERS_URL=http://localhost:8084 UPSTREAM_PAYMENT_URL=http://localhost:8085 UPSTREAM_DELIVERY_URL=http://localhost:8087 UPSTREAM_LOYALTY_URL=http://localhost:8086 \
  TRUSTED_PROXY_CIDRS=127.0.0.0/8,::1/128 CORS_ALLOWED_ORIGINS=http://localhost:3000 AUTH_RATE_PER_MIN=30 AUTH_RATE_BURST=30 \
  "$WORK/gateway" >"$WORK/gateway.log" 2>&1 &
PIDS+=("$!")

for port in 8081 8082 8083 8084 8085 8086 8186 8087 8080; do
  for i in $(seq 1 60); do
    curl -fsS "http://localhost:$port/actuator/health" >/dev/null 2>&1 && break
    [ "$i" = 60 ] && { echo "сервис на :$port не поднялся"; tail -n 30 "$WORK"/*.log; exit 1; }
    sleep 1
  done
done
echo "стенд поднят: gateway :8080 → auth :8081, restaurant :8082, menu :8083, orders :8084, payment :8085, loyalty :8086, delivery :8087"

if [ "${HOLD:-}" = 1 ]; then
  echo "HOLD=1: стенд работает, Ctrl+C — остановить"
  wait
  exit 0
fi

rc=0
PY="${PYTHON:-python3}"
if "$PY" -c 'import yaml, jsonschema, referencing' 2>/dev/null; then
  for scenario in auth gateway; do
    echo "== contract-check $scenario"
    "$PY" tools/contract-check/check.py "$scenario" || rc=$?
  done
else
  echo "contract-check пропущен: нет pyyaml/jsonschema (pip install -r tools/requirements.txt)"
fi
echo "== newman: auth (напрямую)"
$NEW run postman/auth/auth.postman_collection.json -e postman/auth/local.postman_environment.json || rc=$?
echo "== newman: loyalty (напрямую, AUTH_MODE=dev)"
$NEW run postman/loyalty/loyalty.postman_collection.json --env-var baseUrl=http://localhost:8186 || rc=$?
echo "== newman: gateway (периметр)"
$NEW run postman/gateway/gateway.postman_collection.json -e postman/gateway/local.postman_environment.json || rc=$?
# Сценарии, которые Postman не умеет: блокировка пользователя после выдачи токена и отказ auth.
G=http://localhost:8080
fail() { echo "FAIL: $*"; rc=1; }
login() { curl -fsS -X POST "$G/auth/login" -H 'Content-Type: application/json' -d "{\"phone\":\"$1\",\"password\":\"$2\"}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["accessToken"])'; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
ORDER='{"cartId":"00000000-0000-4000-8000-000000000000","paymentMethod":"pos"}'
JSON='Content-Type: application/json'

echo "== S8: акции через периметр (JWT + реальный /auth/authorize)"
OWNER_T=$(login +79995555555 owner123)
HQ_T=$(login +79991111111 admin123)
R4=a1000000-0000-4000-8000-000000000004
PROMO='{"title":"CI","scope":"local","restaurantId":"'$R4'","priority":1,"startsAt":"2026-01-01T00:00:00Z","endsAt":"2099-01-01T00:00:00Z","discount":{"type":"percent","value":1000}}'
NAT=${PROMO/\"local\"/\"national\"}
NAT=${NAT/\"restaurantId\":\"$R4\",/}
c=$(code "$G/promotions?restaurantId=$R4")
[ "$c" = 200 ] && echo "ok: гость видит список действующих акций" || fail "guest GET /promotions: want 200, got $c"
c=$(code -X POST "$G/promotions" -H "Idempotency-Key: s8-$RANDOM" -H "$JSON" -d "$PROMO")
[ "$c" = 401 ] && echo "ok: создание без токена — 401" || fail "POST /promotions without token: want 401, got $c"
c=$(code -X POST "$G/promotions" -H "Authorization: Bearer $OWNER_T" -H "Idempotency-Key: s8-$RANDOM" -H "$JSON" -d "$NAT")
[ "$c" = 403 ] && echo "ok: владелец не создаёт национальную акцию (403 от auth /authorize)" || fail "owner national: want 403, got $c"
out=$(curl -s -X POST "$G/promotions" -H "Authorization: Bearer $OWNER_T" -H "Idempotency-Key: s8-$RANDOM" -H "$JSON" -d "$PROMO")
PID=$(echo "$out" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("id",""))')
[ -n "$PID" ] && echo "ok: владелец создал локальную акцию $PID" || fail "owner local create: $out"
c=$(code -X POST "$G/promotions/$PID/activation" -H "Authorization: Bearer $OWNER_T")
[ "$c" = 200 ] && echo "ok: активация" || fail "activation: want 200, got $c"
c=$(code -X POST "$G/promotions/$PID/completion" -H "Authorization: Bearer $HQ_T")
[ "$c" = 200 ] && echo "ok: HQ завершает акцию ресторана" || fail "HQ completion: want 200, got $c"

echo "== chaos: заблокированный пользователь на sensitive-маршруте"
T=$(login +79995555555 owner123)
psql "$(dburl auth)" -qc "UPDATE users SET status='BLOCKED' WHERE phone='+79995555555'"
c=$(code -X POST "$G/orders" -H "Authorization: Bearer $T" -H "Idempotency-Key: chaos-$RANDOM" -H "$JSON" -d "$ORDER")
[ "$c" = 401 ] && echo "ok: 401 (introspect: inactive)" || fail "blocked user on POST /orders: want 401, got $c"
c=$(code "$G/auth/me" -H "Authorization: Bearer $T")
[ "$c" = 403 ] && echo "ok: /auth/me 403 для заблокированного" || fail "blocked /auth/me: want 403, got $c"

echo "== chaos: auth недоступен (fail-closed только для sensitive)"
T=$(login +79990000000 123456)
kill "${PIDS[0]}" 2>/dev/null || true
sleep 1
c=$(code -X POST "$G/orders" -H "Authorization: Bearer $T" -H "Idempotency-Key: chaos-$RANDOM" -H "$JSON" -d "$ORDER")
[ "$c" = 503 ] && echo "ok: 503 AUTH_UNAVAILABLE" || fail "sensitive with auth down: want 503, got $c"
c=$(code "$G/orders/00000000-0000-4000-8000-000000000000" -H "Authorization: Bearer $T")
[ "$c" = 404 ] && echo "ok: JWT проверяется локально, чтение работает (404 заказа)" || fail "read with auth down: want 404, got $c"
c=$(code "$G/actuator/health")
[ "$c" = 200 ] && echo "ok: readiness gateway не зависит от доступности auth" || fail "gateway readiness with auth down: got $c"

[ "$rc" = 0 ] || { echo "--- логи сервисов"; tail -n 40 "$WORK"/*.log; }
exit "$rc"
