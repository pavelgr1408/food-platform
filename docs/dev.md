# Dev-стенд и работа срезами

Железо: MacBook Air M3 / 16 ГБ, Docker Desktop (лимит VM ≈ 10 ГБ / 6 CPU). Поднимаем **только** то, что нужно текущей задаче.

## Инфраструктура
| Компонент | Образ | Порт на хосте | Память |
|---|---|---|---|
| PostgreSQL 17 (БД и роль на сервис: `auth`, `restaurant`, `menu`, `orders`, `payment`, `loyalty`, `delivery`, `notification`; пароль = имя, **только dev**) | `postgres:17-alpine` | 5432 | ≈150 МБ |
| Kafka 3.9 KRaft, 1 брокер, автосоздание топиков выключено | `apache/kafka:3.9.0` | 29092 (с хоста), `kafka:9092` (внутри) | ≈700 МБ (heap 512m) |
| Redis (с S7) | `redis:7-alpine` | 6379 | ≈20 МБ — `COMPOSE_PROFILES=redis make up` |

`kafka-init` создаёт топики из `deploy/compose/kafka/topics.txt` — файл **генерируется** из AsyncAPI (`make topics`), CI проверяет актуальность.
Для каждого топика есть `<topic>.dlq`.

## Срезы
`make up SLICE="a b"` добавляет `deploy/compose/services/<a>.yml` и `<b>.yml`. Файл среза создаёт `make new-service`.
Бюджет по плану: S1 ≈ 300 МБ сверх инфраструктуры, S2 ≈ 2.2 ГБ всего, … полный набор — только в облаке.

Быстрый цикл активного сервиса — `go run` на хосте при поднятой инфраструктуре:
```bash
make up
DATABASE_URL='postgres://orders:orders@localhost:5432/orders?sslmode=disable' KAFKA_BROKERS=localhost:29092 \
  go run ./services/orders/cmd/orders-service
```
`go run` с `--migrate-only` применяет миграции и выходит (в k8s это pre-upgrade Job).

## Тесты
- `make test` — быстрые; интеграционные пропускаются без `TEST_DATABASE_URL`.
- `make test-int` — с Postgres из `make up` (временные БД `test_xxxxxxxx` создаются и удаляются автоматически).
- Kafka в тестах — in-memory брокер `kfake` (без Docker); с настоящим брокером — в Postman/e2e-срезах.

## Что нельзя
- Коммитить `.env`, `*.pem`, пароли и токены. Новые ключи — только через `make keys`. gitleaks в CI.
- Менять настройки Docker Desktop (кроме лимитов ресурсов).

## Каталог (S1): restaurant + menu
```bash
make up SLICE="restaurant menu"
make newman SERVICE=restaurant
make newman SERVICE=menu
make newman SERVICE=catalog
make contract-check
```
`SEED_ON_START=true` в срезе загружает демо-рестораны и меню (идемпотентно). `make contract-check` валидирует живые
ответы обоих сервисов против `contracts/openapi/*.yaml`.
