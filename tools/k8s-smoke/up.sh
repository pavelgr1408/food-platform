#!/usr/bin/env bash
# Smoke периметра в локальном k8s (долг 37): ingress-nginx + postgres + kafka + все сервисы S1–S6 + gateway.
#   up.sh           — поднять стенд, прогнать newman (gateway: папки 0,1,2,4) и checks.py
#   up.sh checks    — только checks.py (стенд уже поднят)
#   up.sh clean     — удалить стенд (релизы, namespace; ingress-nginx остаётся)
# Переменные: NS (food), INGRESS_HOST (localhost), INGRESS_URL (http://$INGRESS_HOST), KIND_CLUSTER, FRESH (1: пересоздать namespace),
#   INGRESS_NGINX_VERSION, SMOKE_ANY_CONTEXT=1 (разрешить не-локальный kube-context), SKIP_BUILD=1 (образы уже собраны).
# Совместим с bash 3.2 (macOS).
set -euo pipefail
cd "$(dirname "$0")/../.."

NS="${NS:-food}"
INGRESS_HOST="${INGRESS_HOST:-localhost}"
INGRESS_URL="${INGRESS_URL:-http://$INGRESS_HOST}"
INGRESS_NGINX_VERSION="${INGRESS_NGINX_VERSION:-controller-v1.15.1}"
FRESH="${FRESH:-1}"
PY="${PYTHON:-python3}"
CMD="${1:-up}"

log() { printf '\n== %s\n' "$*"; }
die() { echo "ОШИБКА: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "нужен $1"; }

for t in kubectl helm docker; do need "$t"; done
CTX="$(kubectl config current-context)"
case "$CTX" in
  docker-desktop|kind-*|minikube|rancher-desktop|orbstack) ;;
  *) [ "${SMOKE_ANY_CONTEXT:-}" = 1 ] || die "kube-context '$CTX' не похож на локальный (docker-desktop|kind-*|minikube|…). Если уверены: SMOKE_ANY_CONTEXT=1" ;;
esac
echo "kube-context: $CTX · namespace: $NS · ingress: $INGRESS_URL"

dump() {
  echo "--- диагностика (namespace $NS)"
  kubectl -n "$NS" get pods,jobs,ingress -o wide || true
  for p in $(kubectl -n "$NS" get pods --no-headers 2>/dev/null | awk '$3!="Running" && $3!="Completed"{print $1}'); do
    echo "--- describe/logs $p"
    kubectl -n "$NS" describe pod "$p" | tail -n 25 || true
    kubectl -n "$NS" logs "$p" --all-containers --tail=40 || true
  done
  kubectl -n ingress-nginx get pods -o wide || true
}

wait_deploy() { kubectl -n "$NS" rollout status "deploy/$1" --timeout="${2:-180s}"; }

checks() {
  log "checks.py (топология, HTTP через ingress, лимиты)"
  "$PY" tools/k8s-smoke/checks.py --ingress-url "$INGRESS_URL" --namespace "$NS"
}

clean() {
  log "удаляю стенд"
  for r in gateway payment orders menu restaurant auth; do helm uninstall "$r" -n "$NS" >/dev/null 2>&1 || true; done
  kubectl delete namespace "$NS" --ignore-not-found --wait=true
}

case "$CMD" in
  checks) checks; exit $? ;;
  clean) clean; exit 0 ;;
  up) ;;
  *) die "неизвестная команда '$CMD' (up|checks|clean)" ;;
esac

trap 'rc=$?; if [ $rc -ne 0 ]; then dump; fi; exit $rc' EXIT

if [ "$FRESH" = 1 ]; then clean; fi

if [ "${SKIP_BUILD:-}" != 1 ]; then
  log "сборка образов"
  for s in auth restaurant menu orders payment gateway; do make docker-build SERVICE="$s"; done
  docker build -f deploy/docker/Dockerfile --build-arg SRC=tools/mock-externals --build-arg TARGET=./tools/mock-externals \
    -t food/mock-externals:dev .
fi
if [ -n "${KIND_CLUSTER:-}" ]; then
  for s in auth restaurant menu orders payment gateway mock-externals; do kind load docker-image "food/$s:dev" --name "$KIND_CLUSTER"; done
fi

