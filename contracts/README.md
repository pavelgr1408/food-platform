# Контракты (contract-first)

Точка синхронизации backend ↔ frontend ↔ моки. Контракт правится **отдельным PR до кода**.

| Файл | Что |
|---|---|
| `openapi/<service>.yaml` | REST-контракт сервиса (auth, restaurant, menu, loyalty, orders, payment, delivery, notification) |
| `openapi/_common.yaml` | общие схемы (`Money`, `Problem`, `GeoPoint`…), параметры, ответы |
| `asyncapi/kafka-events.yaml` | 8 Kafka-топиков + вебхуки + push (AsyncAPI 3.0) |

Источник: ДЗ-1 (`architecture-homeworks/homeworks/dz-1/openapi|asyncapi`), нарезано в S0.
Исправлено при нарезке: невалидные flow-mapping с запятой в `description` (`TokenPair.expiresIn`, `MenuValidationResult.reason`).

## Изменения относительно dz-1
- **ADR-10:** `OrderState.revision` (обязательное), `paymentStatus` обязателен; `orders.state` публикуется минимум дважды.
- Заголовки конверта события (`event-id`, `event-type`, `occurred-at`, `traceparent`) как `messageTraits.EventEnvelope`.
- Kafka-биндинги: партиции, ключ партиции (`x-partition-key`), DLQ (`x-dlq`), compaction для `promotions.discount`.
- Блок `x-kafka-topics` из OpenAPI удалён (дублировал AsyncAPI).
- **auth.yaml 1.2.0** приведён к коду `services/auth`: `register`, `login` (по `phone`, не `login`), `refresh`, `logout`, `me`, `introspect`, JWKS; `verifyToken` (`/auth/token/verify`) удалён (ADR-11: JWKS + `introspect`); роли — `CUSTOMER/STAFF/FRANCHISE_OWNER/HQ_STAFF` (как в `pkg/authclient`), `subjectId` → `userId`. `authorize` оставлен как `x-status: planned` (S5). Формат ошибок — текущий `AuthError`, переход на Problem Details в S5. Проверено: 17 живых ответов сервиса валидны по схемам.
- **ADR-13 (деньги):** `Money.amount` — `integer/int64`, минорные единицы (копейки), было `number/decimal` (549.00 → 54990). `PromotionDiscount.discount.value` — `integer`: для `percent` сотые доли процента (1500 = 15%), для `fixed` копейки.
- **restaurant.yaml 1.2.0 / menu.yaml 1.2.0 (S1, каталог):** семантика поиска (центр по `lat/lon` или справочнику городов, сортировка по расстоянию, `deliveryAvailable` с учётом радиуса доставки, `openNow` по таймзоне ресторана, ночное закрытие, исключения по датам), `hint` при пустом результате (`pickup|expand_radius|change_address`), `city/timezone/deliveryEnabled/deliveryRadiusMeters/scheduleExceptions` в `Restaurant`; в меню — `menuVersion`, `ETag`/`304`, `MenuCategory.id`, `Modifier.available`, в `validateMenuItems` — `reasonCode`, `lineTotal`, `total`, `menuVersion`, лимиты (1…100 строк, количество 1…99); недоступность позиции — `200 valid=false`, не ошибка. Каталожные операции публичны (`security: [{}, bearerAuth]`).
- **orders.yaml 1.2.0 / menu.yaml 1.3.0 (S2, ядро заказа):** `x-status` на операциях orders (`searchOrder`, `markOrderIssued` — `planned`, S6); статусы заказа выровнены с `orders.status` (+ `pending_payment`, ADR-10); `createOrder` — `Idempotency-Key` обязателен, `402` удалён (заказ создаётся до оплаты), добавлены `409 PRICE_CHANGED|ITEMS_UNAVAILABLE|RESTAURANT_CLOSED|CART_CLOSED`, `503`; `409` со списком `unavailableItems`; `Order`: `paymentMethod`, `pickupSlot`, `subtotal/discountTotal`, формат номера; `Cart.status/orderId`; лимиты (50 строк, 20 модификаторов); сопоставление способов оплаты/каналов с событиями. В `MenuValidationLine` добавлено `name` (снапшот названия). AsyncAPI: `orders.status` + `pending_payment`.
- **payment.yaml 1.2.0 / orders.yaml 1.3.0 / kafka-events.yaml (S3, деньги):** `x-status` на операциях payment (`initiatePosPayment` — `planned`, S6; `courier_card` — S7); платёжный автомат `pending→paid|declined|unknown`, блокировка новой попытки при `unknown`, один «живой» и один успешный платёж на заказ; `Idempotency-Key` обязателен для `initiatePayment`; ответы `202` (unknown), `402` (синхронный отказ шлюза), `409 PAYMENT_IN_PROGRESS|PAYMENT_STATUS_UNKNOWN|ALREADY_PAID|ORDER_NOT_PAYABLE`, `422 PAYMENT_METHOD_NOT_ALLOWED|NOT_SUPPORTED`; `Retry-After` на 404 (событие `orders.state` ещё не дошло); вебхуки: схема безопасности `X-Signature`, `eventId` (дедупликация), `amount` (сверка), `x-path`; `Payment`: `attempt`, `declineReason`, `paidAt`, `updatedAt`; AsyncAPI `PaymentStatus`: `attempt`, `reason`, описание реакции orders; orders: описание потребления `orders.payments.status`, `paymentStatus=paid` при `cancelled` = нужен возврат.
- **orders.yaml 1.4.0 / kafka-events.yaml 1.1.0 (S4, производство и ЖЦ заказа):** семантика потребления `orders.dish.status` (снимок всех блюд; вывод статуса заказа: все `queued` → `accepted`, любое `cooking` → `cooking`, все `ready` → `ready`; защита по `updatedAt`, без отката, события по `pending_payment`/`cancelled` игнорируются) и `production.lead.time` (последнее значение на ресторан, срок годности → `approximate`); `OrderStatus.etaReadyAt` для `accepted`/`cooking`; `getOrderEta`: `readyAt` = принятие + лид-тайм, `approximate=false` при свежей оценке. Новых полей и топиков нет.

## Порты (dev)
gateway 8080 · auth 8081 · restaurant 8082 · menu 8083 · orders 8084 · payment 8085 · loyalty 8086 · delivery 8087 · notification 8088 · mock-externals 9090+

## Проверка
`make lint-contracts` (Redocly для OpenAPI, AsyncAPI parser для Kafka).
