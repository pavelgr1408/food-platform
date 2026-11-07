/* Сгенерировано scripts/gen.mjs из contracts/openapi. НЕ РЕДАКТИРОВАТЬ. */
export interface paths {
    "/restaurants": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Найти рестораны сети
         * @description Подбор ресторанов по геопозиции/адресу с признаком доставки и режимом работы. Геопозиция определяется на клиенте средствами Mapkit; бэкенд к картам не обращается (Д2). Поддерживает повторный подбор по фильтрам. (UC-FND-01)
         */
        get: operations["searchRestaurants"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/restaurants/{restaurantId}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Детали ресторана
         * @description Адрес и координаты выбранного ресторана; фиксируются как контекст сессии и используются далее в UC-ORD-01, а также клиентом для построения маршрута на Mapkit (UC-MAP-02).
         */
        get: operations["getRestaurant"];
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
        GeoPoint: {
            /** Format: double */
            lat: number;
            /** Format: double */
            lon: number;
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
        Restaurant: components["schemas"]["RestaurantSummary"] & {
            deliveryEnabled: boolean;
            /** @description Радиус доставки; 0, если доставки нет. */
            deliveryRadiusMeters?: number;
            scheduleExceptions?: components["schemas"]["ScheduleException"][];
            /**
             * @description Часовой пояс IANA
             * @example Europe/Moscow
             */
            timezone: string;
            workingHours: components["schemas"]["WorkingHours"][];
        };
        RestaurantPage: {
            center?: components["schemas"]["GeoPoint"];
            /**
             * @description Присутствует только при пустом `items`. `pickup` — доставка не покрывает точку, но рядом есть рестораны для самовывоза (повторить без `deliveryOnly`); `expand_radius` — увеличить `radiusMeters`; `change_address` — адрес не распознан или радиус уже максимальный.
             * @enum {string}
             */
            hint?: "pickup" | "expand_radius" | "change_address";
            items: components["schemas"]["RestaurantSummary"][];
            limit: number;
            offset: number;
            total: number;
        };
        RestaurantSummary: {
            address: string;
            city: string;
            deliveryAvailable: boolean;
            /** @description Расстояние до центра поиска; отсутствует, если центр не задан. */
            distanceMeters?: number;
            /** Format: uuid */
            id: string;
            /**
             * Format: uri
             * @description Фото ресторана (HTTPS, необязательно): ссылка на изображение, сами файлы сервис не хранит. Отсутствует, если фото нет — клиент рисует иллюстрацию по названию. Отдаётся в `listRestaurants` и `getRestaurant`. Загрузка и хранение файлов (S3/CDN) — отдельная задача.
             */
            imageUrl?: string;
            location: components["schemas"]["GeoPoint"];
            name: string;
            openNow: boolean;
        };
        /** @description Исключение на календарную дату (в часовом поясе ресторана): заменяет обычный режим этого дня. `closed=true` — в этот день закрыто (тогда `opensAt`/`closesAt` отсутствуют). Несколько записей на одну дату — несколько интервалов. */
        ScheduleException: {
            closed: boolean;
            closesAt?: string;
            /** Format: date */
            date: string;
            note?: string;
            opensAt?: string;
        };
        /** @description Интервал работы; дней/интервалов может быть несколько (перерыв). `closesAt` ≤ `opensAt` — закрытие на следующие сутки (ночной режим). */
        WorkingHours: {
            /** @example 22:00 */
            closesAt: string;
            /** @description 1 = понедельник … 7 = воскресенье (ISO 8601), день начала интервала */
            dayOfWeek: number;
            /** @example 09:00 */
            opensAt: string;
        };
    };
    responses: {
        /** @description Сервис временно недоступен */
        ServiceUnavailable: {
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
        /** @description Широта (определяется на клиенте средствами Mapkit, Д2). */
        Latitude: number;
        Limit: number;
        /** @description Долгота (определяется на клиенте средствами Mapkit, Д2). */
        Longitude: number;
        Offset: number;
        RestaurantId: string;
    };
    requestBodies: never;
    headers: never;
    pathItems: never;
}
export type $defs = Record<string, never>;
export interface operations {
    searchRestaurants: {
        parameters: {
            query?: {
                /** @description Город/адрес при ручном вводе без геолокации (A1). Распознаётся по справочнику городов; не распознан — пустой результат с `hint=change_address`. Игнорируется, если заданы `lat` и `lon`. */
                address?: string;
                /** @description Только рестораны с доставкой. */
                deliveryOnly?: boolean;
                /** @description Широта (определяется на клиенте средствами Mapkit, Д2). */
                lat?: components["parameters"]["Latitude"];
                limit?: components["parameters"]["Limit"];
                /** @description Долгота (определяется на клиенте средствами Mapkit, Д2). */
                lon?: components["parameters"]["Longitude"];
                offset?: components["parameters"]["Offset"];
                /** @description Только открытые сейчас (по режиму работы). */
                openNow?: boolean;
                /** @description Радиус поиска от центра; используется в т.ч. для расширения зоны (A2/E2). Без центра игнорируется. */
                radiusMeters?: number;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Список ресторанов (адрес, расстояние, режим, доставка). Может быть пустым — вне зоны обслуживания / справочник пуст (A2/E2), тогда клиенту предлагается самовывоз или смена адреса. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["RestaurantPage"];
                };
            };
            /** @description Некорректные параметры: задана только одна из `lat`/`lon`, значение вне диапазона (`code`: `INVALID_PARAMETER`, `COORDINATES_INCOMPLETE`). */
            400: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            401: components["responses"]["Unauthorized"];
            503: components["responses"]["ServiceUnavailable"];
        };
    };
    getRestaurant: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                restaurantId: components["parameters"]["RestaurantId"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Ресторан найден */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Restaurant"];
                };
            };
            /** @description Ресторан не найден или выведен из эксплуатации (`active=false`) */
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
