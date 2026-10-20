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

Без Docker (так же работает CI-job `contract-check`): `make contract-check-ci` — сам собирает restaurant, menu и orders, создаёт БД в локальном Postgres (`DB_ADMIN_URL`, по умолчанию `postgres://postgres:postgres@localhost:5432/postgres`), запускает сервисы и прогоняет сценарии `catalog` и `orders`; нужен `psql` и `make py-deps`.

## Ядро заказа (S2): orders
```bash
make up SLICE="restaurant menu orders"
make newman SERVICE=orders
make contract-check SCENARIO=orders
```
`orders` вызывает `menu` (`validateMenuItems`) и `restaurant` по HTTP (`MENU_URL`, `RESTAURANT_URL`; в срезе — имена сервисов compose),
события `orders.state` / `orders.status` уходят в Kafka через outbox. Сценарий Postman идёт по ресторану №4 (Невский, 24/7), поэтому
не зависит от времени суток. Событие в Kafka (нужен срез с Kafka):
```bash
docker compose --project-directory . -f deploy/compose/infra.yml exec kafka /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server kafka:9092 --topic orders.state --from-beginning --property print.key=true --property print.headers=true --timeout-ms 5000
```

## Деньги (S3): payment
```bash
make up SLICE="restaurant menu orders payment mock-externals"
make newman SERVICE=payment
make contract-check SCENARIO=payment
```
`payment` получает заказы из Kafka (`orders.state`) и ходит в платёжный шлюз `GATEWAY_URL` — в срезе это `mock-externals` (:9091, модуль `payment`),
который по умолчанию подтверждает оплату вебхуком через `MOCK_DELAY` (2 с). Сценарии Postman «отказ», «таймаут → unknown», «потерянный вебхук» сами
переключают режимы мока через `/_admin/gateway/*` и возвращают их обратно. Коллекцию можно гонять повторно без пересоздания среза.
Ручная проверка двойного списания: `curl localhost:9091/_inspect/payments` — на заказ ровно одна операция шлюза.

## Производство и сквозной MVP (S4)
```bash
make up SLICE="restaurant menu orders payment mock-externals"
make newman SERVICE=e2e
```
`mock-externals` (по умолчанию `MOCKS=ping,payment,production`) содержит модуль `production` (:9092): читает `orders.state` (берёт заказ в работу, только если
`paymentStatus=paid` или оплата не предоплатная — ADR-10), публикует `orders.dish.status` (`queued → cooking → ready`) и `production.lead.time`.
Тайминги — `MOCK_PRODUCTION_QUEUE_DELAY` (2s), `MOCK_PRODUCTION_COOK_TIME` (3s) или на лету: `POST :9092/_admin/production/timing?queue=1s&cook=2s`.
Управление сценариями: `POST /_admin/production/mode?queue=off|on` («авария очереди», E3: заказы остаются `accepted`, растёт метрика
`orders_stuck_in_accepted_seconds`), `POST /_admin/production/leadtime?restaurantId=<id>&minutes=<n>` (зафиксировать время приготовления ресторана),
`POST /_admin/production/reset`, `GET /_inspect/production`. Коллекция `postman/e2e/` сама задаёт тайминги и возвращает настройки в конце.
Заказы без `production` в срезе остаются в `accepted`: orders честно показывает типовой ETA (`approximate=true`).

## Периметр (S5): auth + gateway
```bash
make keys SERVICE=auth
AUTH_MODE=jwks make up SLICE="auth restaurant menu orders payment mock-externals gateway"
make newman SERVICE=gateway     # через http://localhost:8080
```
`AUTH_MODE=jwks` заставляет сервисы среза проверять JWT сами (локально по JWKS auth); без переменной они остаются в dev-режиме
(личность из заголовков `X-User-*`) — так работают остальные срезы и их Postman-коллекции. За gateway личность берётся **только** из токена.
Входы: клиент `+79990000000 / 123456`, персонал ресторана №4 `+79993333333 / staff123` (остальные — в `services/auth/README.md`).

Без Docker (CI и песочница): `make perimeter-check` (= `tools/perimeter/ci.sh`) собирает auth, restaurant, menu, orders, payment и gateway,
поднимает их на Postgres :5432 и прогоняет contract-check (`auth`, `gateway`), newman (`auth`, `gateway`) и сценарии, которые Postman не умеет:
блокировка пользователя после выдачи токена и отказ auth (fail-closed только на sensitive-маршрутах). `HOLD=1` оставляет стенд поднятым.
`make check-routes` сверяет таблицу маршрутов gateway с `x-routes` и контрактами сервисов.

В k8s: наружу смотрит только ingress → `gateway` (`/`) и → `payment` (`/webhooks/payments`, мимо gateway; лимиты частоты и размера — аннотациями ingress).
`/metrics` gateway — на отдельном порту `9100` (в чарте `metrics.port`), через ingress недоступен. `TRUSTED_PROXY_CIDRS` в `deploy/values/gateway.yaml`
нужно привести к CIDR вашего ingress-контроллера.
