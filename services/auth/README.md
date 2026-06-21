# restaurant-auth-service (Go)

Поведенчески совместимый перенос Java/Spring Boot сервиса аутентификации на Go и PostgreSQL.

## Возможности

- регистрация и BCrypt-хэширование паролей;
- login с RSA/RS256 access JWT и opaque refresh-token;
- атомарная ротация и отзыв refresh-token через `SELECT FOR UPDATE`;
- `/auth/me`, introspection, публичный JWKS;
- встроенные версионированные PostgreSQL-миграции и dev seed;
- health check и graceful shutdown.

## Быстрый запуск

Нужны Docker, Docker Compose и OpenSSL:

```bash
make compose-up
```

Сервис будет доступен на `http://localhost:8081`, PostgreSQL — на `localhost:5432`. При первом запуске `make` создаст локальную RSA-пару в `config/keys`; ключи исключены из Git. Если порты заняты: `POSTGRES_PORT=55432 AUTH_PORT=18081 make compose-up`.

Проверка:

```bash
curl -s http://localhost:8081/actuator/health
curl -s -X POST http://localhost:8081/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"phone":"+79990000000","password":"123456"}'
```

Dev-пользователи сохранены из Java-версии: customer `+79990000000 / 123456`, admin `+79991111111 / admin123`, blocked user `+79992222222 / blocked123`.

## Локальная разработка

Скопируйте `.env.example` в `.env` и экспортируйте переменные либо используйте значения по умолчанию. Затем:

```bash
make keys
docker compose up -d auth-postgres
go test ./...
go run ./cmd/auth-service
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
