# food-platform — единая точка входа разработчика.  `make help`
SHELL := /bin/bash
.DEFAULT_GOAL := help

# Срез сервисов, поднимаемых в контейнерах поверх инфраструктуры: make up SLICE="auth restaurant menu"
SLICE        ?=
PG_URL_TEST  ?= postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable
COMPOSE      := docker compose --project-directory . -f deploy/compose/infra.yml $(foreach s,$(SLICE),-f deploy/compose/services/$(s).yml)
GO_PKGS      := ./...
NS           ?= food
SERVICE      ?=
BASE_URL     ?=
# Python-утилиты (gen-topics, validate-values): venv из `make py-deps`, иначе системный python3
PYTHON       = $(if $(wildcard .dev/venv/bin/python),$(CURDIR)/.dev/venv/bin/python,python3)
export PYTHON

.PHONY: help
help: ## Список команд
	@awk 'BEGIN{FS=":.*##"} /^[a-zA-Z0-9_.-]+:.*##/{printf "  \033[36m%-20s\033[0m %s\n",$$1,$$2}' $(MAKEFILE_LIST)

## ---------- стенд ----------
.PHONY: up down reset ps logs topics check-topics
up: ## Postgres + Kafka (+ срез SLICE="svc …"); Redis: COMPOSE_PROFILES=redis
	$(COMPOSE) up -d --build
	@echo "Postgres :5432 · Kafka (host) :29092 · топики создаёт kafka-init (docker compose logs kafka-init)"
down: ## Остановить (данные сохраняются)
	$(COMPOSE) down
reset: ## Остановить и УДАЛИТЬ тома (чистые БД и топики)
	$(COMPOSE) down -v
ps: ## Состояние контейнеров
	$(COMPOSE) ps
logs: ## Логи: make logs SERVICE=kafka
	$(COMPOSE) logs -f --tail=100 $(SERVICE)
topics: pydeps-check ## Перегенерировать deploy/compose/kafka/topics.txt из AsyncAPI
	$(PYTHON) tools/gen-topics/gen_topics.py
check-topics: pydeps-check ## topics.txt соответствует AsyncAPI
	$(PYTHON) tools/gen-topics/gen_topics.py --check

## ---------- код ----------
.PHONY: fmt fmt-check vet lint test test-int build
fmt: ## gofmt -w
	gofmt -w pkg services tools
fmt-check: ## gofmt без правок: падает, если есть неотформатированное
	@test -z "$$(gofmt -l pkg services tools)" || { gofmt -l pkg services tools; echo "gofmt needed: make fmt"; exit 1; }
vet: ## go vet
	go vet $(GO_PKGS)
lint: vet fmt-check ## vet + gofmt + staticcheck
	go run honnef.co/go/tools/cmd/staticcheck@2025.1.1 $(GO_PKGS)
test: ## Unit-тесты (интеграционные с Postgres пропускаются)
	go test -race $(GO_PKGS)
test-int: ## Все тесты, включая Postgres (нужен `make up`)
	TEST_DATABASE_URL='$(PG_URL_TEST)' go test -race -count=1 $(GO_PKGS)
build: ## Собрать все пакеты
	go build $(GO_PKGS)

## ---------- сервисы ----------
.PHONY: new-service keys docker-build newman newman-e2e-gateway newman-staff-timeout check-template contract-check contract-check-ci check-routes perimeter-check loyalty-e2e
new-service: ## Новый сервис из шаблона: make new-service NAME=orders PORT=8084
	tools/new-service/new-service.sh "$(NAME)" "$(PORT)"
keys: ## Dev RSA-ключи JWT: make keys SERVICE=auth (в git не попадают)
	@test -n "$(SERVICE)" || { echo "SERVICE is required"; exit 2; }
	@d=services/$(SERVICE)/config/keys; mkdir -p $$d; \
	if [ ! -f $$d/private.pem ]; then \
	  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out $$d/private.pem 2>/dev/null && \
	  openssl pkey -in $$d/private.pem -pubout -out $$d/public.pem && chmod 600 $$d/private.pem && echo "generated $$d"; \
	else echo "$$d already has keys"; fi
SRC          ?= services/$(SERVICE)
TARGET       ?= ./services/$(SERVICE)/cmd/$(SERVICE)-service
docker-build: ## Образ: make docker-build SERVICE=auth [TAG=dev]; для не-сервисов SRC/TARGET (mock-externals: SERVICE=mock-externals SRC=tools/mock-externals TARGET=./tools/mock-externals)
	@test -n "$(SERVICE)" || { echo "SERVICE is required"; exit 2; }
	docker build -f deploy/docker/Dockerfile --build-arg SRC=$(SRC) \
	  --build-arg TARGET=$(TARGET) -t food/$(SERVICE):$${TAG:-dev} .
newman: ## Postman-коллекция сервиса: make newman SERVICE=auth [BASE_URL=http://arch.homework]
	@test -n "$(SERVICE)" || { echo "SERVICE is required"; exit 2; }
	npx -y newman@6 run postman/$(SERVICE)/$(SERVICE).postman_collection.json $(if $(BASE_URL),--env-var baseUrl=$(BASE_URL))
newman-e2e-gateway: ## S7: сквозной e2e через gateway с JWT сотрудника (стенд: AUTH_MODE=jwks make up SLICE="auth gateway restaurant menu orders payment delivery loyalty mock-externals")
	@T=$$(curl -fsS -X POST localhost:8080/auth/login -H 'Content-Type: application/json' -d '{"phone":"+79993333333","password":"staff123"}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["accessToken"])') && \
	  O=$$(curl -fsS -X POST localhost:8080/auth/login -H 'Content-Type: application/json' -d '{"phone":"+79995555555","password":"owner123"}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["accessToken"])') && \
	  npx -y newman@6 run postman/e2e/e2e.postman_collection.json -e postman/e2e/gateway.postman_environment.json --env-var staffToken=$$T --env-var ownerToken=$$O
