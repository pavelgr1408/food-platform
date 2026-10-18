# Сервис аутентификации `restaurant-auth-service`
### Документация системного аналитика

**Репозиторий:** `https://github.com/pavelgr1408/auth_go` (ветка `master`)
**Технологический стек:** Go 1.24, PostgreSQL 18, JWT (RS256), pgx v5, bcrypt
**Назначение:** централизованный сервис аутентификации и выдачи токенов для платформы оформления заказов в онлайн-ресторане.

---

## 1. Назначение и границы системы

`restaurant-auth-service` — это отдельный микросервис, отвечающий за **аутентификацию пользователей** и **управление жизненным циклом токенов доступа** в экосистеме онлайн-ресторана (клиентские приложения оформления заказов, панель администратора, приложения курьеров и кухни).

Сервис решает следующие задачи:

- регистрация пользователей по номеру телефона и паролю;
- вход в систему (login) с выдачей пары токенов: access (JWT) + refresh (opaque);
- безопасная **ротация** и **отзыв** refresh-токенов;
- предоставление данных о текущем пользователе (`/auth/me`);
- интроспекция (проверка валидности) access-токена для других сервисов;
- публикация публичного ключа в формате **JWKS** для офлайн-проверки подписи токенов сторонними сервисами (resource-серверами).

### Что НЕ входит в зону ответственности сервиса

- управление профилем пользователя (имя, адреса, история заказов);
- бизнес-логика заказов, оплаты, меню;
- отправка SMS/OTP, восстановление пароля (в текущей версии отсутствуют);
- авторизация на уровне конкретных бизнес-операций — сервис только **аутентифицирует** и выдаёт роли в токене, а решения о доступе принимают потребляющие сервисы.

---

## 2. Глоссарий

| Термин | Определение |
|--------|-------------|
| **Access-токен** | Короткоживущий JWT (RS256), подписанный приватным ключом сервиса. Содержит идентификатор пользователя, телефон и роли. Срок жизни по умолчанию — 15 минут. |
| **Refresh-токен** | Непрозрачный (opaque) случайный токен (64 байта), выдаётся при login. В БД хранится только его SHA‑256‑хэш. Срок жизни по умолчанию — 720 часов (30 дней). |
| **Ротация refresh-токена** | При каждом обновлении старый refresh-токен отзывается, выдаётся новый. Защищает от повторного использования украденного токена. |
| **JWKS** | JSON Web Key Set — публичный набор ключей (`/.well-known/jwks.json`), по которому сторонние сервисы проверяют подпись access-токена без обращения к auth-сервису. |
| **Introspection** | Проверка валидности и активности токена через эндпоинт `/auth/introspect`. |
| **Resource-сервер** | Любой потребляющий сервис платформы (API заказов и т. п.), который проверяет access-токены. |
| **Роль** | Одна из: `CUSTOMER`, `ADMIN`, `COURIER`, `KITCHEN`. |
| **Статус пользователя** | Одно из: `ACTIVE`, `BLOCKED`, `DELETED`, `PENDING_VERIFICATION`. |

---

## 3. Архитектура (нотация C4)

> Диаграммы приведены в нотации **C4** на **PlantUML** с использованием библиотеки `C4-PlantUML`.
> Для рендеринга нужен PlantUML с подключением `https://raw.githubusercontent.com/plantuml-stdlib/C4-PlantUML`.

### 3.1. Уровень 2 — Контейнерная диаграмма (контекст развёртывания)

