/* Сгенерировано scripts/gen.mjs из contracts/openapi. НЕ РЕДАКТИРОВАТЬ. */
export interface paths {
    "/promotions": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Действующие акции и спецпредложения
         * @description Возвращает объединённые национальные и локальные предложения, действующие сейчас (`status=active`, `startsAt ≤ now < endsAt`). Без `restaurantId` возвращаются только общенациональные акции (A1), с ним — национальные и локальные этого ресторана. Порядок: `priority` по убыванию, затем `startsAt`. Доступен гостю (токен не нужен). `region` зарезервирован (сейчас национальные акции действуют во всех регионах) и игнорируется. (UC-PRM-01)
         */
        get: operations["listActivePromotions"];
        put?: never;
        /**
         * Создать акцию (персонал)
         * @description Создаёт акцию в статусе `draft`. Охват (локальная для ресторана франшизы или общенациональная) проверяется по полномочиям (см. «Права»). `Idempotency-Key` обязателен. (UC-PRM-02/03/04)
         */
        post: operations["createPromotion"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/promotions/{promotionId}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Параметры акции (персонал)
         * @description Доступна тем, кто вправе управлять этой акцией (та же проверка, что и для изменения).
         */
        get: operations["getPromotion"];
        /**
         * Изменить акцию (персонал)
         * @description Полная замена параметров черновика или действующей акции в пределах полномочий; каждое изменение увеличивает `version`, фиксируется в истории, а у действующей акции — повторно публикуется. `scope` и `restaurantId` после создания не меняются (`422 SCOPE_IMMUTABLE`), но попытка выйти за пределы охвата (например, владелец ставит `scope=national`) — `403 SCOPE_NOT_ALLOWED`, как и в `createPromotion`. (UC-PRM-03 A1)
         */
        put: operations["updatePromotion"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/promotions/{promotionId}/activation": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Активировать и опубликовать акцию (персонал)
         * @description Проверяет правила приоритета (E2), переводит `draft → active` и в той же транзакции кладёт событие в outbox; публикация в orders-service идёт асинхронно через `promotions.discount`. Повторная активация уже действующей акции — `200` без эффекта. Акция с истёкшим `endsAt` не активируется (`422 PROMOTION_EXPIRED`). (UC-PRM-02/03/04)
         */
        post: operations["activatePromotion"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/promotions/{promotionId}/completion": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Завершить акцию (персонал)
         * @description Переводит акцию в `completed` и публикует событие `status=completed` (orders прекращает применять скидку); для черновика публикации нет. Повторное завершение — `200` без эффекта. (UC-PRM-02 A3, UC-PRM-04 A1)
         */
        post: operations["completePromotion"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/promotions/{promotionId}/history": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * История изменений акции (персонал)
         * @description Журнал версий акции от создания: действие, автор, время и снимок параметров. Новые записи — в конце. (UC-PRM-03: изменение фиксируется в истории)
         */
        get: operations["getPromotionHistory"];
        put?: never;
        post?: never;
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
        /**
         * @description `gift` из AsyncAPI не поддерживается: `422 PROMOTION_INVALID`.
         * @enum {string}
         */
        DiscountType: "percent" | "fixed";
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
        Promotion: components["schemas"]["PromotionPublic"] & {
            /** Format: date-time */
            createdAt: string;
            /** @description Идентификатор автора (userId). */
            createdBy?: string;
            status: components["schemas"]["PromotionStatus"];
            /** Format: date-time */
            updatedAt: string;
            /** @description Растёт при каждом изменении; передаётся в `promotions.discount`. */
            version: number;
        };
        /** @description Условия применения. Неизвестное поле — `400 INVALID_JSON` (как и любое неизвестное поле тела). */
        PromotionConditions: {
            /**
             * @description Только для указанного способа получения. Нет — для любого.
             * @enum {string}
             */
            fulfillment?: "pickup" | "delivery";
            /** @description Позиции меню, на которые действует скидка. Пусто или нет — на весь заказ. */
            menuItemIds?: string[];
            /**
             * Format: int64
             * @description Минимальный подытог заказа (до скидок), копейки.
             */
            minSubtotal?: number;
            /**
             * @description Суммируется с другими акциями (см. «Расчёт скидки»).
             * @default false
             */
            stackable: boolean;
        };
        PromotionConflictProblem: components["schemas"]["Problem"] & {
            conflictingPromotionIds?: string[];
        };
        PromotionDiscountValue: {
            type: components["schemas"]["DiscountType"];
            /**
             * Format: int64
             * @description `percent` — сотые доли процента, 1…10000 (1500 = 15%, 1250 = 12,5%); `fixed` — копейки, > 0 (10000 = 100 ₽). Только целые числа (ADR-13).
             */
            value: number;
        };
        PromotionInput: {
            conditions?: components["schemas"]["PromotionConditions"];
            /** @default RUB */
            currency: string;
            description?: string;
            discount: components["schemas"]["PromotionDiscountValue"];
            /**
             * Format: date-time
             * @description Строго позже `startsAt`.
             */
            endsAt: string;
            /** @default 0 */
            priority: number;
            /**
             * Format: uuid
             * @description Обязателен для `local` и запрещён для `national`; должен совпадать с привязкой владельца (`403 NOT_BOUND_TO_RESTAURANT`).
             */
            restaurantId?: string;
            scope: components["schemas"]["PromotionScope"];
            /** Format: date-time */
            startsAt: string;
            title: string;
        };
        PromotionPublic: {
            conditions?: components["schemas"]["PromotionConditions"];
            /** @example RUB */
            currency?: string;
            description?: string;
            discount: components["schemas"]["PromotionDiscountValue"];
            /** Format: date-time */
            endsAt: string;
            /** Format: uuid */
            id: string;
            priority: number;
            /**
             * Format: uuid
             * @description Заполняется для локальных акций.
             */
            restaurantId?: string;
            scope: components["schemas"]["PromotionScope"];
            /** Format: date-time */
            startsAt: string;
            title: string;
        };
        PromotionRevision: {
            /** @enum {string} */
            action: "created" | "updated" | "activated" | "completed";
            actorId?: string;
            /** Format: date-time */
            occurredAt: string;
            snapshot: components["schemas"]["Promotion"];
            version: number;
        };
        /**
         * @description Область действия акции — ресторан франшизы или вся сеть.
         * @enum {string}
         */
        PromotionScope: "local" | "national";
        /** @enum {string} */
        PromotionStatus: "draft" | "active" | "completed";
    };
    responses: {
        /** @description auth-service недоступен (`AUTH_UNAVAILABLE`), права проверить нельзя — отказ (fail-closed) */
        AuthUnavailable: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["Problem"];
            };
        };
        /** @description Некорректный запрос */
        BadRequest: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["Problem"];
            };
        };
        /** @description `PROMOTION_CONFLICT` (E2; расширение `conflictingPromotionIds`) или `PROMOTION_COMPLETED` (акция завершена и не меняется) */
        PromotionConflict: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["PromotionConflictProblem"];
            };
        };
        /** @description Недостаточно прав (`code` = причина auth-service: `NOT_ACTIVE`, `ROLE_REQUIRED`, `NOT_BOUND_TO_RESTAURANT`, `SCOPE_NOT_ALLOWED`) */
        PromotionForbidden: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["Problem"];
            };
        };
        /** @description Семантически некорректные параметры: `PROMOTION_INVALID` (период, размер скидки, значения условий, `restaurantId` не соответствует `scope`), `SCOPE_IMMUTABLE`, `PROMOTION_EXPIRED`, `IDEMPOTENCY_KEY_REUSED` */
        PromotionInvalid: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["Problem"];
            };
        };
        /** @description Акция не найдена (`PROMOTION_NOT_FOUND`) */
        PromotionNotFound: {
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
    parameters: {
        /** @description Уникальный ключ (1–128 печатных ASCII-символов), гарантирующий однократное создание акции при повторной отправке. */
        IdempotencyKeyRequired: string;
        PromotionId: string;
    };
    requestBodies: never;
    headers: {
        /** @description URI созданного ресурса. */
        Location: string;
    };
    pathItems: never;
}
export type $defs = Record<string, never>;
export interface operations {
    listActivePromotions: {
        parameters: {
            query?: {
                /** @description Зарезервирован, игнорируется. */
                region?: string;
                restaurantId?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Перечень действующих акций (условия, сроки) */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PromotionPublic"][];
                };
            };
            400: components["responses"]["BadRequest"];
            /** @description Подсистема акций недоступна (`PROMOTIONS_UNAVAILABLE`, E1). Переход к меню и оформлению не блокируется: orders считает скидку по собственной реплике. */
            503: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
        };
    };
    createPromotion: {
        parameters: {
            query?: never;
            header: {
                /** @description Уникальный ключ (1–128 печатных ASCII-символов), гарантирующий однократное создание акции при повторной отправке. */
                "Idempotency-Key": components["parameters"]["IdempotencyKeyRequired"];
            };
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["PromotionInput"];
            };
        };
        responses: {
            /** @description Акция создана (в статусе «Черновик») */
            201: {
                headers: {
                    /** @description `true`, если это повтор уже обработанного запроса. */
                    "Idempotent-Replay"?: "true";
                    Location: components["headers"]["Location"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Promotion"];
                };
            };
            /** @description `INVALID_JSON`, `VALIDATION_FAILED` (типы полей), `IDEMPOTENCY_KEY_REQUIRED` */
            400: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["PromotionForbidden"];
            /** @description `IDEMPOTENCY_IN_PROGRESS` — параллельный дубликат запроса */
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            422: components["responses"]["PromotionInvalid"];
            503: components["responses"]["AuthUnavailable"];
        };
    };
    getPromotion: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                promotionId: components["parameters"]["PromotionId"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Акция найдена */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Promotion"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["PromotionForbidden"];
            404: components["responses"]["PromotionNotFound"];
            503: components["responses"]["AuthUnavailable"];
        };
    };
    updatePromotion: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                promotionId: components["parameters"]["PromotionId"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["PromotionInput"];
            };
        };
        responses: {
            /** @description Акция обновлена */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Promotion"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["PromotionForbidden"];
            404: components["responses"]["PromotionNotFound"];
            409: components["responses"]["PromotionConflict"];
            422: components["responses"]["PromotionInvalid"];
            503: components["responses"]["AuthUnavailable"];
        };
    };
    activatePromotion: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                promotionId: components["parameters"]["PromotionId"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Акция опубликована целевой аудитории */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Promotion"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["PromotionForbidden"];
            404: components["responses"]["PromotionNotFound"];
            409: components["responses"]["PromotionConflict"];
            422: components["responses"]["PromotionInvalid"];
            503: components["responses"]["AuthUnavailable"];
        };
    };
    completePromotion: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                promotionId: components["parameters"]["PromotionId"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Акция завершена */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Promotion"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["PromotionForbidden"];
            404: components["responses"]["PromotionNotFound"];
            503: components["responses"]["AuthUnavailable"];
        };
    };
    getPromotionHistory: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                promotionId: components["parameters"]["PromotionId"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description История изменений */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PromotionRevision"][];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["PromotionForbidden"];
            404: components["responses"]["PromotionNotFound"];
            503: components["responses"]["AuthUnavailable"];
        };
    };
}
