# loyalty-service

Акции и скидки (S8): персонал создаёт, правит, активирует и завершает акции, клиент (в том числе гость)
видит действующие. Скидку **считает orders-service** по собственной реплике — loyalty только хранит акции и
публикует их в `promotions.discount` (outbox, компактный топик, ключ — `promotionId`).

| | |
|---|---|
| Порт | `8086` |
| БД | `loyalty` (`postgres://loyalty:loyalty@localhost:5432/loyalty`) |
| Контракт | `contracts/openapi/loyalty.yaml` 1.2.0, событие `PromotionDiscount` в `contracts/asyncapi/kafka-events.yaml` |
| Probes | `GET /health/` (liveness), `GET /actuator/health` (readiness), `GET /metrics` |

## Операции
`GET /promotions` (гость) · `POST /promotions` (`Idempotency-Key` обязателен) · `GET|PUT /promotions/{id}` ·
`GET /promotions/{id}/history` · `POST /promotions/{id}/activation` · `POST /promotions/{id}/completion`.
Статусы `draft → active → completed`; «запланирована» = `active` до `startsAt`. Каждое изменение увеличивает
`version` и пишется в `promotion_history` (снимок в том виде, в каком его отдаёт API).

## Права
Роли `FRANCHISE_OWNER` / `HQ_STAFF` проверяет middleware, **охват** (свой ресторан или национальная) — auth-service:
`POST /auth/authorize` с токеном пользователя (`AUTH_MODE=jwks`, нужен `AUTH_URL`). Auth недоступен — `503
AUTH_UNAVAILABLE` (fail-closed). В `AUTH_MODE=dev` токена нет, поэтому те же правила применяются к claims из
`X-User-*` (`internal/authz.Claims`; таблица теста повторяет тест auth) — режим запрещён в prod.

## Конфликты (E2)
Две действующие акции конфликтуют, если обе не `stackable`, приоритет равен, пересекаются период, охват и позиции.
Проверка и запись — под advisory-lock (`LockActivation`), поэтому параллельные активации не обходят правило.

## Запуск
```bash
make up SLICE="auth loyalty"          # Postgres + Kafka + сервисы
AUTH_MODE=jwks make up SLICE="auth loyalty"   # права через auth /authorize
make newman SERVICE=loyalty           # Postman-коллекция
```
Локально без контейнера:
```bash
DATABASE_URL='postgres://loyalty:loyalty@localhost:5432/loyalty?sslmode=disable' \
KAFKA_BROKERS=localhost:29092 go run ./services/loyalty/cmd/loyalty-service
```

## Env
`DATABASE_URL`, `HTTP_PORT` (8086), `APP_ENV`, `LOG_LEVEL`, `AUTH_MODE`, `AUTH_JWKS_URL`, `AUTH_URL` (обязателен при
`jwks`), `AUTH_TIMEOUT` (2s), `KAFKA_BROKERS` (пусто — события копятся в outbox).

## Метрики
`loyalty_promotion_actions_total{action,result}`, `loyalty_authz_total{result}` + RED + outbox.
