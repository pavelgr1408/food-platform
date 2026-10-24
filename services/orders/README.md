# orders-service

Ядро заказа (UC-ORD-04…07, ADR-10): корзина, заказ, автомат статусов, outbox → Kafka, идемпотентное оформление.
Оплата — S3 (payment-service), производство и ETA — S4: заказ проходит `pending_payment → accepted → cooking → ready`.

| | |
|---|---|
| Порт | `8084` |
| БД | `orders` (`postgres://orders:orders@localhost:5432/orders`) |
| Контракт | `contracts/openapi/orders.yaml` (1.5.0); события — `contracts/asyncapi/kafka-events.yaml` (публикует `orders.state`, `orders.status`; потребляет `orders.payments.status`, `orders.dish.status`, `production.lead.time`) |
| Probes | `GET /health/`, `GET /actuator/health`, `GET /metrics` |
| Env | `DATABASE_URL` (обяз.), `HTTP_PORT`, `APP_ENV`, `LOG_LEVEL`, `AUTH_MODE`, `KAFKA_BROKERS` (пусто — relay выключен, события копятся в outbox), `MENU_URL`, `RESTAURANT_URL`, `UPSTREAM_TIMEOUT` (800ms), `PAYMENT_TIMEOUT` (15m), `PAYMENT_UNKNOWN_GRACE`, `LEAD_TIME_TTL` (15m), `CART_TTL` (168h), `JANITOR_INTERVAL` (30s), `NOT_RECEIVED_AFTER` (2h: готовый заказ на самовывоз без выдачи → `not_received`), `ETA_PREP_MINUTES`, `ETA_DELIVERY_MINUTES` |

## Запуск
```bash
make up SLICE="restaurant menu orders"
make newman SERVICE=orders
make contract-check SCENARIO=orders
```
Сквозной сценарий M1 (заказ → оплата → производство → ready): `make up SLICE="restaurant menu orders payment mock-externals"` и `make newman SERVICE=e2e`.

## Как устроено
- `internal/domain` — таблица переходов статусов, способы оплаты/получения, слот самовывоза, нормализация телефона, номер `R<код>-<YYMMDD>-<NNNN>`.
- `internal/service` — сценарии. Вызовы menu/restaurant — **вне транзакций** (таймаут, 1 ретрай, circuit breaker); недоступность → `503` (fail-closed), корзина сохраняется.
- Оформление: одна транзакция — блокировка корзины, номер, заказ, позиции, история, события в outbox и ответ под `Idempotency-Key`. Либо всё, либо ничего.
- Идемпотентность (`pkg/idempotency`): повтор с тем же ключом и телом → тот же ответ + `Idempotent-Replay: true`; другое тело → `422 IDEMPOTENCY_KEY_REUSED`; параллельные одинаковые ключи → один заказ. Страховка: `orders.cart_id UNIQUE`.
- Цена: сервер пересчитывает по menu; изменение → `409 PRICE_CHANGED` (корзина обновлена, нужен новый ключ); стоп-лист → `409 ITEMS_UNAVAILABLE`.
- События: `orders.state` (revision растёт: создание=1, смена слота/оплата — +1) и `orders.status`, ключ партиции = `orderId`.
- **Производство (S4).** Две consumer group: `orders-service` (`orders.payments.status`) и `orders-service-production` (`orders.dish.status`, `production.lead.time`) — медленное или «ядовитое» событие производства не задерживает оплату. `orders.dish.status` — снимок статусов всех блюд заказа; `domain.AggregateDishes` выводит статус заказа: все `queued` → `accepted`, любое `cooking` → `cooking`, все `ready` → `ready` (из `accepted` через `cooking`, два `orders.status`). Снимок старше применённого (`production_updated_at`) игнорируется, статус не откатывается, события по `pending_payment`/`cancelled` игнорируются (`orders_production_events_total{result="ignored_status"}`). Неизвестный заказ → ошибка → повторы и DLQ.
- **ETA.** `production.lead.time` хранится последним значением на ресторан (`production_lead_time_replica`). `getOrderEta`: `readyAt` = момент принятия (`accepted_at`) + лид-тайм ресторана, для самовывоза со слотом — не раньше начала слота; реплика свежая (`LEAD_TIME_TTL`) → `approximate=false`, иначе типовое `ETA_PREP_MINUTES` и `approximate=true` (E1). Для доставки поверх добавляется типовое `ETA_DELIVERY_MINUTES` (`approximate=true` до S7). Для `accepted`/`cooking` ETA публикуется и в `orders.status` (`etaReadyAt`). У готового заказа `readyAt` — момент готовности из журнала.
- **Метрики S4.** `orders_stuck_in_accepted_seconds` (возраст самого старого заказа в `accepted`; алерт E3 «производство не берёт заказы»), `orders_accepted`, `orders_production_events_total{event,result}`.
- **Сценарии персонала (S6).** Роли STAFF/FRANCHISE_OWNER/HQ_STAFF (`authclient.RequireRole`: гость 401, прочие 403), доступ к ресторанам — в сервисе (`Actor.Can`), чужой заказ → `404` (существование не раскрываем). `createOrder` с `channel=staff_fax` — от имени сотрудника, заказ без владельца, `contact.phone` обязателен, предоплата (`online_card`) запрещена (`422`); `created_by` пишется в заказ. `searchOrder` (`GET /orders/search?number=…`) — по номеру в пределах ресторанов сотрудника. `markOrderIssued` (`POST /orders/{id}/issuance`) — `ready → handed_over → closed` одной транзакцией (только самовывоз, оплата принята, иначе `409 ORDER_NOT_PAID`; повтор — без эффекта). `cancelOrder` (`POST /orders/{id}/cancellation`, причина из перечня) — `→ cancelled`, возврат запускает payment-service по `orders.status`. Janitor: готовый заказ на самовывоз старше `NOT_RECEIVED_AFTER` → `not_received` (`orders_status_transitions_total{to="not_received"}`), индекс `orders_ready_idx`. Статус возврата приходит в `orders.payments.status.refundStatus` и отдаётся как `Order.refundStatus` (`orders_refund_events_total{status}`).
- Janitor: `pending_payment` старше `PAYMENT_TIMEOUT` → `cancelled`; брошенные корзины старше `CART_TTL` удаляются.
- Деньги — `int64` копейки (ADR-13).

## Ограничения (осознанные)
- Отмена отдельных блюд на производстве (долг №29) — позже; радиус доставки по адресу — S7; скидки — `NoopDiscounter` (S8, таблицы `promotions_replica` пока нет); ETA доставки — типовое (S7).
- Счётчик номеров — строка на (ресторан, день): горячая точка при высокой нагрузке (S11).

## Тесты
`make test` · `make test-int` (Postgres): домен, сервис (гонки, откат, outbox после «падения»), HTTP (идемпотентность, владение).