```plantuml
@startuml C4_Container_AuthService
!include https://raw.githubusercontent.com/plantuml-stdlib/C4-PlantUML/master/C4_Container.puml

title Контейнерная диаграмма — restaurant-auth-service

Person(customer, "Клиент / Администратор / Курьер / Кухня", "Пользователь платформы онлайн-ресторана")

System_Boundary(platform, "Платформа онлайн-ресторана") {
    Container(clientApp, "Клиентское приложение", "Web / iOS / Android", "Отправляет учётные данные, хранит и обновляет токены")
    Container(authService, "Auth Service", "Go 1.24, net/http", "Аутентификация, выпуск и ротация токенов, JWKS")
    Container(resourceApi, "Restaurant API", "Resource-сервер", "Проверяет access-токены по JWKS, обслуживает заказы")
    ContainerDb(db, "PostgreSQL", "PostgreSQL 18", "users, user_roles, refresh_tokens, user_devices, schema_migrations")
}

Rel(customer, clientApp, "Использует")
Rel(clientApp, authService, "register / login / refresh / logout / me", "HTTPS, JSON")
Rel(clientApp, resourceApi, "Запросы с Bearer access-токеном", "HTTPS, JSON")
Rel(resourceApi, authService, "Получает публичный ключ (JWKS) / introspect", "HTTPS, JSON")
Rel(authService, db, "Чтение/запись пользователей и refresh-токенов", "TCP, pgx")

@enduml
```

### 3.2. Уровень 3 — Компонентная диаграмма (внутреннее устройство сервиса)

```plantuml
@startuml C4_Component_AuthService
!include https://raw.githubusercontent.com/plantuml-stdlib/C4-PlantUML/master/C4_Component.puml

title Компонентная диаграмма — Auth Service (Go)

Container(clientApp, "Клиентское приложение", "Web / iOS / Android")
ContainerDb(db, "PostgreSQL", "PostgreSQL 18")

Container_Boundary(auth, "Auth Service (Go)") {
    Component(main, "Entrypoint", "cmd/auth-service/main.go", "Инициализация: config → store → migrations → token → service → HTTP-сервер, graceful shutdown")
    Component(config, "Config", "internal/config", "Загрузка настроек из переменных окружения")
    Component(httpapi, "HTTP API / Router", "internal/httpapi", "Маршрутизация, обработчики, middleware (recover, auth), валидация входных данных, маппинг ошибок в HTTP-коды")
    Component(service, "Auth Service Logic", "internal/service", "Бизнес-логика: Register, Login, Refresh, Logout, Me, Introspect; транзакции; bcrypt; генерация refresh")
    Component(token, "Token Manager", "internal/token", "Выпуск/валидация JWT RS256, формирование JWKS")
    Component(store, "Store (Repository)", "internal/store", "SQL-доступ к PostgreSQL через pgx, транзакции, SELECT FOR UPDATE")
    Component(domain, "Domain Model", "internal/domain", "Сущности User, RefreshToken; перечисления ролей/статусов; доменные ошибки")
    Component(migrations, "Migrations Runner", "migrations", "Встроенные версионированные миграции под advisory lock")
}

Rel(clientApp, httpapi, "HTTP-запросы", "HTTPS, JSON")
Rel(main, config, "Загружает")
Rel(main, store, "Открывает пул соединений")
Rel(main, migrations, "Применяет при старте")
Rel(main, token, "Инициализирует")
Rel(main, service, "Создаёт")
Rel(main, httpapi, "Регистрирует обработчики и запускает сервер")

Rel(httpapi, service, "Вызывает бизнес-операции")
Rel(httpapi, store, "Ping для health-check")
Rel(service, store, "Чтение/запись, транзакции")
Rel(service, token, "Issue / Validate / JWKS")
Rel(service, domain, "Использует сущности и ошибки")
Rel(token, domain, "Использует роли/ошибки")
Rel(store, domain, "Маппинг строк в сущности")
Rel(migrations, db, "DDL/DML")
Rel(store, db, "SQL", "pgx / pgxpool")

@enduml
```

### 3.3. Слоистая архитектура

Сервис построен по принципу **чистой слоистой архитектуры** с однонаправленными зависимостями:

```
cmd/auth-service ──► internal/httpapi ──► internal/service ──► internal/store ──► PostgreSQL
                                                  │
                                                  └──► internal/token (JWT)
                                                  │
                                          internal/domain  (общие сущности и ошибки, не зависит ни от чего)
```

