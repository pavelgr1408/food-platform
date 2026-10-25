# delivery-service (S7)

Доставка заказа курьером: берёт заказ из `orders.state`, заводит заявку в курьерской системе, ведёт статусы
(`searching → assigned → picked_up → en_route → delivered | failed`), отдаёт координаты курьера из Redis.

| | |
|---|---|
| Порт | `8087` |
| БД | `delivery` (`postgres://delivery:delivery@localhost:5432/delivery`) |
| Redis | координаты курьера, ключ `delivery:courier-location:{orderId}`, TTL `LOCATION_TTL` (60 с) |
| Контракт | `contracts/openapi/delivery.yaml`, `contracts/asyncapi/` (`orders.delivery.status`, `orders.payments.link`) |
| Probes | `GET /health/`, `GET /actuator/health`, `GET /metrics` |

## API
`GET /orders/{orderId}/delivery`, `GET /orders/{orderId}/delivery/courier-location` (через gateway) и
`POST /webhooks/delivery/status` (мимо gateway, подпись `X-Signature`, секрет `COURIER_WEBHOOK_SECRET`).

## Как устроено
- Заказ попадает в доставку по `orders.state` (`fulfillmentType=delivery`, оплачен или оплата не предоплатная — ADR-10). Для оплаты курьеру сервис ждёт `orders.payments.link` до `LINK_WAIT`, затем отправляет заявку без ссылки и передаёт её позже.
- Вызовы курьерской системы идут вне транзакций БД, заявка идемпотентна по `deliveryId`; итог приходит вебхуком, недостающее добирает reconciler (`POLL_INTERVAL`, backoff до `POLL_BACKOFF_MAX`).
- Статус меняется в одном месте (`applyTx`) и публикуется в `orders.delivery.status` через outbox; устаревшие и повторные уведомления игнорируются, вебхуки дедуплицируются по `eventId`.
- Нет курьера за `SEARCH_TIMEOUT` → `failed/no_courier`, `hint=pickup`; заказ не отменяется (E2), клиент забирает его в ресторане.
- Контакты курьера отдаются только пока доставка активна (PII). Координаты в логи не пишутся.
- Redis недоступен → `getCourierLocation` отвечает `503 LOCATION_STORE_UNAVAILABLE`, остальное работает. Пустой `REDIS_ADDR` — координаты в памяти процесса (только dev).

## Конфигурация
`DATABASE_URL`, `KAFKA_BROKERS`, `REDIS_ADDR`, `REDIS_PASSWORD`, `RESTAURANT_URL`, `COURIER_URL`, `COURIER_API_KEY`,
`COURIER_WEBHOOK_SECRET` (вне dev обязательны, ≥16 символов), `SEARCH_TIMEOUT`, `LOCATION_TTL`, `POLL_INTERVAL`,
`POLL_BACKOFF_MAX`, `LINK_WAIT`, `RECONCILE_INTERVAL`, `AUTH_MODE`/`AUTH_JWKS_URL`.

## Запуск
```bash
COMPOSE_PROFILES=redis REDIS_ADDR=redis:6379 make up SLICE="restaurant menu orders payment delivery mock-externals"
make newman SERVICE=delivery
make newman SERVICE=e2e
```
Тесты: `TEST_DATABASE_URL=… TEST_REDIS_ADDR=127.0.0.1:6379 go test -race ./pkg/redisx/... ./services/delivery/...` (без `TEST_REDIS_ADDR` тест против настоящего Redis пропускается, остальное идёт на фейках).
