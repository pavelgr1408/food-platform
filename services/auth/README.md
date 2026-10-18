# restaurant-auth-service (Go)

Поведенчески совместимый перенос Java/Spring Boot сервиса аутентификации на Go и PostgreSQL.

## Возможности

- регистрация и BCrypt-хэширование паролей;
- login с RSA/RS256 access JWT и opaque refresh-token;
- атомарная ротация и отзыв refresh-token через `SELECT FOR UPDATE`;
- `/auth/me`, introspection, публичный JWKS;
- встроенные версионированные PostgreSQL-миграции и dev seed;
- health check и graceful shutdown.

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

Dev-пользователи сохранены из Java-версии: customer `+79990000000 / 123456`, admin `+79991111111 / admin123`, blocked user `+79992222222 / blocked123`.

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
| POST | `/auth/introspect` | public |
| GET | `/auth/.well-known/jwks.json` | public |
| GET | `/actuator/health` | public |

Миграции применяются самим приложением при старте под PostgreSQL advisory lock. Каждая миграция выполняется транзакционно и отмечается в `schema_migrations`.