newman-staff-timeout: ## S6: not_received по таймеру (orders запущен с NOT_RECEIVED_AFTER=10s JANITOR_INTERVAL=2s, см. docs/dev.md)
	npx -y newman@6 run postman/staff/not-received.postman_collection.json
SCENARIO     ?= catalog
contract-check: pydeps-check ## Живые ответы против OpenAPI: сценарий catalog, orders, payment, staff или delivery (см. docs/dev.md)
	$(PYTHON) tools/contract-check/check.py $(SCENARIO)
contract-check-ci: pydeps-check ## contract-check без Docker: сам собирает и запускает restaurant/menu/orders (нужен Postgres на :5432)
	PYTHON=$(PYTHON) tools/contract-check/ci.sh
check-template: pydeps-check ## Шаблон сервиса генерирует собираемый и тестируемый сервис
	tools/new-service/check-template.sh

## ---------- контракты ----------
.PHONY: lint-contracts
lint-contracts: check-topics check-routes ## Redocly (OpenAPI) + AsyncAPI validate + topics.txt + таблица маршрутов gateway
	@for f in contracts/openapi/*.yaml; do npx -y @redocly/cli@1.34.5 lint --config redocly.yaml $$f || exit 1; done
	cd tools/validate-asyncapi && npm ci --silent && node validate.mjs ../../contracts/asyncapi/kafka-events.yaml

check-routes: pydeps-check ## Таблица маршрутов gateway ↔ x-routes ↔ контракты сервисов (без запуска)
	$(PYTHON) tools/contract-check/check_routes.py
perimeter-check: ## Периметр без Docker: auth+restaurant+menu+orders+payment+gateway на Postgres, newman auth+gateway, chaos (нужны Go, openssl, psql, newman/npx)
	tools/perimeter/ci.sh
loyalty-e2e: ## S8 без Docker: loyalty → Kafka (in-memory kfake) → orders, скидка в корзине/заказе, newman папка 12 (нужны Go, psql, newman/npx)
	tools/loyalty-e2e/ci.sh

## ---------- Kubernetes / Helm ----------
.PHONY: validate-values helm-lint k8s-dev-secrets k8s-smoke-auth k8s-smoke-perimeter k8s-smoke-perimeter-checks k8s-clean
validate-values: pydeps-check ## deploy/values/*.yaml против values.schema.json
	$(PYTHON) tools/validate-values/validate_values.py
helm-lint: validate-values ## helm lint + template для каждого values-файла
	@for v in deploy/values/*.yaml; do n=$$(basename $$v .yaml); \
	  helm lint deploy/charts/service -f $$v && helm template $$n deploy/charts/service -f $$v >/dev/null || exit 1; done
k8s-dev-secrets: ## Secret'ы dev-стенда в NS (БД сервисов + JWT-ключи auth); в git не попадают
	kubectl create namespace $(NS) --dry-run=client -o yaml | kubectl apply -f -
	$(MAKE) keys SERVICE=auth
	@for svc in auth restaurant menu orders payment delivery loyalty; do \
	  kubectl -n $(NS) create secret generic $$svc-secrets \
	    --from-literal=DATABASE_URL="postgres://$$svc:$$svc@postgres:5432/$$svc?sslmode=disable" --dry-run=client -o yaml | kubectl apply -f - || exit 1; done
	kubectl -n $(NS) create secret generic auth-jwt \
	  --from-file=private.pem=services/auth/config/keys/private.pem --from-file=public.pem=services/auth/config/keys/public.pem \
	  --dry-run=client -o yaml | kubectl apply -f -
k8s-smoke-auth: SERVICE := auth
k8s-smoke-auth: docker-build k8s-dev-secrets ## Smoke: auth в локальном k8s (Docker Desktop k8s / kind: KIND_CLUSTER=kind)
	$(if $(KIND_CLUSTER),kind load docker-image food/auth:dev --name $(KIND_CLUSTER),@echo "образ берётся из локального Docker (Docker Desktop k8s)")
	kubectl -n $(NS) apply -f deploy/k8s-dev/postgres.yaml
	kubectl -n $(NS) rollout status deploy/postgres --timeout=120s
	helm upgrade --install auth deploy/charts/service -n $(NS) -f deploy/values/auth.yaml --wait --timeout 3m
	@echo "→ port-forward: kubectl -n $(NS) port-forward svc/auth 8081:80 ; curl localhost:8081/actuator/health"
k8s-smoke-perimeter: ## Smoke периметра в локальном k8s: ingress-nginx + postgres + kafka + сервисы + gateway, newman и проверки через ingress (см. docs/dev.md)
	tools/k8s-smoke/up.sh
k8s-smoke-perimeter-checks: ## Только проверки периметра через ingress (стенд уже поднят)
	tools/k8s-smoke/up.sh checks
k8s-clean: ## Удалить smoke-стенд (релизы и namespace; ingress-nginx остаётся)
	tools/k8s-smoke/up.sh clean

## ---------- python-утилиты ----------
.PHONY: py-deps pydeps-check
py-deps: ## Создать .dev/venv и поставить pyyaml/jsonschema (нужно для topics, validate-values, check-template)
	python3 -m venv .dev/venv
	.dev/venv/bin/pip install -q -r tools/requirements.txt
	@echo "готово: .dev/venv"
pydeps-check:
	@$(PYTHON) -c 'import yaml, jsonschema' 2>/dev/null || { echo "Нет python-зависимостей (pyyaml, jsonschema). Выполните: make py-deps"; exit 1; }