| Слой | Пакет | Ответственность |
|------|-------|-----------------|
| Точка входа | `cmd/auth-service` | Сборка зависимостей, запуск, graceful shutdown |
| Конфигурация | `internal/config` | Чтение настроек из ENV |
| Транспорт (HTTP) | `internal/httpapi` | Маршруты, валидация, сериализация, middleware, коды ответов |
| Бизнес-логика | `internal/service` | Сценарии аутентификации, транзакционность |
| Токены | `internal/token` | JWT RS256, JWKS |
| Хранилище | `internal/store` | SQL, pgx, транзакции |
| Домен | `internal/domain` | Сущности, перечисления, доменные ошибки |
| Миграции | `migrations` | Версионированные SQL-миграции |

---

## 4. Модель данных

### 4.1. ER-диаграмма

```mermaid
erDiagram
    users ||--o{ user_roles : "имеет"
    users ||--o{ refresh_tokens : "владеет"
    users ||--o{ user_devices : "регистрирует"

    users {
        UUID id PK
        VARCHAR phone UK
        VARCHAR password_hash
        VARCHAR status
        TIMESTAMPTZ created_at
        TIMESTAMPTZ updated_at
        TIMESTAMPTZ last_login_at "nullable"
    }
    user_roles {
        UUID user_id FK
        VARCHAR role
    }
    refresh_tokens {
        UUID id PK
        UUID user_id FK
        VARCHAR token_hash UK
        TIMESTAMPTZ expires_at
        TIMESTAMPTZ revoked_at "nullable"
        TIMESTAMPTZ created_at
        UUID device_id "nullable"
    }
    user_devices {
        UUID id PK
        UUID user_id FK
        VARCHAR platform
        TEXT push_token "nullable"
        TIMESTAMPTZ created_at
        TIMESTAMPTZ updated_at
    }
    schema_migrations {
        BIGINT version PK
        TIMESTAMPTZ applied_at
    }
```

### 4.2. Описание таблиц

**`users`** — учётные записи. `phone` уникален (`ux_users_phone`). `status` ограничен `CHECK` значениями перечисления. Пароль хранится как **bcrypt-хэш** (`password_hash`).

**`user_roles`** — роли пользователя (связь many-to-many через композитный PK `user_id + role`). Каскадное удаление при удалении пользователя. `role` ограничена `CHECK`.

**`refresh_tokens`** — выданные refresh-токены. Хранится **только SHA‑256‑хэш** (`token_hash`, уникальный индекс), сам токен в БД отсутствует. Поля `expires_at` и `revoked_at` управляют валидностью. Есть индексы по `user_id` и `expires_at`.

**`user_devices`** — устройства пользователя (платформа `WEB`/`IOS`/`ANDROID`, push-токен). Таблица создана схемой, но в текущей бизнес-логике сервиса не используется (заготовка под будущее, связь `refresh_tokens.device_id`).

**`schema_migrations`** — журнал применённых миграций (создаётся раннером миграций).

> **Примечание для аналитика:** перечисления статусов и ролей продублированы в трёх местах — в коде (`internal/domain`), в `CHECK`-ограничениях БД и в валидации токена. При добавлении новой роли/статуса нужно синхронно править все три источника.

---

## 5. Функциональные требования и API

Базовый префикс отсутствует, порт по умолчанию — `8081`. Формат обмена — `application/json; charset=UTF-8`.

| Метод | Путь | Доступ | Назначение |
|-------|------|--------|-----------|
| POST | `/auth/register` | публичный | Регистрация нового пользователя |
| POST | `/auth/login` | публичный | Вход, выдача пары токенов |
| POST | `/auth/refresh` | публичный | Ротация: обмен refresh на новую пару |
| POST | `/auth/logout` | публичный | Отзыв refresh-токена |
| GET | `/auth/me` | Bearer JWT | Данные текущего пользователя |
| POST | `/auth/introspect` | публичный | Проверка валидности access-токена |
| GET | `/auth/.well-known/jwks.json` | публичный | Публичный ключ (JWKS) |
| GET | `/actuator/health` | публичный | Проверка работоспособности |

