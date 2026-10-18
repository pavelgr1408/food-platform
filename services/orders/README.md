# orders-service

Ядро заказа (UC-ORD-04…07, ADR-10): корзина, заказ, автомат статусов, outbox → Kafka, идемпотентное оформление.
Оплата — S3 (payment-service), кухня — S4; здесь заказ доходит до `pending_payment` / `accepted` и принимает переходы.

| | |
|---|---|
| Порт | `8084` |
| БД | `orders` (`postgres://orders:orders@localhost:5432/orders`) |
| Контракт | `contracts/openapi/orders.yaml` (1.2.0); события — `contracts/asyncapi/kafka-events.yaml` (`orders.state`, `orders.status`) |
| Probes | `GET /health/`, `GET /actuator/health`, `GET /metrics` |
| Env | `DATABASE_URL` (обяз.), `HTTP_PORT`, `APP_ENV`, `LOG_LEVEL`, `AUTH_MODE`, `KAFKA_BROKERS` (пусто — relay выключен, события копятся в outbox), `MENU_URL`, `RESTAURANT_URL`, `UPSTREAM_TIMEOUT` (800ms), `PAYMENT_TIMEOUT` (15m), `CART_TTL` (168h), `JANITOR_INTERVAL` (30s), `ETA_PREP_MINUTES`, `ETA_DELIVERY_MINUTES` |

## Запуск
```bash
make up SLICE="restaurant menu orders"
make newman SERVICE=orders
make contract-check SCENARIO=orders
```

## Как устроено
- `internal/domain` — таблица переходов статусов, способы оплаты/получения, слот самовывоза, нормализация телефона, номер `R<код>-<YYMMDD>-<NNNN>`.
- `internal/service` — сценарии. Вызовы menu/restaurant — **вне транзакций** (таймаут, 1 ретрай, circuit breaker); недоступность → `503` (fail-closed), корзина сохраняется.
- Оформление: одна транзакция — блокировка корзины, номер, заказ, позиции, история, события в outbox и ответ под `Idempotency-Key`. Либо всё, либо ничего.
- Идемпотентность (`pkg/idempotency`): повтор с тем же ключом и телом → тот же ответ + `Idempotent-Replay: true`; другое тело → `422 IDEMPOTENCY_KEY_REUSED`; параллельные одинаковые ключи → один заказ. Страховка: `orders.cart_id UNIQUE`.
- Цена: сервер пересчитывает по menu; изменение → `409 PRICE_CHANGED` (корзина обновлена, нужен новый ключ); стоп-лист → `409 ITEMS_UNAVAILABLE`.
- События: `orders.state` (revision растёт: создание=1, смена слота/оплата — +1) и `orders.status`, ключ партиции = `orderId`.
- Janitor: `pending_payment` старше `PAYMENT_TIMEOUT` → `cancelled`; брошенные корзины старше `CART_TTL` удаляются.
- Деньги — `int64` копейки (ADR-13).

## Ограничения (осознанные)
- `channel=staff_fax`, `searchOrder`, `markOrderIssued` — `x-status: planned` (S6); радиус доставки по адресу — S7; скидки — `NoopDiscounter` (S8, таблицы `promotions_replica` пока нет); ETA — типовое время, `approximate=true` (до S4).
- Счётчик номеров — строка на (ресторан, день): горячая точка при высокой нагрузке (S11).

## Тесты
`make test` · `make test-int` (Postgres): домен, сервис (гонки, откат, outbox после «падения»), HTTP (идемпотентность, владение).
