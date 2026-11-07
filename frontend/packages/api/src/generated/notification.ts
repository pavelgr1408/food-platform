/* Сгенерировано scripts/gen.mjs из contracts/openapi. НЕ РЕДАКТИРОВАТЬ. */
export interface paths {
    "/notifications/device-tokens": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Токены устройств текущего пользователя
         * @description Токены текущего пользователя, от новых к старым. Чужие токены не возвращаются.
         */
        get: operations["listDeviceTokens"];
        put?: never;
        /**
         * Зарегистрировать разрешения и push-токен
         * @description Регистрирует push-токен устройства и разрешения на уведомления. Операция естественно идемпотентна: пара (`platform`, `token`) уникальна. Новый токен — `201`. Повторная регистрация того же токена тем же пользователем обновляет `permissionsGranted` и язык и отвечает `200` с тем же `id`. Если токен был у другого пользователя (устройство сменило владельца), он переходит к текущему пользователю — `200`, прежний владелец его теряет. У пользователя хранится не более 10 токенов: при регистрации одиннадцатого самый старый по `registeredAt` удаляется. Токены присылают клиенты, умеющие push; нативные мобильные приложения вне объёма проекта (ADR-12), поэтому в проекте токены регистрируют тестовые клиенты, а провайдеры APNs/FCM/HMS — моки.
         */
        post: operations["registerDeviceToken"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/notifications/device-tokens/{tokenId}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        /**
         * Отозвать push-токен
         * @description Удаляет токен текущего пользователя (например, при выходе из аккаунта). Уведомления, которые ещё не отправлены на это устройство, отменяются. Чужой или несуществующий токен — `404 DEVICE_TOKEN_NOT_FOUND`.
         */
        delete: operations["deleteDeviceToken"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
}
export type webhooks = Record<string, never>;
export interface components {
    schemas: {
        DeviceToken: components["schemas"]["DeviceTokenRegistration"] & {
            /** Format: uuid */
            id: string;
            /**
             * @description Язык уведомлений для устройства.
             * @enum {string}
             */
            locale: "ru" | "en";
            permissionsGranted: boolean;
            /**
             * Format: date-time
             * @description Время первой регистрации (при повторной регистрации не меняется).
             */
            registeredAt: string;
        };
        DeviceTokenRegistration: {
            /**
             * @description Пользователь разрешил уведомления; `false` — токен хранится, но push не отправляются.
             * @default true
             */
            permissionsGranted: boolean;
            platform: components["schemas"]["PushPlatform"];
            /** @description Push-токен устройства, выданный провайдером (без пробелов и переводов строк). */
            token: string;
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
        /** @enum {string} */
        PushPlatform: "apns" | "fcm" | "huawei";
    };
    responses: {
        /** @description Некорректный запрос */
        BadRequest: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["Problem"];
            };
        };
        /** @description Ресурс не найден */
        NotFound: {
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
    listDeviceTokens: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Список зарегистрированных токенов */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["DeviceToken"][];
                };
            };
            401: components["responses"]["Unauthorized"];
        };
    };
    registerDeviceToken: {
        parameters: {
            query?: never;
            header?: {
                /** @description Язык уведомлений для этого устройства (`ru`, `en`); при другом значении используется `ru`. */
                "Accept-Language"?: string;
            };
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["DeviceTokenRegistration"];
            };
        };
        responses: {
            /** @description Токен уже был зарегистрирован — данные обновлены */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["DeviceToken"];
                };
            };
            /** @description Токен зарегистрирован */
            201: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["DeviceToken"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
        };
    };
    deleteDeviceToken: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                tokenId: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Токен удалён */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            401: components["responses"]["Unauthorized"];
            404: components["responses"]["NotFound"];
        };
    };
}
