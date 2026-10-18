# S0. Фундамент — что сделано

## Результат относительно DoD плана
| Пункт DoD | Статус |
|---|---|
| `make up` поднимает Postgres+Kafka(+Redis по профилю), заведены топики | Конфиг написан и статически проверен; **запуск на Docker Desktop — первая проверка у вас** (в среде сборки не было Docker) |
| `make new-service` даёт собираемый «пустой» сервис со всеми хуками | ✅ проверено: `make check-template` (build, vet, test, запуск против реального Postgres, newman) |
| Пайплайн зелёный | CI описан (`.github/workflows/ci.yml`); локально прогнаны go vet/test -race, lint контрактов, валидация values и рендер чарта |

## Что в репозитории
- **Монорепо** (ADR-1): один `go.mod` в корне — `pkg/` общий без `replace`. История auth-service перенесена с зачисткой секретов.
- **Контракты**: OpenAPI нарезан на 8 файлов + `_common.yaml`; AsyncAPI: `OrderState.revision` (ADR-10), конверт события, биндинги топиков (партиции, ключ, DLQ, compaction).
- **pkg/**: `config`, `logging` (маскирование), `httpx` (problem+json, middleware, health, сервер, клиент retry+breaker), `metrics` (RED по шаблону маршрута), `pgxutil` (пул, мигратор с checksum, `--migrate-only`), `kafka` (outbox+relay, inbox, consumer+DLQ, franz-go), `idempotency`, `authclient` (dev-режим), `webhook` (HMAC).
- **tools/**: `mock-externals` (каркас + `ping`), `service-template` + `new-service`, `gen-topics`, `validate-values`.
- **deploy/**: compose (инфра + срезы), единый Dockerfile (distroless nonroot), Helm-чарт `service` + `values.schema.json`, `values/auth.yaml`, smoke-манифест Postgres для k8s.
- **CI**: gofmt, vet, staticcheck, race-тесты с Postgres, проверка шаблона, lint контрактов, `helm lint/template`, `docker compose config`, сборка образов, gitleaks.

## Решения, принятые в S0 (кроме ADR-9…12)
| Решение | Почему | Альтернатива и цена |
|---|---|---|
| Kafka-клиент **franz-go** (чистый Go, без cgo) | образы `static`/distroless, кросс-сборка на M3; есть `kfake` для тестов без брокера | `kafka-go` проще, но нет in-memory брокера и слабее управление коммитами |
| Метрики на **prometheus/client_golang** | стандарт де-факто; гистограммы для квантилей ДЗ-5 | собственный экспорт — меньше зависимостей, но поддержка на нас |
| Заголовочная подпись вебхуков `X-Signature: t=…,v1=…` (`pkg/webhook`) | единая схема для шлюза/POS/курьерки + окно против replay | поле `signature` в теле (как в AsyncAPI шлюза) — S3 может проверять и его |
| Миграции по **модулям** (`service`, `kafka`, `idempotency`) в одной таблице `schema_migrations(module, version, checksum)` | outbox/inbox/ключи не копируются в каждый сервис; дрейф схемы ловится | у auth пока своя таблица `schema_migrations(version)` — выравнивается в S5 |
| `httpx.TrackRoute` | `ServeMux` пишет `r.Pattern` в копию запроса; без этого метки `route` в метриках/логах пустели бы после middleware с `WithContext` | — |
| Один Dockerfile на все сервисы (`SRC`, `TARGET`) | одинаковые гарантии безопасности, кэш `go mod download` | отдельные Dockerfile — гибче, но дрейфуют |

## Известные ограничения / открытые вопросы
1. ~~**Деньги в контракте.**~~ Закрыто contract-PR (ADR-13): `Money.amount` — целое в копейках.
2. **Ключ JWT из `auth_go` скомпрометирован** (лежал в git и в дампе документации). В новом репозитории его нет, но в самом `auth_go` на GitHub он остаётся: репозиторий нужно удалить/сделать приватным и нигде не использовать ключ.
3. **auth-service** пока не использует `pkg/*` (S5): нет `/health/`, `/metrics`, `--migrate-only`, роли старые. Helm-values auth учитывают это (`migrations.enabled: false`, пробы на `/actuator/health`).
4. Не проверено в среде сборки (нет Docker/Helm/kubectl): `docker compose up`, `helm lint` на реальном helm, k8s-smoke. Чарт прошёл рендер приблизительным движком + схему values; Kafka-путь проверен на `kfake`, но не на настоящем брокере.
5. Отправка в GitHub не выполнялась (правило проекта): доставка — git bundle.
