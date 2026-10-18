# menu-service

Меню на ресторан и актуальные цены (UC-ORD-01/02/03). Синхронный контракт для orders-service:
`validateMenuItems`. Read-only: меню загружаются seed'ом при старте, CRUD-API нет. Kafka не используется.

| | |
|---|---|
| Порт | `8083` |
| БД | `menu` (`postgres://menu:menu@localhost:5432/menu`) |
| Контракт | `contracts/openapi/menu.yaml` (1.2.0) |
| Probes | `GET /health/` (liveness), `GET /actuator/health` (readiness с пингом БД), `GET /metrics` |
| Env | `DATABASE_URL` (обяз.), `HTTP_PORT`, `APP_ENV`, `LOG_LEVEL`, `AUTH_MODE`, `SEED_ON_START` (`true` в dev-срезе) |

## Запуск
```bash
make up SLICE="restaurant menu"
make newman SERVICE=menu
make newman SERVICE=catalog
```
Быстрый цикл:
```bash
make up
DATABASE_URL='postgres://menu:menu@localhost:5432/menu?sslmode=disable' SEED_ON_START=true HTTP_PORT=8083 \
  go run ./services/menu/cmd/menu-service
```

## Как устроено
- `internal/domain` — модель и чистая `Validate`: цена за единицу = позиция + найденные модификаторы, строка = цена × количество,
  итог суммирует только доступные строки. Причины (приоритет): `ITEM_NOT_FOUND` > `ITEM_UNAVAILABLE` > `MODIFIER_NOT_FOUND` > `MODIFIER_UNAVAILABLE`.
  Недоступность — не ошибка запроса: `200`, `valid=false`.
- `internal/store` — **одна выборка на запрос** (`getMenu`: menus ⟕ categories ⟕ items ⟕ modifiers; `validateMenuItems`:
  menus ⟕ items(`id = ANY`) ⟕ modifiers) — один снимок данных, одно обращение к БД. Изоляция по ресторану в условии JOIN:
  позиция чужого ресторана — `ITEM_NOT_FOUND`.
- Деньги: `bigint` копейки с `CHECK (0 ≤ price ≤ 10^10)`, `int64` в Go, `integer` в JSON (ADR-13). Сумма ≤ 100 строк × 99 шт. × ≤ 21 × 10^10 влезает в int64 с запасом.
- Версия меню (`menus.version`) растёт, когда меняется содержимое (seed сравнивает sha256-отпечаток), в том числе стоп-лист.
  На ней строится `ETag` (`"v<N>"`); `If-None-Match` → `304`; `Cache-Control: public, max-age=30`.
  Авторитетная проверка перед заказом — всегда `validateMenuItems`, кэш `getMenu` её не заменяет.
- `internal/seed` — 5 меню из двух шаблонов (`classic`, `kiosk`); ID — UUIDv5 от `restaurantId` и ключа, стабильны между запусками.
  Стоп-лист в seed: ресторан 1 — «Брауни»; ресторан 2 — «Чикен бургер» и модификатор «Сыр чеддер». У ресторана 6 меню нет (404).

## Решение: меню на ресторан
Сетевой шаблон с переопределениями сложнее (наследование, конфликты версий) и не нужен для критичного пути.
Seed уже строит меню ресторана из шаблона — позже это можно превратить в настоящий механизм, не меняя контракт.

## Тесты
`make test` (домен, seed, HTTP) · `make test-int` (store против Postgres: порядок категорий, изоляция по ресторану, версия меню, CHECK на цену).
