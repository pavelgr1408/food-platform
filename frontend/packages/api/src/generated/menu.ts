/* Сгенерировано scripts/gen.mjs из contracts/openapi. НЕ РЕДАКТИРОВАТЬ. */
export interface paths {
    "/restaurants/{restaurantId}/menu": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Меню и актуальные цены ресторана
         * @description Возвращает меню с актуальными ценами для выбранного ресторана. Первый шаг оформления заказа. Поддерживает условные запросы: `If-None-Match` с текущим `ETag` → `304` без тела. (UC-ORD-01, UC-ORD-03)
         */
        get: operations["getMenu"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/restaurants/{restaurantId}/menu/validation": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Проверить позиции по актуальному меню
         * @description Проверяет доступность и цены переданных позиций против актуального меню. Используется при вводе заказа сотрудником (UC-ORD-02) и при пересчёте корзины. Недоступность позиции — **не ошибка запроса**: ответ `200` с `valid=false` и причиной в строке (E1 «позиция недоступна»). Строки возвращаются в порядке запроса. `404` — только если у ресторана нет меню.
         */
        post: operations["validateMenuItems"];
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
        Menu: {
            categories: components["schemas"]["MenuCategory"][];
            /**
             * Format: int64
             * @description Растёт при каждом изменении меню.
             */
            menuVersion: number;
            /** Format: uuid */
            restaurantId: string;
        };
        MenuCategory: {
            /** Format: uuid */
            id: string;
            items: components["schemas"]["MenuItem"][];
            name: string;
        };
        MenuItem: {
            /** @description false — позиция в стоп-листе */
            available: boolean;
            description?: string;
            /** Format: uuid */
            id: string;
            /**
             * Format: uri
             * @description Фото блюда (HTTPS, необязательно): ссылка на изображение, сами файлы сервис не хранит. Отсутствует, если фото нет — клиент рисует иллюстрацию по названию. Смена ссылки меняет `menuVersion` и `ETag`. Загрузка и хранение файлов (S3/CDN) — отдельная задача.
             */
            imageUrl?: string;
            modifiers: components["schemas"]["Modifier"][];
            name: string;
            price: components["schemas"]["Money"];
        };
        MenuValidationLine: {
            available: boolean;
            /** @description Цена за единицу с выбранными модификаторами (позиция + модификаторы). Отсутствует для `ITEM_NOT_FOUND`. */
            currentPrice?: components["schemas"]["Money"];
            /** @description `currentPrice` × `quantity`. Отсутствует, если `currentPrice` нет. */
            lineTotal?: components["schemas"]["Money"];
            /** Format: uuid */
            menuItemId: string;
            /** @description Название позиции (для снапшота в корзине/заказе). Отсутствует для `ITEM_NOT_FOUND`. */
            name?: string;
            quantity: number;
            /** @description Причина недоступности, человекочитаемо; если есть */
            reason?: string;
            reasonCode?: components["schemas"]["ValidationReasonCode"];
        };
        MenuValidationRequest: {
            items: {
                /** Format: uuid */
                menuItemId: string;
                modifierIds?: string[];
                quantity: number;
            }[];
        };
        MenuValidationResult: {
            lines: components["schemas"]["MenuValidationLine"][];
            /** Format: int64 */
            menuVersion: number;
            /** @description Сумма `lineTotal` доступных строк. */
            total: components["schemas"]["Money"];
            /** @description true, если все строки доступны */
            valid: boolean;
        };
        Modifier: {
            available: boolean;
            /** Format: uuid */
            id: string;
            name: string;
            price: components["schemas"]["Money"];
        };
        /**
         * @description Денежная сумма. `amount` — **целое число минорных единиц валюты** (для RUB — копейки):
         *     `54990` = 549,90 ₽. Никаких дробных чисел и строк: сложение, сравнение и сверка
         *     платежей — целочисленные. Округление (скидки, проценты) выполняется один раз,
         *     на стороне orders-service, банковским способом (ADR-13).
         */
        Money: {
            /**
             * Format: int64
             * @description Сумма в минорных единицах (копейки для RUB), не отрицательная.
             * @example 54990
             */
            amount: number;
            /**
             * @description Код валюты ISO 4217
             * @example RUB
             */
            currency: string;
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
        /**
         * @description `ITEM_NOT_FOUND` — позиции нет в меню ресторана; `MODIFIER_NOT_FOUND` — модификатор не принадлежит позиции; `*_UNAVAILABLE` — в стоп-листе.
         * @enum {string}
         */
        ValidationReasonCode: "ITEM_NOT_FOUND" | "ITEM_UNAVAILABLE" | "MODIFIER_NOT_FOUND" | "MODIFIER_UNAVAILABLE";
    };
    responses: never;
    parameters: {
        RestaurantId: string;
    };
    requestBodies: never;
    headers: never;
    pathItems: never;
}
export type $defs = Record<string, never>;
export interface operations {
    getMenu: {
        parameters: {
            query?: never;
            header?: {
                "If-None-Match"?: string;
            };
            path: {
                restaurantId: components["parameters"]["RestaurantId"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Меню с ценами */
            200: {
                headers: {
                    /** @description `public, max-age=30`; авторитетная проверка перед заказом — `validateMenuItems`. */
                    "Cache-Control"?: string;
                    /** @description Сильный валидатор вида `"v<menuVersion>"`. */
                    ETag?: string;
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Menu"];
                };
            };
            /** @description Меню не изменилось (совпал `ETag`) */
            304: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            /** @description Для ресторана нет меню */
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
        };
    };
    validateMenuItems: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                restaurantId: components["parameters"]["RestaurantId"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["MenuValidationRequest"];
            };
        };
        responses: {
            /** @description Результат проверки позиций (доступность, актуальные цены) */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["MenuValidationResult"];
                };
            };
            /** @description Некорректный запрос: пустой список, более 100 строк, количество вне 1…99, не-UUID (`code`: `INVALID_JSON`, `INVALID_PARAMETER`). */
            400: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            /** @description Для ресторана нет меню */
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
        };
    };
}
