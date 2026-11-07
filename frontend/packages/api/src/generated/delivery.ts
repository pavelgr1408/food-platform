/* Сгенерировано scripts/gen.mjs из contracts/openapi. НЕ РЕДАКТИРОВАТЬ. */
export interface paths {
    "/orders/{orderId}/delivery": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Информация о доставке
         * @description Возвращает статус доставки и назначенного курьера по заказу. Назначение курьера инициируется по событию `orders.state`; при отсутствии курьера (`failed/no_courier`) клиенту предлагается самовывоз (`hint=pickup`, E2). (UC-RCV-02)
         */
        get: operations["getDeliveryInfo"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/orders/{orderId}/delivery/courier-location": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Координаты курьера (пулинг)
         * @description Возвращает последние координаты курьера из delivery-Redis для отслеживания доставки. Клиент периодически опрашивает эндпоинт и отображает курьера на карте (карта — только на клиенте). Координаты обновляются пулингом из внешней курьерской системы, пока доставка `picked_up`/`en_route`. (UC-RCV-02, loop «Пулинг координат курьера»)
         */
        get: operations["getCourierLocation"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
}
export interface webhooks {
    courierDeliveryStatus: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Статус доставки от курьерской системы
         * @description Асинхронное уведомление курьерской системы (приёмник — delivery-service, путь `POST /webhooks/delivery/status`, **мимо api-gateway**). Подпись — `X-Signature` (секрет `COURIER_WEBHOOK_SECRET`; без секрета endpoint отвечает `503 WEBHOOK_DISABLED`). Обработка идемпотентна по `eventId`: повтор доставки возвращает `200` без эффекта; статус не откатывается назад. Статус публикуется в `orders.delivery.status`. Признак `paid` информативен: **деньги подтверждает только payment-service** по вебхуку платёжного шлюза (UC-PAY-03). (UC-RCV-02, UC-PAY-03)
         */
        post: operations["onCourierDeliveryStatus"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
}
export interface components {
    schemas: {
        Courier: {
            name?: string;
            phone?: string;
        };
        CourierLocation: {
            location: components["schemas"]["GeoPoint"];
            /** Format: date-time */
            updatedAt: string;
        };
        /**
         * @description `no_courier` — курьер не назначен за `SEARCH_TIMEOUT` (E2); `undelivered` — курьер выехал, но не доставил; `cancelled` — заказ отменён, доставка снята.
         * @enum {string}
         */
        DeliveryFailReason: "no_courier" | "undelivered" | "cancelled";
        DeliveryInfo: {
            /** @description Есть, пока доставка `assigned`/`picked_up`/`en_route`. */
            courier?: components["schemas"]["Courier"];
            /** Format: uuid */
            deliveryId?: string;
            /**
             * Format: date-time
             * @description Ожидаемое время вручения по оценке курьерской системы.
             */
            etaAt?: string;
            failReason?: components["schemas"]["DeliveryFailReason"];
            /**
             * @description `pickup` при `failed/no_courier`: предложить забрать заказ в ресторане (E2).
             * @enum {string}
             */
            hint?: "pickup";
            /** Format: uuid */
            orderId: string;
            /** @description Заказ оплачивается курьеру (`courier_card`, UC-PAY-03). */
            payOnDelivery?: boolean;
            status: components["schemas"]["DeliveryStatus"];
            /** Format: date-time */
            updatedAt: string;
        };
        /**
         * @description Совпадает с `orders.delivery.status` (AsyncAPI).
         * @enum {string}
         */
        DeliveryStatus: "searching" | "assigned" | "picked_up" | "en_route" | "delivered" | "failed";
        DeliveryStatusCallback: {
            courier?: components["schemas"]["Courier"];
            courierId?: string;
            /** Format: uuid */
            deliveryId?: string;
            /** Format: date-time */
            etaAt?: string;
            /** @description Идентификатор уведомления (дедупликация повторной доставки). */
            eventId: string;
            /** Format: uuid */
            orderId: string;
            /** @description Курьер сообщает об оплате при вручении (информативно; UC-PAY-03). */
            paid?: boolean;
            /** @description Причина `failed` (`no_courier`, `undelivered`). */
            reason?: string;
            /** @enum {string} */
            status: "assigned" | "picked_up" | "en_route" | "delivered" | "failed";
            /** Format: date-time */
            timestamp: string;
        };
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
        OrderId: string;
    };
    requestBodies: never;
    headers: never;
    pathItems: never;
}
export type $defs = Record<string, never>;
export interface operations {
    getDeliveryInfo: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                orderId: components["parameters"]["OrderId"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Сведения о доставке */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["DeliveryInfo"];
                };
            };
            401: components["responses"]["Unauthorized"];
            /** @description `DELIVERY_NOT_FOUND` — заказа нет, он чужой, это самовывоз или событие `orders.state` ещё не дошло (повторить через секунду-две). */
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
    getCourierLocation: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                orderId: components["parameters"]["OrderId"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Последние координаты курьера */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["CourierLocation"];
                };
            };
            401: components["responses"]["Unauthorized"];
            /** @description `DELIVERY_NOT_FOUND` (см. `getDeliveryInfo`) или `LOCATION_NOT_AVAILABLE` — курьер не назначен/не выехал, доставка завершена, либо точка устарела (старше `LOCATION_TTL`). */
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            /** @description `LOCATION_STORE_UNAVAILABLE` — delivery-Redis недоступен; статус доставки (`getDeliveryInfo`) при этом работает. Клиент повторяет опрос позже. */
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
    onCourierDeliveryStatus: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["DeliveryStatusCallback"];
            };
        };
        responses: {
            /** @description Уведомление принято (в том числе повтор — без эффекта) */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            /** @description Подпись отсутствует/неверна или вне окна replay (`INVALID_SIGNATURE`) */
            401: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            /** @description Доставка неизвестна (`DELIVERY_NOT_FOUND`) — курьерская система повторит позже */
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            /** @description `WEBHOOK_DISABLED` — секрет вебхука не задан */
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
}