### 5.1. Правила валидации входных данных

- **Телефон** — формат E.164: регулярное выражение `^\+[1-9][0-9]{7,14}$` (от 8 до 15 цифр после `+`).
- **Пароль** — длина от **6** до **72** символов (по числу рун UTF‑8), не должен состоять только из пробелов.
- **Тело запроса** — строго один JSON-объект; лишние данные после объекта отвергаются. Максимальный размер тела — **1 МБ**.

### 5.2. Контракты эндпоинтов

#### `POST /auth/register`
Запрос: `{ "phone": "+79990000000", "password": "secret1" }`
Ответ `201`: `{ "userId": "<uuid>", "phone": "+79990000000", "status": "ACTIVE" }`
Новому пользователю всегда присваивается статус `ACTIVE` и единственная роль `CUSTOMER`.
Ошибки: `400` (валидация), `409` (телефон уже зарегистрирован).

#### `POST /auth/login`
Запрос: `{ "phone": "+79990000000", "password": "secret1" }`
Ответ `200`:
```json
{ "accessToken": "<jwt>", "refreshToken": "<opaque>", "tokenType": "Bearer", "expiresIn": 900 }
```
Ошибки: `400` (валидация), `401` (неверные учётные данные), `403` (пользователь не активен).

#### `POST /auth/refresh`
Запрос: `{ "refreshToken": "<opaque>" }`
Ответ `200`: новая пара токенов (как у login). Старый refresh-токен **отзывается**.
Ошибки: `400`, `401` (невалидный/истёкший refresh), `403` (пользователь не активен).

#### `POST /auth/logout`
Запрос: `{ "refreshToken": "<opaque>" }`
Ответ `200`: `{ "message": "Logged out successfully" }`. Refresh-токен отзывается (идемпотентность по факту: повторный отзыв уже невалидного вернёт `401`).

#### `GET /auth/me`
Заголовок: `Authorization: Bearer <accessToken>`
Ответ `200`: `{ "userId", "phone", "roles": [...], "status" }`.
Ошибки: `401` (нет/невалидный токен), `403` (пользователь не активен).

#### `POST /auth/introspect`
Запрос: `{ "token": "<accessToken>" }`
Ответ `200` (активен): `{ "active": true, "userId", "phone", "roles": [...] }`
Ответ `200` (неактивен/невалиден): `{ "active": false }`
Проверяется не только подпись токена, но и **текущая активность пользователя в БД**.

#### `GET /auth/.well-known/jwks.json`
Ответ `200`: JWKS с одним RSA-ключом (`kty`, `e`, `use=sig`, `kid`, `alg=RS256`, `n`). Заголовок `Cache-Control: no-store`.

#### `GET /actuator/health`
Ответ `200`: `{ "status": "UP" }` (если БД отвечает на `Ping`), иначе `503`: `{ "status": "DOWN" }`.

### 5.3. Модель ошибок

Все ошибки возвращаются единым форматом:
```json
{ "timestamp": "...", "status": 400, "error": "Bad Request", "message": "...", "path": "/auth/login" }
```

| Доменная ошибка | HTTP-код |
|-----------------|----------|
| `ErrDuplicatePhone` | 409 Conflict |
| `ErrInvalidCredentials`, `ErrInvalidAccessToken`, `ErrInvalidRefreshToken`, `ErrExpiredRefreshToken` | 401 Unauthorized |
| `ErrUserNotActive` | 403 Forbidden |
| Любая прочая (внутренняя) | 500 Internal Server Error |

---

## 6. Диаграммы выполнения методов (sequence diagrams)

> Диаграммы последовательности в нотации Mermaid отражают пошаговое выполнение ключевых методов сервиса. Участники: **Client**, **HTTP API** (`internal/httpapi`), **Service** (`internal/service`), **Token** (`internal/token`), **Store** (`internal/store`), **DB** (PostgreSQL).