log "namespace, секреты, Postgres"
make k8s-dev-secrets NS="$NS"
kubectl -n "$NS" apply -f deploy/k8s-dev/postgres.yaml
wait_deploy postgres 180s
# init-скрипт образа срабатывает только на пустом каталоге данных: недостающие БД досоздаём идемпотентно
for svc in auth restaurant menu orders payment; do
  kubectl -n "$NS" exec deploy/postgres -- psql -U postgres -qtAc "SELECT 1 FROM pg_roles WHERE rolname='$svc'" | grep -q 1 \
    || kubectl -n "$NS" exec deploy/postgres -- psql -U postgres -qc "CREATE ROLE $svc LOGIN PASSWORD '$svc'"
  kubectl -n "$NS" exec deploy/postgres -- psql -U postgres -qtAc "SELECT 1 FROM pg_database WHERE datname='$svc'" | grep -q 1 \
    || kubectl -n "$NS" exec deploy/postgres -- psql -U postgres -qc "CREATE DATABASE $svc OWNER $svc"
done

log "Kafka и топики"
kubectl -n "$NS" delete job kafka-topics --ignore-not-found
kubectl -n "$NS" create configmap kafka-init \
  --from-file=create-topics.sh=deploy/compose/kafka/create-topics.sh --from-file=topics.txt=deploy/compose/kafka/topics.txt \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "$NS" apply -f deploy/k8s-dev/kafka.yaml
wait_deploy kafka 240s
kubectl -n "$NS" wait --for=condition=complete job/kafka-topics --timeout=240s

log "ingress-nginx $INGRESS_NGINX_VERSION"
if ! kubectl get ns ingress-nginx >/dev/null 2>&1; then
  kubectl apply -f "https://raw.githubusercontent.com/kubernetes/ingress-nginx/$INGRESS_NGINX_VERSION/deploy/static/provider/cloud/deploy.yaml"
fi
kubectl -n ingress-nginx rollout status deploy/ingress-nginx-controller --timeout=240s
CTRL_IPS="$(kubectl -n ingress-nginx get pods -l app.kubernetes.io/component=controller -o jsonpath='{.items[*].status.podIP}')"
[ -n "$CTRL_IPS" ] || die "не нашёл pod ingress-controller"
CTRL_IP="${CTRL_IPS%% *}"
echo "pod ingress-controller: $CTRL_IPS → TRUSTED_PROXY_CIDRS для smoke: $CTRL_IP/32"

log "сервисы (helm, 1 реплика, host=$INGRESS_HOST)"
COMMON=(-n "$NS" --set replicas=1 --set ingress.host="$INGRESS_HOST" --wait --timeout 4m)
helm upgrade --install auth deploy/charts/service -f deploy/values/auth.yaml "${COMMON[@]}"
for s in restaurant menu orders payment; do
  helm upgrade --install "$s" deploy/charts/service -f "deploy/values/$s.yaml" "${COMMON[@]}"
done
kubectl -n "$NS" apply -f deploy/k8s-dev/mock-externals.yaml
wait_deploy mock-externals 120s
# 30/30: newman логинится с одного IP, лимит по умолчанию (10/10) сработал бы раньше, чем дойдёт до проверок лимита
helm upgrade --install gateway deploy/charts/service -f deploy/values/gateway.yaml "${COMMON[@]}" \
  --set-string env.TRUSTED_PROXY_CIDRS="$CTRL_IP/32" --set-string env.AUTH_RATE_PER_MIN=30 --set-string env.AUTH_RATE_BURST=30

log "жду ответа ingress"
ok=0
for i in $(seq 1 60); do
  if curl -fsS -H "Host: $INGRESS_HOST" "$INGRESS_URL/actuator/health" >/dev/null 2>&1; then ok=1; break; fi
  sleep 2
done
[ "$ok" = 1 ] || die "ingress не отвечает на $INGRESS_URL (Docker Desktop: LoadBalancer ingress-nginx должен слушать localhost:80; kind/minikube — см. docs/dev.md)"

log "newman: gateway через ingress (папки 0,1,2,4; 3 и 5 — в checks.py)"
if command -v newman >/dev/null 2>&1; then NEW=newman; else NEW="npx -y newman@6"; fi
$NEW run postman/gateway/gateway.postman_collection.json --env-var "baseUrl=$INGRESS_URL" \
  --folder "0. Пробы и ключи" --folder "1. Вход и токены" --folder "2. Отказы на периметре (до сервисов)" \
  --folder "4. Заказ клиента через периметр (владелец — из JWT)"

checks
trap - EXIT
echo
echo "smoke периметра пройден. Подсеть pod'ов для TRUSTED_PROXY_CIDRS в values: kubectl get nodes -o jsonpath='{.items[*].spec.podCIDR}'"
