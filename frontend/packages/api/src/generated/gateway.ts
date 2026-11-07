/* Сгенерировано scripts/gen.mjs из contracts/openapi. НЕ РЕДАКТИРОВАТЬ. */
export type paths = Record<string, never>;
export type webhooks = Record<string, never>;
export interface components {
    schemas: {
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
        Readiness: {
            components?: {
                [key: string]: {
                    /** @enum {string} */
                    status?: "UP" | "DOWN";
                };
            };
            /** @enum {string} */
            status: "UP" | "DOWN";
        };
    };
    responses: {
        /** @description `UPSTREAM_UNAVAILABLE` — сервис не принял соединение или оборвал ответ. */
        BadGateway: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["Problem"];
            };
        };
        /** @description `FORBIDDEN` — у субъекта нет роли, требуемой маршрутом. */
        Forbidden: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["Problem"];
            };
        };
        /** @description `UPSTREAM_TIMEOUT` — сервис не ответил за `UPSTREAM_TIMEOUT`. */
        GatewayTimeout: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["Problem"];
            };
        };
        /** @description `ROUTE_NOT_FOUND` — маршрут или метод вне таблицы (в том числе внутренние пути сервисов). */
        NotFound: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["Problem"];
            };
        };
        /** @description `PAYLOAD_TOO_LARGE` — тело больше `MAX_BODY_BYTES`. */
        PayloadTooLarge: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["Problem"];
            };
        };
        /** @description `AUTH_UNAVAILABLE` — для `sensitive`-маршрута не удалось выполнить `introspect` (fail-closed). */
        ServiceUnavailable: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["Problem"];
            };
        };
        /** @description `RATE_LIMITED` — превышен лимит запросов. */
        TooManyRequests: {
            headers: {
                "Retry-After": components["headers"]["RetryAfter"];
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["Problem"];
            };
        };
        /**
         * @description `UNAUTHORIZED` — токен отсутствует на маршруте `required`, просрочен, подпись неверна, неизвестный
         *     `kid`, не тот `iss`/`aud`, либо (для `sensitive`) пользователь неактивен по `introspect`.
         *     Заголовок `WWW-Authenticate: Bearer error="invalid_token"`.
         */
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
    headers: {
        /** @description Через сколько секунд повторить запрос. */
        RetryAfter: number;
        /** @description Идентификатор запроса (для корреляции логов). */
        XRequestId: string;
    };
    pathItems: never;
}
export type $defs = Record<string, never>;
export type operations = Record<string, never>;
