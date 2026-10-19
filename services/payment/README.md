# payment-service

Деньги (S3): онлайн-оплата картой через платёжный шлюз (в dev — mock), вебхуки, сверка состояния, согласование с orders (ADR-10).

| | |
|---|---|
| Порт | `8085` |
| БД | `payment` (`postgres://payment:payment@localhost:5432/payment`) |
| Контракт | `contracts/openapi/payment.yaml` (1.2.0), события — `contracts/asyncapi/kafka-events.yaml` (`orders.payments.status`) |
| Probes | `GET /health/` (liveness), `GET /actuator/health` (readiness: БД, Kafka), `GET /metrics` |

## Запуск
```bash
make up SLICE="restaurant menu orders payment mock-externals"
make newman SERVICE=payment
make contract-check SCENARIO=payment
```
Без Docker: `make contract-check-ci` (Kafka нет — заказ для payment засевается в БД скриптом).

## Как это устроено
- **Заказ приходит событием.** `orders.state` (ревизия заказа, сумма в копейках) и `orders.status` потребляются в `payment_orders` / `order_statuses` через inbox (`kafka.ProcessOnce`). Пока событие не дошло, API отвечает `404 ORDER_NOT_FOUND` с `Retry-After: 1`.
- **Платёж в БД до вызова шлюза.** `initiatePayment` (обязателен `Idempotency-Key`) пишет попытку `pending` в транзакции, затем вызывает шлюз **вне** транзакции; Create шлюза идемпотентен по ключу = `paymentId`. Один живой платёж на заказ и один успешный — уникальные частичные индексы.
- **Автомат** `pending → authorized → paid | declined | unknown`; `paid` и `declined` финальны, смена статуса — только в `applyTx` (под `FOR UPDATE`). Отказ открывает новую попытку (`attempt+1`), `unknown` блокирует повторную оплату до выяснения.
- **Три источника истины сходятся в одном месте:** ответ шлюза, вебхук (`POST /webhooks/payments/gateway`, HMAC `X-Signature: t=<unix>,v1=<hex>`, окно replay, дедупликация по `eventId`, сверка суммы) и reconciler (опрос шлюза с арендой `SKIP LOCKED`, backoff, истечение `PENDING_EXPIRY`).
- **Исход в orders** — событие `orders.payments.status` через outbox; orders принимает заказ (`pending_payment → accepted`) и повторно публикует `orders.state` с `revision+1`.
- **Деньги — только int64 копейки** (ADR-13).

## Конфигурация (env)
`DATABASE_URL`, `KAFKA_BROKERS`, `GATEWAY_URL`, `GATEWAY_API_KEY`, `GATEWAY_TIMEOUT` (5s), `GATEWAY_WEBHOOK_SECRET`, `POS_WEBHOOK_SECRET`, `WEBHOOK_WAIT` (10s), `PENDING_EXPIRY` (10m), `RECONCILE_BACKOFF_MIN/MAX` (5s/5m), `RECONCILE_INTERVAL` (2s), `DEFAULT_RETURN_URL`.
Секреты вне `APP_ENV=dev` обязательны, не короче 16 символов и не равны dev-значениям; в k8s — из Secret.

## Mock шлюза (`tools/mock-externals`, модуль `payment`, :9091)
Протокол: `POST /v1/payments` (Idempotency-Key), `GET /v1/payments/{id}`, `POST /v1/payments/{id}/cancel`. Исход по умолчанию — `paid` через `MOCK_DELAY` (вебхук с 4 попытками и подписью).
Магические суммы: `amount % 100 == 13` — отказ, `== 99` — «тишина» (вебхука нет, итог виден только опросом). Управление:
```bash
curl -XPOST 'localhost:9091/_admin/gateway/next?outcome=declined'
curl -XPOST 'localhost:9091/_admin/gateway/mode?init=timeout&status=error&webhooks=off'
curl -XPOST 'localhost:9091/_admin/gateway/redeliver?id=gw_xxx'
curl -XPOST 'localhost:9091/_admin/gateway/reset'
curl localhost:9091/_inspect/payments
```

## Не входит в S3
POS-инициация (`initiatePosPayment`, `x-status: planned`) и возврат средств — S6; `courier_card` и ссылка на оплату — S7; смена способа оплаты у существующего заказа.
