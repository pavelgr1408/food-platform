# payment-service

Деньги: онлайн-оплата картой через платёжный шлюз (S3), оплата на кассе персоналом и возвраты при отмене заказа (S6) — вебхуки, сверка состояния, согласование с orders (ADR-10). В dev шлюз и касса — модули `tools/mock-externals`.

| | |
|---|---|
| Порт | `8085` |
| БД | `payment` (`postgres://payment:payment@localhost:5432/payment`) |
| Контракт | `contracts/openapi/payment.yaml` (1.3.1), события — `contracts/asyncapi/kafka-events.yaml` (`orders.payments.status`) |
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
- **Автомат** `pending → authorized → paid | declined | unknown`; `paid` и `declined` финальны, смена статуса — только в `applyFiscalTx` (под `FOR UPDATE`). Отказ открывает новую попытку (`attempt+1`), `unknown` блокирует повторную оплату до выяснения.
- **Три источника истины сходятся в одном месте:** ответ шлюза, вебхук (`POST /webhooks/payments/gateway`, HMAC `X-Signature: t=<unix>,v1=<hex>`, окно replay, дедупликация по `eventId`, сверка суммы) и reconciler (опрос шлюза с арендой `SKIP LOCKED`, backoff, истечение `PENDING_EXPIRY`).
- **Исход в orders** — событие `orders.payments.status` через outbox; orders принимает заказ (`pending_payment → accepted`) и повторно публикует `orders.state` с `revision+1`.
- **Оплата на кассе (S6).** `POST /orders/{id}/payments/pos` (`Idempotency-Key`, роли STAFF/FRANCHISE_OWNER/HQ_STAFF: гость 401, не персонал 403, чужой ресторан 404) — тот же автомат и те же правила, что у онлайн-оплаты, но шлюз — кассовая система (`POS_URL`). `tender` (`card|cash`; наличные тоже проводятся через терминал) и фискальные данные (`fiscalData`: из вебхука `/webhooks/payments/pos` или из опроса кассы) хранятся в платеже. Платёж читают сотрудник ресторана заказа, владелец и (для гостевого заказа) знающий id.
- **Возвраты (S6).** Возврат — отдельный процесс поверх `paid` (статус платежа не меняется): `refund_status` `pending → refunded | failed`. Запрос создаёт цикл `ProcessRefunds` (свип: оплаченные платежи отменённых заказов — покрывает и «платёж пришёл после отмены», и порядок событий Kafka), вызов шлюза `POST /v1/payments/{id}/refund` идемпотентен по ключу = `paymentId`. Сбои и «в обработке» повторяются с backoff, определённый отказ (4xx) → `failed` + метрика `payment_refunds_failed` (ручная обработка). Статус публикуется в `orders.payments.status` (`refundStatus`, `refundedAt`). Отмена заказа с неоплаченной операцией — reconciler отменяет её у шлюза (`declineReason=order_cancelled`), возврата нет.
- **Деньги — только int64 копейки** (ADR-13).

## Конфигурация (env)
`DATABASE_URL`, `KAFKA_BROKERS`, `GATEWAY_URL`, `GATEWAY_API_KEY`, `GATEWAY_TIMEOUT` (5s), `GATEWAY_WEBHOOK_SECRET`, `POS_URL` (кассовая система, dev: mock :9093), `POS_API_KEY`, `POS_WEBHOOK_SECRET`, `WEBHOOK_WAIT` (10s), `PENDING_EXPIRY` (10m), `RECONCILE_BACKOFF_MIN/MAX` (5s/5m), `RECONCILE_INTERVAL` (2s), `DEFAULT_RETURN_URL`.
Секреты вне `APP_ENV=dev` обязательны, не короче 16 символов и не равны dev-значениям; в k8s — из Secret.

## Mock шлюза (`tools/mock-externals`, модуль `payment`, :9091)
Протокол: `POST /v1/payments` (Idempotency-Key), `GET /v1/payments/{id}`, `POST /v1/payments/{id}/cancel`, `POST /v1/payments/{id}/refund` (полная сумма, Idempotency-Key; `_admin/gateway/mode?refund=ok|error|reject|timeout`). Исход по умолчанию — `paid` через `MOCK_DELAY` (вебхук с 4 попытками и подписью).
Магические суммы: `amount % 100 == 13` — отказ, `== 99` — «тишина» (вебхука нет, итог виден только опросом). Управление:
```bash
curl -XPOST 'localhost:9091/_admin/gateway/next?outcome=declined'
curl -XPOST 'localhost:9091/_admin/gateway/mode?init=timeout&status=error&webhooks=off'
curl -XPOST 'localhost:9091/_admin/gateway/redeliver?id=gw_xxx'
curl -XPOST 'localhost:9091/_admin/gateway/reset'
curl localhost:9091/_inspect/payments
```

## Mock кассы (модуль `pos`, :9093)
Тот же протокол и код, что у шлюза (профиль «касса»): `create` требует `tender`, оплаченная операция несёт `fiscalData` (номер чека, ФН, ФД, ФПД, сумма) в статусе и в вебхуке; ключ `POS_API_KEY`, вебхук → `/webhooks/payments/pos`. Админка — `/_admin/pos/{mode,next,reset,redeliver}`, инспекция — `GET /_inspect/pos`. Магические суммы те же.

## Не входит
`courier_card` и ссылка на оплату — S7; смена способа оплаты у существующего заказа; частичные возвраты (возвращается вся сумма платежа).
