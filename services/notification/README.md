# notification-service (S9)

Push-уведомления о статусе заказа. Читает `orders.status`, раскладывает уведомление по устройствам владельца заказа и
отправляет через APNs / FCM / HMS (в проекте — моки, ADR-12). Хранит push-токены устройств.

| | |
|---|---|
| Порт | `8088` |
| БД | `notification` (`postgres://notification:notification@localhost:5432/notification`) |
| Kafka | читает `orders.status` (группа `notification-service`, DLQ `orders.status.dlq`); сам ничего не публикует |
| Контракт | `contracts/openapi/notification.yaml`, `contracts/asyncapi/kafka-events.yaml` (`OrderStatus.userId`) |
| Probes | `GET /health/`, `GET /actuator/health`, `GET /metrics` |

## API (через gateway, любая роль, гость — 401)
`POST /notifications/device-tokens` (201 новый / 200 повтор или смена владельца), `GET /notifications/device-tokens`,
`DELETE /notifications/device-tokens/{tokenId}` (чужой токен — 404). Язык уведомлений берётся из `Accept-Language` (`ru`, `en`).

## Как устроено
- **Кому.** Только зарегистрированному владельцу заказа (`userId` в событии). Гость и заказ по факсу — push не получают (осознанное ограничение); устройство с `permissionsGranted=false` — тоже.
- **Какие статусы.** `accepted`, `ready`, `on_delivery`, `delivered`, `handed_over`, `cancelled`, `not_received`; `pending_payment`, `cooking`, `closed` уведомлений не порождают (`domain.TypeFor`).
- **Транзакционный inbox.** Консьюмер в одной транзакции пишет строки очереди `notifications` (по одной на устройство) и запись inbox. `UNIQUE(order_id, type, token_id)` — дубль события не даёт второго push.
- **Отправка вне транзакции.** `RunSender` забирает строки с арендой (`FOR UPDATE SKIP LOCKED`, `SEND_LEASE`), шлёт параллельно (`SEND_WORKERS`), результат пишет отдельной короткой транзакцией. Временный сбой → повтор с нарастающей паузой (`SEND_BACKOFF`…`SEND_BACKOFF_MAX`), после `SEND_MAX_ATTEMPTS` → `failed`.
- **Изоляция.** Сбой провайдера не влияет на заказ: отдельная consumer-группа, orders об уведомлениях не знает. Ядовитое сообщение уходит в `orders.status.dlq`.
- **Устаревшие события.** Статусы старше `MAX_EVENT_AGE` (1 ч) не отправляются: новая группа читает топик с начала, и «заказ готов» двухдневной давности не нужен.
- **Недействительный токен.** Провайдер ответил Unregistered/NotRegistered — токен удаляется, остальные устройства получают push как обычно.
- **Токены.** Пара `(platform, token)` уникальна; токен, переданный другому пользователю (смена владельца устройства), переходит к нему, неотправленные уведомления прежнему владельцу отменяются. Не больше 10 токенов на пользователя — старейший вытесняется. Токены в журналы и метрики не попадают.

## Конфигурация
`DATABASE_URL`, `KAFKA_BROKERS`, `AUTH_MODE`/`AUTH_JWKS_URL`, `PUSH_APNS_URL|KEY|TOPIC`, `PUSH_FCM_URL|KEY|PROJECT`, `PUSH_HMS_URL|KEY|APP_ID`
(ключи вне dev обязательны; в dev — публичные `dev-push-*-key`), `PUSH_TIMEOUT`, `MAX_EVENT_AGE`, `SEND_INTERVAL`, `SEND_BACKOFF`, `SEND_BACKOFF_MAX`,
`SEND_MAX_ATTEMPTS`, `SEND_BATCH`, `SEND_WORKERS`, `SEND_TIMEOUT`, `SEND_LEASE` (> `SEND_TIMEOUT`), `RETENTION`.

## Запуск
```bash
make up SLICE="restaurant menu orders notification mock-externals"
make newman SERVICE=notification
make newman SERVICE=e2e              # папка 13: заказ → push (нужны Kafka и mock-externals с модулем push)
make notification-e2e                # то же без Docker: реальные бинарники + in-memory Kafka
```
Тесты: `TEST_DATABASE_URL=… go test -race ./services/notification/... ./tools/mock-externals/...` (без `TEST_DATABASE_URL` тесты с БД пропускаются).
Мок провайдеров: `tools/mock-externals/mock/push.go`, магические токены `invalid` / `reject` / `flaky`, просмотр принятых — `GET :9095/_inspect/pushes`.
