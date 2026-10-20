# restaurant-auth-service (Go)

Поведенчески совместимый перенос Java/Spring Boot сервиса аутентификации на Go и PostgreSQL.

## Возможности

- регистрация и BCrypt-хэширование паролей;
- login с RSA/RS256 access JWT и opaque refresh-token;
- атомарная ротация и отзыв refresh-token через `SELECT FOR UPDATE`;
- `/auth/me`, `introspect` и `authorize` (внутренние, наружу через gateway не публикуются), публичный JWKS;
- роли `CUSTOMER/STAFF/FRANCHISE_OWNER/HQ_STAFF`, привязка сотрудников к ресторанам (`restaurants[]` в токене);
- ротация ключа без простоя: прежние публичные ключи остаются в JWKS (`JWT_PREVIOUS_PUBLIC_KEYS`);
- ошибки в формате Problem (RFC 9457, `application/problem+json`, поле `code`);
- версионированные миграции (`--migrate-only` для Job в k8s; переход со старой таблицы `schema_migrations` — `pgxutil.AdoptLegacy`);
- `/health/`, `/actuator/health`, `/metrics`, graceful shutdown.

## Быстрый запуск (монорепо food-platform)

Нужны Docker, Docker Compose, OpenSSL, Go 1.24+. Из **корня репозитория**:

```bash
make keys SERVICE=auth          # локальная RSA-пара JWT → services/auth/config/keys (в git не попадает)
make up SLICE="auth"            # Postgres + Kafka + auth-service в контейнере → http://localhost:8081
```

Проверка:

```bash
curl -s http://localhost:8081/actuator/health
curl -s -X POST http://localhost:8081/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"phone":"+79990000000","password":"123456"}'
```

Dev-пользователи (`SEED_ON_START=true`, при `APP_ENV=prod` запрещено; в prod учётки наследия удаляются при старте):
customer `+79990000000 / 123456`, HQ_STAFF `+79991111111 / admin123`, blocked `+79992222222 / blocked123`,
STAFF ресторана №4 `+79993333333 / staff123`, STAFF ресторана №1 `+79994444444 / staff2123`, FRANCHISE_OWNER ресторана №4 `+79995555555 / owner123`
(пароли — публичные dev-константы).

> **Секреты.** Приватный ключ JWT и `.env` никогда не коммитятся (`.gitignore`). Ключ, который лежал в старом репозитории `auth_go`, считается скомпрометированным — не используйте его нигде.

## Локальная разработка (go run)

```bash
make up                         # инфраструктура
make keys SERVICE=auth
cp services/auth/.env.example services/auth/.env   # .env игнорируется git
set -a; . services/auth/.env; set +a
go run ./services/auth/cmd/auth-service
go test ./services/auth/...
```

## API

| Метод | Путь | Доступ |
|---|---|---|
| POST | `/auth/register` | public |
| POST | `/auth/login` | public |
| POST | `/auth/refresh` | public |
| POST | `/auth/logout` | public |
| GET | `/auth/me` | Bearer JWT |
| POST | `/auth/introspect` | внутренний (в gateway не опубликован) |
| POST | `/auth/authorize` | Bearer JWT субъекта, внутренний |
| GET | `/auth/.well-known/jwks.json` | public |
| GET | `/actuator/health` | public |

Миграции применяются при старте под advisory lock (в k8s — Job `--migrate-only`); каждая выполняется транзакционно и отмечается в `schema_migrations(module, version, checksum)`.
Конфигурация: `JWT_PRIVATE_KEY_PATH`, `JWT_PUBLIC_KEY_PATH`, `JWT_KEY_ID`, `JWT_ISSUER`, `JWT_AUDIENCE`, `JWT_ACCESS_TTL`, `REFRESH_TOKEN_TTL`, `BCRYPT_COST` (10–15), `SEED_ON_START`, `JWT_PREVIOUS_PUBLIC_KEYS=kid=path,…`.
