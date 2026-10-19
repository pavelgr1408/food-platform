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
.PHONY: new-service keys docker-build newman check-template contract-check
new-service: ## Новый сервис из шаблона: make new-service NAME=orders PORT=8084
	tools/new-service/new-service.sh "$(NAME)" "$(PORT)"
keys: ## Dev RSA-ключи JWT: make keys SERVICE=auth (в git не попадают)
	@test -n "$(SERVICE)" || { echo "SERVICE is required"; exit 2; }
	@d=services/$(SERVICE)/config/keys; mkdir -p $$d; \
	if [ ! -f $$d/private.pem ]; then \
	  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out $$d/private.pem 2>/dev/null && \
	  openssl pkey -in $$d/private.pem -pubout -out $$d/public.pem && chmod 600 $$d/private.pem && echo "generated $$d"; \
	else echo "$$d already has keys"; fi
docker-build: ## Образ сервиса: make docker-build SERVICE=auth [TAG=dev]
	@test -n "$(SERVICE)" || { echo "SERVICE is required"; exit 2; }
	docker build -f deploy/docker/Dockerfile --build-arg SRC=services/$(SERVICE) \
	  --build-arg TARGET=./services/$(SERVICE)/cmd/$(SERVICE)-service -t food/$(SERVICE):$${TAG:-dev} .
newman: ## Postman-коллекция сервиса: make newman SERVICE=auth [BASE_URL=http://arch.homework]
	@test -n "$(SERVICE)" || { echo "SERVICE is required"; exit 2; }
	npx -y newman@6 run postman/$(SERVICE)/$(SERVICE).postman_collection.json $(if $(BASE_URL),--env-var baseUrl=$(BASE_URL))
SCENARIO     ?= catalog
contract-check: pydeps-check ## Живые ответы против OpenAPI: сценарий catalog или orders (см. docs/dev.md)
	$(PYTHON) tools/contract-check/check.py $(SCENARIO)
check-template: pydeps-check ## Шаблон сервиса генерирует собираемый и тестируемый сервис
	tools/new-service/check-template.sh

## ---------- контракты ----------
.PHONY: lint-contracts
lint-contracts: check-topics ## Redocly (OpenAPI) + AsyncAPI validate + topics.txt
	@for f in contracts/openapi/*.yaml; do npx -y @redocly/cli@1.34.5 lint --config redocly.yaml $$f || exit 1; done
	npx -y @asyncapi/cli@2 validate contracts/asyncapi/kafka-events.yaml

## ---------- Kubernetes / Helm ----------
.PHONY: validate-values helm-lint k8s-dev-secrets k8s-smoke-auth k8s-clean
validate-values: pydeps-check ## deploy/values/*.yaml против values.schema.json
	$(PYTHON) tools/validate-values/validate_values.py
helm-lint: validate-values ## helm lint + template для каждого values-файла
	@for v in deploy/values/*.yaml; do n=$$(basename $$v .yaml); \
	  helm lint deploy/charts/service -f $$v && helm template $$n deploy/charts/service -f $$v >/dev/null || exit 1; done
k8s-dev-secrets: ## Secret'ы dev-стенда в NS (БД + JWT-ключи auth); в git не попадают
	kubectl create namespace $(NS) --dry-run=client -o yaml | kubectl apply -f -
	$(MAKE) keys SERVICE=auth
	kubectl -n $(NS) create secret generic auth-secrets \
	  --from-literal=DATABASE_URL='postgres://auth:auth@postgres:5432/auth?sslmode=disable' --dry-run=client -o yaml | kubectl apply -f -
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
k8s-clean: ## Удалить smoke-стенд
	-helm uninstall auth -n $(NS)
	-kubectl delete namespace $(NS)

## ---------- python-утилиты ----------
.PHONY: py-deps pydeps-check
py-deps: ## Создать .dev/venv и поставить pyyaml/jsonschema (нужно для topics, validate-values, check-template)
	python3 -m venv .dev/venv
	.dev/venv/bin/pip install -q -r tools/requirements.txt
	@echo "готово: .dev/venv"
pydeps-check:
	@$(PYTHON) -c 'import yaml, jsonschema' 2>/dev/null || { echo "Нет python-зависимостей (pyyaml, jsonschema). Выполните: make py-deps"; exit 1; }