### 6.1. Регистрация — `Service.Register`

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant H as HTTP API
    participant S as Service
    participant DB as Store / PostgreSQL

    C->>H: POST /auth/register {phone, password}
    H->>H: decode + validateCredentials
    alt валидация не пройдена
        H-->>C: 400 Bad Request
    else корректно
        H->>S: Register(phone, password)
        S->>DB: BEGIN tx
        S->>DB: PhoneExists(phone)?
        alt телефон существует
            S->>DB: ROLLBACK
            S-->>H: ErrDuplicatePhone
            H-->>C: 409 Conflict
        else свободен
            S->>S: bcrypt.GenerateFromPassword(cost=12)
            S->>DB: INSERT users (status=ACTIVE)
            S->>DB: INSERT user_roles (CUSTOMER)
            S->>DB: COMMIT
            S-->>H: User
            H-->>C: 201 {userId, phone, status}
        end
    end
```

### 6.2. Вход — `Service.Login`

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant H as HTTP API
    participant S as Service
    participant T as Token
    participant DB as Store / PostgreSQL

    C->>H: POST /auth/login {phone, password}
    H->>H: decode + validateCredentials
    H->>S: Login(phone, password)
    S->>DB: BEGIN tx
    S->>DB: UserByPhone(phone)
    alt не найден
        S-->>H: ErrInvalidCredentials
        H-->>C: 401 Unauthorized
    else найден
        S->>S: bcrypt.CompareHashAndPassword
        alt пароль неверный
            S-->>H: ErrInvalidCredentials
            H-->>C: 401 Unauthorized
        else пароль верный
            alt пользователь не ACTIVE
                S-->>H: ErrUserNotActive
                H-->>C: 403 Forbidden
            else ACTIVE
                S->>DB: UPDATE last_login_at
                S->>T: Issue(user) → access JWT (RS256)
                S->>S: rand 64 байта → refresh (opaque)
                S->>DB: INSERT refresh_tokens (SHA-256 hash)
                S->>DB: COMMIT
                S-->>H: TokenPair
                H-->>C: 200 {accessToken, refreshToken, tokenType, expiresIn}
            end
        end
    end
```

### 6.3. Ротация — `Service.Refresh`

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant H as HTTP API
    participant S as Service
    participant T as Token
    participant DB as Store / PostgreSQL

    C->>H: POST /auth/refresh {refreshToken}
    H->>S: Refresh(plain)
    S->>DB: BEGIN tx
    S->>DB: RefreshForUpdate(SHA-256 hash) -- SELECT ... FOR UPDATE
    Note over S,DB: validRefresh: проверки существования,<br/>отзыва, срока, активности пользователя
    alt не найден / отозван
        S-->>H: ErrInvalidRefreshToken
        H-->>C: 401 Unauthorized
    else истёк
        S-->>H: ErrExpiredRefreshToken
        H-->>C: 401 Unauthorized
    else пользователь не активен
        S-->>H: ErrUserNotActive
        H-->>C: 403 Forbidden
    else валиден
        S->>DB: UPDATE refresh_tokens SET revoked_at (отзыв старого)
        S->>T: Issue(user) → новый access JWT
        S->>DB: INSERT refresh_tokens (новый)
        S->>DB: COMMIT
        S-->>H: TokenPair
        H-->>C: 200 {новая пара токенов}
    end
```

### 6.4. Выход — `Service.Logout`

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant H as HTTP API
    participant S as Service
    participant DB as Store / PostgreSQL

    C->>H: POST /auth/logout {refreshToken}
    H->>S: Logout(plain)
    S->>DB: BEGIN tx
    S->>DB: RefreshForUpdate (validRefresh)
    alt невалиден / истёк / не активен
        S-->>H: соответствующая ошибка
        H-->>C: 401 / 403
    else валиден
        S->>DB: UPDATE refresh_tokens SET revoked_at
        S->>DB: COMMIT
        S-->>H: ok
        H-->>C: 200 {message: "Logged out successfully"}
    end
```

