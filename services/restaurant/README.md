# restaurant-service

Каталог ресторанов сети (UC-FND-01): поиск по геопозиции/адресу, карточка ресторана, режим работы.
Read-only: данные загружаются seed'ом при старте, CRUD-API нет. Kafka не используется.

| | |
|---|---|
| Порт | `8082` |
| БД | `restaurant` (`postgres://restaurant:restaurant@localhost:5432/restaurant`) |
| Контракт | `contracts/openapi/restaurant.yaml` (1.2.0) |
| Probes | `GET /health/` (liveness), `GET /actuator/health` (readiness с пингом БД), `GET /metrics` |
| Env | `DATABASE_URL` (обяз.), `HTTP_PORT`, `APP_ENV`, `LOG_LEVEL`, `AUTH_MODE`, `SEED_ON_START` (`true` в dev-срезе) |

## Запуск
```bash
make up SLICE="restaurant"
make newman SERVICE=restaurant
```
Быстрый цикл:
```bash
make up
DATABASE_URL='postgres://restaurant:restaurant@localhost:5432/restaurant?sslmode=disable' SEED_ON_START=true \
  go run ./services/restaurant/cmd/restaurant-service
```

## Как устроено
- `internal/domain` — модель, **расписание** (`Schedule.IsOpen`: часовой пояс ресторана, перерывы, ночные интервалы
  `closesAt <= opensAt`, `24:00`, исключения по датам заменяют режим дня и «хвост» вчерашнего ночного интервала), гаверсинус и bounding-box (антимеридиан, полюса).
- `internal/geocode` — мок-справочник «город → центроид» (A1). Бэкенд к картам не обращается (Д2).
- `internal/store` — pgx; поиск: bounding-box по индексу `(lat, lon)` → гаверсинус в SQL → сортировка по расстоянию (кандидатов ≤ 2000).
- `internal/service` — фильтры `deliveryOnly`/`openNow`, `deliveryAvailable` = доставка включена **и** центр в радиусе доставки, пагинация, `hint` при пустом результате.
- `internal/seed` — 6 демо-ресторанов (Москва ×3 + 1 неактивный, Санкт-Петербург 24/7, Казань); upsert идемпотентен, режим работы заменяется целиком.
- Время работы хранится минутами от полуночи (`smallint`), `time/tzdata` вшит в бинарь (distroless без zoneinfo).

## Трейд-офф: без PostGIS
Рестораны — десятки/сотни/тысячи, а не миллионы; узкое место — число запросов, а не размер справочника. Bounding-box по
B-tree-индексу + гаверсинус на ≤ 2000 строк укладывается в единицы миллисекунд. PostGIS добавил бы 300+ МБ к образу и
эксплуатационную сложность. **Граница замены:** десятки тысяч точек или нужен полигон зоны доставки → PostGIS + GiST.

## Тесты
`make test` (быстрые) · `make test-int` (store против Postgres: SQL-расстояние сверяется с гаверсинусом в Go, антимеридиан, upsert).
