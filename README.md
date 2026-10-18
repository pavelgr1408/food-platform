# food-platform — платформа «Заказ еды»

Микросервисная платформа на Go (stdlib-first): 9 продуктовых сервисов + веб-клиенты, Kafka-шина, PostgreSQL на сервис.
Все внешние системы (платёжный шлюз, POS, курьерка, производство, push) — моки.

```
contracts/    OpenAPI per service + AsyncAPI (contract-first)
pkg/          общий код: config logging httpx metrics pgxutil kafka idempotency authclient webhook
services/     <name>/{cmd,internal,migrations}   — эталон слоёв: services/auth
tools/        mock-externals · service-template · new-service · gen-topics · validate-values
deploy/       compose (стенд, срезы) · charts/service (единый Helm-чарт) · values/<svc>.yaml · docker · k8s-dev
postman/      <svc>/*.postman_collection.json
docs/         dev.md (стенд и срезы) · s0-foundation.md (что сделано в S0)
```

## Быстрый старт

```bash
make help                  # все команды
make up                    # Postgres + Kafka (KRaft) + топики из AsyncAPI
make test                  # unit-тесты;  make test-int — вместе с Postgres
make new-service NAME=restaurant PORT=8082
make up SLICE="auth"       # + сервис в контейнере (срезы — docs/dev.md)
```

Порядок реализации — `IMPLEMENTATION-PLAN` (проект «Микросервисная архитектура»): S0 фундамент → S1 каталог → S2 заказ → S3 деньги → S4 производство → **M1 сквозной MVP** → S5…S11.

## Принципы
Contract-first · outbox/inbox для событий · `Idempotency-Key` на создающих операциях · деньги — целые минорные единицы ·
секреты не в git (`.env`, `*.pem` в `.gitignore`, gitleaks в CI) · `GET /health/` (liveness) + `GET /actuator/health` (readiness) + `GET /metrics` у каждого сервиса.