### 6.5. Аутентификация по access-токену — middleware + `GET /auth/me`

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant M as Middleware authenticate
    participant S as Service
    participant T as Token
    participant DB as Store / PostgreSQL

    C->>M: GET /auth/me (Authorization: Bearer <jwt>)
    M->>M: Проверка префикса "Bearer "
    alt нет токена
        M-->>C: 401 Invalid access token
    else есть токен
        M->>S: ValidateAccess(jwt)
        S->>T: Validate(jwt) -- подпись RS256, iss, aud, exp, iat, роли
        alt токен невалиден
            T-->>M: ErrInvalidAccessToken
            M-->>C: 401 Invalid access token
        else валиден → userID
            M->>S: Me(userID)
            S->>DB: UserByID(userID)
            alt не найден
                S-->>M: ErrInvalidAccessToken
                M-->>C: 401
            else не активен
                S-->>M: ErrUserNotActive
                M-->>C: 403
            else ok
                M->>M: положить userID в context
                M->>S: handler me → Me(userID)
                S-->>M: User
                M-->>C: 200 {userId, phone, roles, status}
            end
        end
    end
```

### 6.6. Интроспекция — `Service.Introspect`

```mermaid
sequenceDiagram
    autonumber
    participant R as Resource-сервер / Client
    participant H as HTTP API
    participant S as Service
    participant T as Token
    participant DB as Store / PostgreSQL

    R->>H: POST /auth/introspect {token}
    H->>S: Introspect(token)
    S->>T: Validate(token)
    alt подпись/claims невалидны
        S-->>H: active=false
        H-->>R: 200 {active: false}
    else валиден → userID
        S->>DB: UserByID(userID)
        alt не найден или не активен
            S-->>H: active=false
            H-->>R: 200 {active: false}
        else активен
            S-->>H: User, active=true
            H-->>R: 200 {active: true, userId, phone, roles}
        end
    end
```

---

## 7. Безопасность

### 7.1. Хранение паролей
Пароли хэшируются алгоритмом **bcrypt с cost-фактором 12**. В БД хранится только хэш; исходный пароль нигде не сохраняется и не логируется.

### 7.2. Access-токены (JWT RS256)
- Подпись асимметричная — **RSA / RS256**. Приватный ключ известен только auth-сервису, проверка возможна по публичному ключу (JWKS).
- Состав claims: `iss` (issuer), `sub` (UUID пользователя), `aud` (audience), `exp`, `iat`, `jti` (уникальный id токена), а также кастомные `phone` и `roles`.
- В заголовке токена присутствует `kid` (идентификатор ключа), что позволяет ротировать ключи.
- При валидации проверяются: метод подписи строго `RS256`, `issuer`, `audience`, обязательность и корректность `exp`/`iat`, непустой `phone`, непустой и валидный список ролей.

### 7.3. Refresh-токены
- Генерируются из **64 криптослучайных байт** (`crypto/rand`), кодируются base64url — непрозрачны для клиента.
- В БД хранится только **SHA‑256‑хэш**, что исключает компрометацию при утечке дампа таблицы.
- Реализована **ротация с отзывом**: при каждом `refresh` старый токен помечается `revoked_at`, выдаётся новый. Чтение строки выполняется с `SELECT ... FOR UPDATE`, что обеспечивает атомарность и защищает от гонок при параллельных обновлениях.

### 7.4. Контроль активности
Любая операция, требующая «живого» пользователя (login, refresh, me, introspect), повторно сверяется со **статусом пользователя в БД**. Заблокированный (`BLOCKED`) пользователь не сможет обновить токен, даже имея валидный refresh.

### 7.5. Транспортные и инфраструктурные меры
- Ограничение размера тела запроса (1 МБ) — защита от переполнения.
- Таймауты HTTP-сервера: `ReadHeaderTimeout 5s`, `ReadTimeout 15s`, `WriteTimeout 15s`, `IdleTimeout 60s`.
- `recover`-middleware перехватывает паники и возвращает `500` без раскрытия деталей.
- Docker-образ собирается на базе **distroless `nonroot`** — минимальная поверхность атаки, запуск не от root.
- Приватный ключ монтируется в контейнер только на чтение и исключён из системы контроля версий.

---

## 8. Конфигурация

Все параметры читаются из переменных окружения (есть значения по умолчанию).

| Переменная | По умолчанию | Назначение |
|-----------|--------------|-----------|
| `HTTP_PORT` | `8081` | Порт HTTP-сервера |
| `DATABASE_URL` | `postgres://restaurant_auth:...@localhost:5432/restaurant_auth?sslmode=disable` | Строка подключения к PostgreSQL |
| `JWT_ISSUER` | `restaurant-auth-service` | Значение `iss` в токене |
| `JWT_AUDIENCE` | `restaurant-api` | Значение `aud` в токене |
| `JWT_ACCESS_TTL` | `15m` | Срок жизни access-токена |
| `REFRESH_TOKEN_TTL` | `720h` | Срок жизни refresh-токена |
| `JWT_KEY_ID` | `local-dev-key` | `kid` ключа в JWKS |
| `JWT_PRIVATE_KEY_PATH` | `./config/keys/private.pem` | Путь к приватному ключу |
| `JWT_PUBLIC_KEY_PATH` | `./config/keys/public.pem` | Путь к публичному ключу |

