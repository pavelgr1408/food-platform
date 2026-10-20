# gateway-service (api-gateway, S5)

Единая публичная точка входа. Проверяет JWT **локально** по JWKS auth-service (ADR-11), применяет лимиты,
CORS и ограничение тела, проксирует запросы по таблице маршрутов. Своих бизнес-операций, БД и Kafka нет.

| | |
|---|---|
| Порты | `8080` — клиентский трафик и пробы · `9100` (`METRICS_ADDR`) — только `/metrics`, наружу не публикуется |
| Контракт | `contracts/openapi/gateway.yaml` (политика периметра, `x-routes`) + контракты сервисов |
| Таблица маршрутов | `internal/routes/routes.json` (в образе) ↔ `x-routes` в `gateway.yaml`; расхождение ловит `make check-routes` |
| Probes | `GET /health/` (живость), `GET /actuator/health` (готовность: загружены ключи JWKS; недоступность сервисов её не снимает) |

## Как принимается решение
`маршрут → лимит IP (класс auth) → личность по JWT → лимит default (userId либо IP) → 401/403 → [sensitive: introspect у auth] → прокси`.

* `public` — токен не смотрим; `optional` — нет токена = гость, токен есть, но плохой = 401; `required` (+`roles`) — иначе 401/403.
* Подпись RS256 по JWKS (`kid`), `iss`, `aud`, `exp`; `alg=none` и HS256 отвергаются. Auth на каждый запрос не вызывается.
* `sensitive` (создание заказа, оплата) — при наличии токена ещё `POST /auth/introspect` (кэш `INTROSPECT_CACHE_TTL`, до `exp` токена).
  Auth недоступен → `503 AUTH_UNAVAILABLE` (fail-closed); чтение и гостевые запросы не затрагиваются.
* `X-User-*`/`X-Restaurant-Ids` снаружи вырезаются; сервисы за gateway работают в `AUTH_MODE=jwks` и сами проверяют тот же JWT.
* Не в таблице → `404 ROUTE_NOT_FOUND` (introspect/authorize, `/metrics`, `/actuator/*` сервисов, вебхуки).
* Вебхуки `/webhooks/payments/*` идут **мимо** gateway (ingress → payment, HMAC + лимиты ingress), чтобы отказ периметра не терял уведомления шлюза.

## Конфигурация (env)
| Переменная | По умолчанию | Назначение |
|---|---|---|
| `HTTP_PORT` / `METRICS_ADDR` | `8080` / `:9100` | клиентский порт / порт метрик |
| `UPSTREAM_{AUTH,RESTAURANT,MENU,ORDERS,PAYMENT}_URL` | `http://localhost:808{1..5}` | адреса сервисов (без пути) |
| `AUTH_JWKS_URL`, `AUTH_ISSUER`, `AUTH_AUDIENCE` | JWKS из `UPSTREAM_AUTH_URL`, `restaurant-auth-service`, `restaurant-api` | проверка JWT |
| `CORS_ALLOWED_ORIGINS` | пусто | точные origin через запятую; `*` запрещено |
| `TRUSTED_PROXY_CIDRS` | пусто | чьим `X-Forwarded-For/Proto` верим (CIDR ingress); `/0` запрещён в prod |
| `RATE_LIMIT_RPS` / `RATE_LIMIT_BURST` | `50` / `100` | класс default, на реплику |
| `AUTH_RATE_PER_MIN` / `AUTH_RATE_BURST` | `10` / `10` | класс auth (register/login/refresh), по IP, на реплику |
| `MAX_BODY_BYTES` | `1048576` | `413 PAYLOAD_TOO_LARGE` |
| `UPSTREAM_TIMEOUT` | `10s` | `504 UPSTREAM_TIMEOUT` (недоступен — `502`) |
| `INTROSPECT_CACHE_TTL` / `INTROSPECT_TIMEOUT` | `10s` / `1s` | онлайн-проверка sensitive-маршрутов |

Лимит считается **на реплику** (при N репликах эффективный предел до N раз выше): общий лимит — Redis, S11.

## Запуск
```bash
AUTH_MODE=jwks make up SLICE="auth restaurant menu orders payment mock-externals gateway"
make newman SERVICE=gateway          # http://localhost:8080
make perimeter-check                 # то же без Docker: бинарники + Postgres, newman, contract-check, chaos
```
Метрики: `gateway_rate_limited_total{class}`, `gateway_auth_rejected_total{reason}`, `gateway_upstream_errors_total{upstream,kind}`,
`gateway_introspections_total{result}` и RED-метрики `http_*` (маршрут — шаблон из таблицы).

## Как добавить маршрут
1. Операция в контракте сервиса (`contracts/openapi/<svc>.yaml`) — отдельным PR.
2. Строка в `x-routes` (`gateway.yaml`) и в `internal/routes/routes.json` — одним PR; `make check-routes` проверяет согласованность.
3. Новая операция персонала/внутренняя — сначала `required` + роль или вне таблицы; публичной её делает только осознанное решение.
