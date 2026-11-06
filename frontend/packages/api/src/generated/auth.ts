/* Сгенерировано scripts/gen.mjs из contracts/openapi. НЕ РЕДАКТИРОВАТЬ. */
export interface paths {
    "/auth/.well-known/jwks.json": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Публичные ключи подписи (JWKS)
         * @description Набор публичных ключей RS256 (`kid` совпадает с заголовком JWT). Потребители кэшируют набор и перечитывают его при неизвестном `kid` (ротация ключа без простоя).
         */
        get: operations["getJwks"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/auth/authorize": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Проверить полномочия субъекта на действие (internal)
         * @description Решает, может ли **владелец Bearer-токена** выполнить действие. Решение принимается по
         *     **актуальным данным БД** (статус, роли, привязка к ресторанам), а не по claims токена, —
         *     поэтому блокировка и смена привязки действуют сразу. Вызывающий сервис передаёт
         *     токен пользователя как есть. Отказ — **не ошибка**: `200`, `allowed=false` и `reason`.
         *
         *     Каталог действий (`action`):
         *
         *     | action | scope | Кто допускается |
         *     |---|---|---|
         *     | `promotion.manage` | `local` + `restaurantId` | `FRANCHISE_OWNER`, привязанный к этому ресторану; `HQ_STAFF` |
         *     | `promotion.manage` | `national` | только `HQ_STAFF` |
         *     | `order.manage` | `restaurantId` обязателен | `STAFF` или `FRANCHISE_OWNER`, привязанные к ресторану; `HQ_STAFF` |
         *
         *     `reason` для отказа: `NOT_ACTIVE`, `ROLE_REQUIRED`, `NOT_BOUND_TO_RESTAURANT`, `SCOPE_NOT_ALLOWED`.
         *     Неизвестное `action` или неполный запрос (нет `restaurantId`/`scope`, где они обязательны) —
         *     `400` (`UNKNOWN_ACTION` / `VALIDATION_FAILED`).
         *     Для простых проверок по ролям достаточно локального анализа токена (`roles`, `restaurants`).
         */
        post: operations["authorize"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/auth/introspect": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Онлайн-проверка access-токена (internal)
         * @description Проверяет подпись, срок и **актуальность пользователя** (заблокированный → `active=false`). Всегда отвечает 200: недействительный токен — `{"active": false}` без данных субъекта (RFC 7662). Только для вызовов из кластера (api-gateway не публикует путь наружу; в S11 — NetworkPolicy); gateway использует для чувствительных маршрутов (создание заказа, оплата) вместо проверки только по JWKS. Заменяет `verifyToken` из ДЗ-1.
         */
        post: operations["introspectToken"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/auth/login": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Вход по телефону и паролю
         * @description Выдаёт пару токенов: access (JWT RS256, короткий TTL) и refresh (непрозрачный, одноразовый). Одинаковый ответ 401 на неверный пароль и неизвестный телефон (нет перечисления пользователей). Используется всеми клиентскими и персональными приложениями. Коды: `401 INVALID_CREDENTIALS`, `403 USER_NOT_ACTIVE`. На api-gateway для этого пути действует отдельный, более строгий лимит запросов (защита от подбора пароля).
         */
        post: operations["login"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/auth/logout": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Выход (отзыв refresh-токена)
         * @description Отзывает переданный refresh-токен. Выданные access-токены остаются действительными до истечения TTL (отзыв «сейчас же» — через `introspect` на чувствительных путях).
         */
        post: operations["logout"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/auth/me": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Текущий пользователь */
        get: operations["getCurrentPrincipal"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/auth/refresh": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Обновить пару токенов
         * @description Ротация refresh-токена: старый отзывается атомарно, выдаётся новая пара. Повторное использование старого токена — `401 INVALID_REFRESH_TOKEN`; просроченный — `401 REFRESH_TOKEN_EXPIRED`.
         */
        post: operations["refreshTokens"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/auth/register": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Регистрация клиента
         * @description Создаёт пользователя с ролью `CUSTOMER` и статусом `ACTIVE`. Идентификатор — телефон в формате E.164, уникален. Пароль хранится как bcrypt-хэш. Ошибки: `400 VALIDATION_FAILED|INVALID_JSON`, `409 PHONE_ALREADY_REGISTERED`.
         */
        post: operations["register"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
}
export type webhooks = Record<string, never>;
export interface components {
    schemas: {
        AuthorizeDecision: {
            allowed: boolean;
            /**
             * @description Причина отказа; у `allowed=true` отсутствует.
             * @enum {string}
             */
            reason?: "NOT_ACTIVE" | "ROLE_REQUIRED" | "NOT_BOUND_TO_RESTAURANT" | "SCOPE_NOT_ALLOWED";
        };
        AuthorizeRequest: {
            /**
             * @example promotion.manage
             * @enum {string}
             */
            action: "promotion.manage" | "order.manage";
            /**
             * Format: uuid
             * @description Ресторан, к которому проверяется привязка (для `local` и `order.manage`).
             */
            restaurantId?: string;
            /**
             * @description Область действия; обязательна для `promotion.manage`.
             * @enum {string}
             */
            scope?: "local" | "national";
        };
        Credentials: {
            /** Format: password */
            password: string;
            /**
             * @description Телефон в формате E.164
             * @example +79991234567
             */
            phone: string;
        };
        IntrospectionResult: {
            active: boolean;
            phone?: string;
            restaurants?: string[];
            roles?: components["schemas"]["Role"][];
            /** Format: uuid */
            userId?: string;
        };
        Jwks: {
            keys: {
                /** @enum {string} */
                alg: "RS256";
                /**
                 * @description Экспонента (base64url)
                 * @example AQAB
                 */
                e: string;
                /** @example local-dev-key */
                kid: string;
                /** @enum {string} */
                kty: "RSA";
                /** @description Модуль (base64url) */
                n: string;
                /** @enum {string} */
                use: "sig";
            }[];
        };
        Principal: {
            phone: string;
            /** @description Привязка сотрудника/владельца к ресторанам (`user_restaurants`); пусто у CUSTOMER и HQ_STAFF. */
            restaurants?: string[];
            roles: components["schemas"]["Role"][];
            status: components["schemas"]["UserStatus"];
            /** Format: uuid */
            userId: string;
        };
        /** @description Формат ошибки по RFC 9457 (Problem Details). */
        Problem: {
            /**
             * @description Машиночитаемый код ошибки
             * @example ITEM_UNAVAILABLE
             */
            code?: string;
            detail?: string;
            /** Format: uri-reference */
            instance?: string;
            /** @example 409 */
            status?: number;
            /** @example Позиция недоступна */
            title?: string;
            /**
             * Format: uri
             * @default about:blank
             */
            type: string;
        };
        RefreshRequest: {
            refreshToken: string;
        };
        RegisteredUser: {
            /** @example +79991234567 */
            phone: string;
            status: components["schemas"]["UserStatus"];
            /** Format: uuid */
            userId: string;
        };
        /**
         * @description Роли платформы: `CUSTOMER` — клиент, `STAFF` — сотрудник кафе,
         *     `FRANCHISE_OWNER` — владелец франшизы, `HQ_STAFF` — сотрудник головной компании.
         *     `STAFF` и `FRANCHISE_OWNER` привязаны к ресторанам (`restaurants[]`); `HQ_STAFF` видит всё.
         *     Прежние роли Java-версии (`ADMIN`, `COURIER`, `KITCHEN`) удалены в 1.3.0
         *     (`ADMIN` → `HQ_STAFF`; курьер и кухня — внешние системы).
         * @enum {string}
         */
        Role: "CUSTOMER" | "STAFF" | "FRANCHISE_OWNER" | "HQ_STAFF";
        TokenPair: {
            /** @description JWT RS256. Заголовок: `kid`; claims: `sub` (userId), `phone`, `roles`, `restaurants` (UUID ресторанов привязки; может быть пустым), `iss`, `aud`, `exp`, `iat`, `jti`. */
            accessToken: string;
            /**
             * @description TTL access-токена, сек
             * @example 900
             */
            expiresIn: number;
            /** @description Непрозрачный одноразовый токен */
            refreshToken: string;
            /** @enum {string} */
            tokenType: "Bearer";
        };
        /** @enum {string} */
        UserStatus: "ACTIVE" | "BLOCKED" | "DELETED" | "PENDING_VERIFICATION";
    };
    responses: {
        /** @description Некорректный запрос: невалидный JSON (`INVALID_JSON`) или поля (`VALIDATION_FAILED`: `phone` не E.164, пароль вне 6–72 символов, пустой токен). */
        BadRequest: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["Problem"];
            };
        };
        /** @description Превышен лимит запросов (rate limit на api-gateway; заголовок `Retry-After`, секунды) */
        TooManyRequests: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["Problem"];
            };
        };
        /** @description Требуется аутентификация / токен недействителен */
        Unauthorized: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["Problem"];
            };
        };
    };
    parameters: never;
    requestBodies: never;
    headers: never;
    pathItems: never;
}
export type $defs = Record<string, never>;
export interface operations {
    getJwks: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description JWKS */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Jwks"];
                };
            };
            429: components["responses"]["TooManyRequests"];
        };
    };
    authorize: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["AuthorizeRequest"];
            };
        };
        responses: {
            /** @description Решение по авторизации */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["AuthorizeDecision"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
        };
    };
    introspectToken: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": {
                    /** @description Access-токен (JWT) */
                    token: string;
                };
            };
        };
        responses: {
            /** @description Результат проверки */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["IntrospectionResult"];
                };
            };
            400: components["responses"]["BadRequest"];
        };
    };
    login: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["Credentials"];
            };
        };
        responses: {
            /** @description Успешная аутентификация */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TokenPair"];
                };
            };
            400: components["responses"]["BadRequest"];
            /** @description Неверный телефон или пароль */
            401: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            /** @description Пользователь не активен (заблокирован, удалён, не подтверждён) */
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            429: components["responses"]["TooManyRequests"];
        };
    };
    logout: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["RefreshRequest"];
            };
        };
        responses: {
            /** @description Токен отозван */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        /** @example Logged out successfully */
                        message: string;
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            /** @description Refresh-токен неизвестен, уже отозван или просрочен */
            401: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
        };
    };
    getCurrentPrincipal: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Данные текущего субъекта */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Principal"];
                };
            };
            /** @description Токен отсутствует, недействителен или просрочен */
            401: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            /** @description Пользователь не активен */
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
        };
    };
    refreshTokens: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["RefreshRequest"];
            };
        };
        responses: {
            /** @description Новая пара токенов */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TokenPair"];
                };
            };
            400: components["responses"]["BadRequest"];
            /** @description Refresh-токен неизвестен, отозван или просрочен */
            401: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            /** @description Пользователь не активен */
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
        };
    };
    register: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["Credentials"];
            };
        };
        responses: {
            /** @description Пользователь создан */
            201: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["RegisteredUser"];
                };
            };
            400: components["responses"]["BadRequest"];
            /** @description Телефон уже зарегистрирован */
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            429: components["responses"]["TooManyRequests"];
        };
    };
}