**Пул соединений PostgreSQL:** `MaxConns=20`, `MinConns=2`, `MaxConnLifetime=1h`.

---

## 9. Развёртывание и эксплуатация

### 9.1. Локальный запуск
```bash
make compose-up    # генерирует RSA-ключи, поднимает PostgreSQL + сервис
```
Сервис доступен на `http://localhost:8081`. При конфликте портов:
`POSTGRES_PORT=55432 AUTH_PORT=18081 make compose-up`.

### 9.2. Миграции
Применяются **самим приложением при старте**: под PostgreSQL advisory lock (`pg_advisory_lock(8142026)`), каждая миграция выполняется в отдельной транзакции и регистрируется в `schema_migrations`. Это безопасно при одновременном старте нескольких экземпляров — миграции не выполнятся дважды.

### 9.3. Graceful shutdown
По сигналам `SIGINT`/`SIGTERM` сервер прекращает приём новых соединений и в течение 10 секунд завершает активные запросы.

### 9.4. Тестовые (dev) пользователи
| Телефон | Пароль | Роль | Статус |
|---------|--------|------|--------|
| `+79990000000` | `123456` | CUSTOMER | ACTIVE |
| `+79991111111` | `admin123` | ADMIN | ACTIVE |
| `+79992222222` | `blocked123` | CUSTOMER | BLOCKED |

---

## 10. Нефункциональные характеристики и замечания

- **Тестирование:** покрыт юнит-тестами модуль токенов (`internal/token/jwt_test.go`): корректность выпуска/валидации, отсутствие утечки приватной экспоненты в JWKS, отклонение неверного `audience`.
- **Наблюдаемость:** структурированное JSON-логирование (`log/slog`). Метрик/трейсинга в текущей версии нет.
- **Заготовки на будущее:** таблица `user_devices` и поле `refresh_tokens.device_id` присутствуют в схеме, но бизнес-логика их пока не задействует (потенциал для привязки сессий к устройствам).
- **Потенциальные направления развития:** rate limiting на login/register, восстановление пароля, верификация телефона (статус `PENDING_VERIFICATION` уже предусмотрен), эндпоинт массового отзыва всех refresh-токенов пользователя, ротация подписывающих ключей.

---

*Документ подготовлен на основе анализа исходного кода ветки `master`. Полный снимок исходного кода — в сопроводительном файле `auth-service-source-code.md`.*
