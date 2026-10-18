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

## Порты (dev)
gateway 8080 · auth 8081 · restaurant 8082 · menu 8083 · orders 8084 · payment 8085 · loyalty 8086 · delivery 8087 · notification 8088 · mock-externals 9090+

## Проверка
`make lint-contracts` (Redocly для OpenAPI, AsyncAPI parser для Kafka).
