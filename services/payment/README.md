# payment-service

Скелет создан `make new-service NAME=payment PORT=8085`.

| | |
|---|---|
| Порт | `8085` |
| БД | `payment` (`postgres://payment:payment@localhost:5432/payment`) |
| Контракт | `contracts/openapi/payment.yaml` (contract-first: сначала контракт) |
| Probes | `GET /health/` (liveness), `GET /actuator/health` (readiness), `GET /metrics` |

## Запуск
```bash
make up                               # Postgres + Kafka
DATABASE_URL='postgres://payment:payment@localhost:5432/payment?sslmode=disable' \
KAFKA_BROKERS=localhost:29092 go run ./services/payment/cmd/payment-service
# или в контейнере:  make up SLICE="payment"
make newman SERVICE=payment          # Postman-коллекция
```

## Что уже подключено
config (pkg/config) · JSON-логи с request_id/trace_id · RED-метрики · health · миграции с
`--migrate-only` · outbox relay (если задан `KAFKA_BROKERS`) · идемпотентность · authclient (`AUTH_MODE=dev`).

## Что делать дальше
1. Описать операции в `contracts/openapi/payment.yaml`.
2. Слои `internal/{domain,service,store}` по образцу `services/auth`.
3. Заменить заглушку `/payment/_ping`.
4. Дополнить `postman/payment/` сценариями; `deploy/values/payment.yaml` — ресурсами и env.
