/* Сгенерировано scripts/gen.mjs из contracts/openapi. НЕ РЕДАКТИРОВАТЬ. */
export interface paths {
    "/carts": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Начать заказ / создать корзину
         * @description Создаёт корзину в контексте выбранного ресторана (ресторан проверяется в restaurant-service). Поддерживается гостевое оформление (без авторизации, `guest=true`) с контактными данными. Неавторизованный запрос без `guest=true` — `401`. (UC-ORD-01/03)
         */
        post: operations["createCart"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/carts/{cartId}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Состав и сумма корзины */
        get: operations["getCart"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/carts/{cartId}/fulfillment": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * Выбрать/сменить способ получения
         * @description Устанавливает способ получения (доставка / самовывоз) и, при доставке, адрес (`ADDRESS_REQUIRED`, если пуст; ресторан должен поддерживать доставку — `DELIVERY_NOT_AVAILABLE`). **S7:** у адреса обязательны координаты (`ADDRESS_LOCATION_REQUIRED`), а адрес должен быть в радиусе доставки ресторана (`ADDRESS_OUT_OF_ZONE`, расширения `distanceMeters`/`radiusMeters`, `hint=pickup`). (UC-ORD-01, UC-RCV-01)
         */
        put: operations["setCartFulfillment"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/carts/{cartId}/items": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * Обновить корзину (позиции, модификаторы, количество)
         * @description Заменяет состав корзины целиком (до 50 строк; количество 1…99; до 20 модификаторов на строку; одинаковые строки — `400`) и пересчитывает суммы по актуальным ценам menu-service (`validateMenuItems`; таймаут 800 мс, один ретрай, circuit breaker). Пустой список очищает корзину. Недоступные позиции (E1) **удаляются из корзины**, остальные сохраняются, ответ — `409` `ITEMS_UNAVAILABLE` со списком `unavailableItems`; актуальный состав — `getCart`. menu недоступен → `503 MENU_UNAVAILABLE`, корзина не меняется (MG2). Заказанная/отменённая корзина — `409 CART_CLOSED`.
         */
        put: operations["updateCartItems"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/orders": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Оформить заказ
         * @description Фиксирует финальный состав и сумму, присваивает номер и создаёт заказ на основе корзины. Порядок (ADR-10): проверка ресторана (открыт — E2) и повторная проверка позиций/цен в menu-service (E1) → одна транзакция: заказ, история статусов, события `orders.state` (`revision=1`) и `orders.status` в outbox, корзина закрывается, ответ сохраняется под `Idempotency-Key`. Начальный статус: `pending_payment` для онлайн-оплаты, `accepted` для оплаты курьеру/на кассе. Заказ не уходит в производство, пока не оплачен (см. payment-service, S3).
         *
         *     `Idempotency-Key` **обязателен**; повтор с тем же ключом и телом возвращает тот же `201` (заголовок `Idempotent-Replay: true`) без нового заказа; тот же ключ с другим телом — `422 IDEMPOTENCY_KEY_REUSED`; параллельный дубликат — `409 IDEMPOTENCY_IN_PROGRESS`. Корзину можно оформить один раз (`409 CART_CLOSED`). Если цены изменились с момента расчёта корзины — `409 PRICE_CHANGED`, корзина обновлена, клиент подтверждает новую сумму и повторяет запрос с новым ключом. Может создаваться клиентом (UC-ORD-03) или сотрудником от имени клиента (`channel=staff_fax`, UC-ORD-02, S6): нужна роль персонала с доступом к ресторану корзины (корзину сотрудник создаёт сам, `POST /carts`), `contact.phone` клиента обязателен, способ оплаты — только `pos` (самовывоз) или `courier_card` (доставка): клиента у кассы нет, онлайн-оплата невозможна. Заказ принадлежит клиенту-гостю, сотрудник записывается в журнал статусов; в `orders.state` `source=fax`.
         */
        post: operations["createOrder"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/orders/{orderId}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Получить заказ */
        get: operations["getOrder"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/orders/{orderId}/cancellation": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Отменить заказ (персонал)
         * @description Отменяет заказ в статусе `pending_payment` или `accepted` (до начала приготовления; дальше — `409 ORDER_STATE_CONFLICT`). Публикует `orders.status` (`cancelled`, `reason` = код причины). Оплаченный заказ возвращает деньги payment-service (`Order.refundStatus`: `pending` → `refunded`); неоплаченную онлайн-операцию он отменяет у шлюза. Повтор для уже отменённого заказа — `200` без эффекта.
         */
        post: operations["cancelOrder"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/orders/{orderId}/eta": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Просмотреть время получения заказа
         * @description Возвращает ориентировочное время готовности (для самовывоза) или доставки. Оценка считается из локальной реплики загрузки производства (`production.lead.time`) без синхронного обращения к системе производства (S4): `readyAt` = момент принятия заказа (`accepted`) + `leadTimeMinutes` ресторана; для самовывоза со слотом — не раньше начала слота. Реплика свежая → `approximate=false`. Реплики нет или она устарела (производство молчит, E1) → типовое время (настраивается) и `approximate=true`; сам заказ при этом не теряется. Для заказа, уже ставшего `ready`, `readyAt` — момент готовности, `estimateMinutes=0`.
         *
         *     **Доставка (S7).** `deliveryAt` — время вручения. Если delivery-service сообщил оценку курьерской системы (`etaAt` в `orders.delivery.status`, не старше срока годности), то `deliveryAt` = она и `approximate=false` (при условии свежей оценки производства или готового заказа); иначе — `readyAt` + типовое время доставки (`ETA_DELIVERY_MINUTES`) и `approximate=true`. Для доставленного заказа `deliveryAt` — фактическое время `delivered`. (UC-ORD-05, UC-RCV-02)
         */
        get: operations["getOrderEta"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/orders/{orderId}/issuance": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Отметить выдачу заказа (персонал)
         * @description Фиксирует факт выдачи заказа клиенту (`ready → handed_over → closed`, одна транзакция, два события `orders.status`). Доступно сотруднику ресторана заказа при получении в ресторане (самовывоз). **S7:** заказ с доставкой выдаётся в ресторане только если доставка не состоялась из-за отсутствия курьера (`delivery.status=failed`, `failReason=no_courier`) — клиент принял предложение забрать заказ сам (E2). Условия: статус `ready`, `paymentStatus=paid`. Если оплата `pos` ещё не принята — `409 ORDER_NOT_PAID`: сначала `initiatePosPayment`. Повтор для уже выданного (`closed` через `handed_over`) заказа — `200` без эффекта. (UC-RCV-03, UC-RCV-01)
         */
        post: operations["markOrderIssued"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/orders/{orderId}/pickup-slot": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * Зафиксировать желаемый интервал получения
         * @description Сохраняет выбранный клиентом интервал получения (отложенное время, UC-ORD-05 A1). Только для самовывоза и только пока заказ в `pending_payment|accepted` (`409 ORDER_STATE_CONFLICT`). Интервал: `from < to`, `from` не в прошлом, длительность ≤ 4 ч, не дальше 7 суток вперёд (`422 INVALID_SLOT`); совпадение с режимом работы ресторана — S4. Изменение публикует `orders.state` с `revision+1`.
         */
        put: operations["setPickupSlot"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/orders/search": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Поиск заказа по номеру (персонал)
         * @description Поиск заказа сотрудником кафе по номеру для выдачи; только заказы ресторанов, к которым привязан сотрудник (`HQ_STAFF` — любые). Номер сопоставляется без учёта регистра и пробелов по краям. Заказ чужого ресторана неотличим от несуществующего: `404 ORDER_NOT_FOUND` — уточнить данные клиента или эскалировать (E1). В ответ, в отличие от `getOrder`, входят контакты клиента (для сверки при выдаче). (UC-RCV-03)
         */
        get: operations["searchOrder"];
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
        Address: {
            /** @description Координаты адреса от карты клиента. Для доставки обязательны (S7, `ADDRESS_LOCATION_REQUIRED`); для самовывоза не нужны. */
            location?: components["schemas"]["GeoPoint"];
            raw?: string;
        };
        AppliedPromotion: {
            discount?: components["schemas"]["Money"];
            /** Format: uuid */
            promotionId?: string;
            title?: string;
        };
        CancelOrderRequest: {
            /**
             * @description Код причины; попадает в `orders.status.reason` и в журнал статусов.
             * @enum {string}
             */
            reason: "customer_request" | "out_of_stock" | "restaurant_closed" | "other";
        };
        Cart: {
            appliedPromotions: components["schemas"]["AppliedPromotion"][];
            discountTotal: components["schemas"]["Money"];
            fulfillment?: components["schemas"]["FulfillmentInfo"];
            /** Format: uuid */
            id: string;
            items: components["schemas"]["CartItem"][];
            /**
             * Format: uuid
             * @description Заказ, созданный из корзины.
             */
            orderId?: string;
            /** Format: uuid */
            restaurantId: string;
            /**
             * @description `ordered` — корзина оформлена в заказ и изменению не подлежит.
             * @enum {string}
             */
            status: "open" | "ordered";
            subtotal: components["schemas"]["Money"];
            total: components["schemas"]["Money"];
        };
        CartCreateRequest: {
            contact?: components["schemas"]["ContactInfo"];
            /** @description Гостевое оформление без авторизации (UC-ORD-03 A1). */
            guest?: boolean;
            /** Format: uuid */
            restaurantId: string;
        };
        CartItem: {
            /** @description В корзине и заказе всегда `true`; недоступные позиции удаляются. */
            available: boolean;
            lineTotal: components["schemas"]["Money"];
            /** Format: uuid */
            menuItemId: string;
            modifierIds: string[];
            /** @description Название позиции на момент расчёта (снапшот). */
            name: string;
            quantity: number;
            /** @description Цена за единицу с модификаторами на момент расчёта. */
            unitPrice: components["schemas"]["Money"];
        };
        CartItemInput: {
            /** Format: uuid */
            menuItemId: string;
            modifierIds?: string[];
            quantity: number;
        };
        CartItemsUpdate: {
            items: components["schemas"]["CartItemInput"][];
        };
        ContactInfo: {
            /** Format: email */
            email?: string;
            name?: string;
            /** @description E.164 */
            phone?: string;
        };
        Eta: {
            /** @description true — показано типовое время, потому что свежей оценки производства нет (E1): реплика `production.lead.time` отсутствует или устарела. false — время рассчитано по свежей загрузке ресторана. */
            approximate?: boolean;
            /** Format: date-time */
            deliveryAt?: string;
            estimateMinutes?: number;
            /** Format: date-time */
            readyAt?: string;
        };
        FulfillmentInfo: {
            address?: components["schemas"]["Address"];
            type: components["schemas"]["FulfillmentType"];
        };
        FulfillmentInput: {
            address?: components["schemas"]["Address"];
            type: components["schemas"]["FulfillmentType"];
        };
        /** @enum {string} */
        FulfillmentType: "delivery" | "pickup";
        GeoPoint: {
            /** Format: double */
            lat: number;
            /** Format: double */
            lon: number;
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
        Order: {
            /** @description Акции, применённые при оформлении (снимок, S8). */
            appliedPromotions?: components["schemas"]["AppliedPromotion"][];
            /** @enum {string} */
            channel?: "online" | "staff_fax";
            /** Format: date-time */
            createdAt: string;
            delivery?: components["schemas"]["OrderDelivery"];
            discountTotal: components["schemas"]["Money"];
            eta?: components["schemas"]["Eta"];
            fulfillment: components["schemas"]["FulfillmentInfo"];
            /** Format: uuid */
            id: string;
            items: components["schemas"]["CartItem"][];
            /**
             * @description Человекочитаемый номер, уникален глобально, формат `R<ресторан>-<YYMMDD>-<NNNN>` (счётчик на ресторан и день).
             * @example R2-261018-0007
             */
            number: string;
            paymentMethod: components["schemas"]["PaymentMethodType"];
            paymentStatus: components["schemas"]["PaymentStatus"];
            pickupSlot?: components["schemas"]["PickupSlot"];
            /** @description Есть только у оплаченного заказа, по которому возвращаются деньги. */
            refundStatus?: components["schemas"]["RefundStatus"];
            /** Format: uuid */
            restaurantId: string;
            status: components["schemas"]["OrderStatus"];
            /** @description Причина статуса: `paid`, `payment_timeout`, `pickup_timeout`, `issued`, `courier_picked_up`, `delivered`, `undelivered` (S7) или код причины отмены персоналом (`customer_request`, `out_of_stock`, `restaurant_closed`, `other`). */
            statusReason?: string;
            subtotal: components["schemas"]["Money"];
            total: components["schemas"]["Money"];
        };
        OrderCreateRequest: {
            /** Format: uuid */
            cartId: string;
            /**
             * @description Канал оформления (клиент онлайн / сотрудник по факсу). `staff_fax` — только для персонала (см. описание `createOrder`).
             * @default online
             * @enum {string}
             */
            channel: "online" | "staff_fax";
            /** @description Контакты клиента; переопределяют контакты корзины. Для гостя обязателен `phone` (`CONTACT_REQUIRED`). */
            contact?: components["schemas"]["ContactInfo"];
            /** @description Итог к оплате, который видел клиент в корзине (S8). Если пересчёт даёт другую сумму (акция началась или закончилась) — `409 TOTAL_CHANGED`, заказ не создаётся; клиент перечитывает корзину и подтверждает новую сумму новым ключом. Не передан — проверки нет. */
            expectedTotal?: components["schemas"]["Money"];
            paymentMethod: components["schemas"]["PaymentMethodType"];
        };
        /** @description Снимок доставки из `orders.delivery.status` (только заказ с доставкой, после первого события delivery-service). Подробности (курьер, координаты) — `getDeliveryInfo` и `getCourierLocation` (delivery.yaml). */
        OrderDelivery: {
            /** Format: date-time */
            etaAt?: string;
            /** @enum {string} */
            failReason?: "no_courier" | "undelivered" | "cancelled";
            /**
             * @description `pickup` при `failed/no_courier`: предложить клиенту забрать заказ в ресторане (E2).
             * @enum {string}
             */
            hint?: "pickup";
            /** @enum {string} */
            status: "searching" | "assigned" | "picked_up" | "en_route" | "delivered" | "failed";
            /** Format: date-time */
            updatedAt?: string;
        };
        /**
         * @description Совпадает с `orders.status` (AsyncAPI) плюс стартовый `pending_payment`. Допустимые переходы: `pending_payment→{accepted,cancelled}`, `accepted→{cooking,cancelled}`, `cooking→ready`, `ready→{handed_over,on_delivery,not_received}`, `on_delivery→{delivered,not_received}`, `{delivered,handed_over,not_received}→closed`.
         * @enum {string}
         */
        OrderStatus: "pending_payment" | "accepted" | "cooking" | "ready" | "on_delivery" | "delivered" | "handed_over" | "closed" | "not_received" | "cancelled";
        /** @description Problem Details; при `ADDRESS_OUT_OF_ZONE` содержит расстояние до ресторана, радиус доставки и подсказку `pickup`. */
        OutOfZoneProblem: components["schemas"]["Problem"] & {
            distanceMeters?: number;
            /** @enum {string} */
            hint?: "pickup";
            radiusMeters?: number;
        };
        /**
         * @description Онлайн-картой / курьеру (картой по ссылке) / на кассе.
         * @enum {string}
         */
        PaymentMethodType: "online_card" | "courier_card" | "pos";
        /** @enum {string} */
        PaymentStatus: "pending" | "authorized" | "paid" | "declined" | "unknown";
        PickupSlot: {
            /** Format: date-time */
            from: string;
            /** Format: date-time */
            to: string;
        };
        PickupSlotInput: {
            /** Format: date-time */
            from: string;
            /** Format: date-time */
            to: string;
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
         * @description Возврат денег по оплаченному заказу, который не будет исполнен (отмена; `paid` пришёл после отмены). `pending` — возврат запрошен и выполняется (повторяется с паузами); `refunded` — деньги возвращены; `failed` — шлюз/касса определённо отказали, нужна ручная обработка. Для заказа без возврата поле отсутствует.
         * @enum {string}
         */
        RefundStatus: "pending" | "refunded" | "failed";
        /** @description Заказ с контактами клиента — ответ `searchOrder`. */
        StaffOrder: components["schemas"]["Order"] & {
            contact?: components["schemas"]["ContactInfo"];
            /** @description Сотрудник, оформивший заказ (только `channel=staff_fax`). */
            createdBy?: string;
        };
        UnavailableItem: {
            /** Format: uuid */
            menuItemId: string;
            reason?: string;
            reasonCode: components["schemas"]["ValidationReasonCode"];
        };
        /** @description Problem Details; при `ITEMS_UNAVAILABLE` содержит `unavailableItems`. */
        UnavailableItemsProblem: components["schemas"]["Problem"] & {
            unavailableItems?: components["schemas"]["UnavailableItem"][];
        };
        /**
         * @description `ITEM_NOT_FOUND` — позиции нет в меню ресторана; `MODIFIER_NOT_FOUND` — модификатор не принадлежит позиции; `*_UNAVAILABLE` — в стоп-листе.
         * @enum {string}
         */
        ValidationReasonCode: "ITEM_NOT_FOUND" | "ITEM_UNAVAILABLE" | "MODIFIER_NOT_FOUND" | "MODIFIER_UNAVAILABLE";
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
        /** @description Недостаточно прав */
        Forbidden: {
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
        /** @description Семантически некорректные данные */
        UnprocessableEntity: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/problem+json": components["schemas"]["Problem"];
            };
        };
    };
    parameters: {
        CartId: string;
        /** @description Уникальный ключ (1–128 печатных ASCII-символов), гарантирующий однократное создание заказа при повторной отправке. */
        IdempotencyKeyRequired: string;
        OrderId: string;
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
    createCart: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CartCreateRequest"];
            };
        };
        responses: {
            /** @description Корзина создана */
            201: {
                headers: {
                    Location: components["headers"]["Location"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Cart"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            /** @description Ресторан не найден или не активен (`RESTAURANT_NOT_FOUND`) */
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            /** @description restaurant-service недоступен (`RESTAURANT_UNAVAILABLE`) */
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
    getCart: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                cartId: components["parameters"]["CartId"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Текущий состав и предварительная сумма */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Cart"];
                };
            };
            404: components["responses"]["NotFound"];
        };
    };
    setCartFulfillment: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                cartId: components["parameters"]["CartId"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["FulfillmentInput"];
            };
        };
        responses: {
            /** @description Способ получения зафиксирован, сумма пересчитана */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Cart"];
                };
            };
            400: components["responses"]["BadRequest"];
            404: components["responses"]["NotFound"];
            /** @description Корзина закрыта (`CART_CLOSED`) */
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            /** @description `ADDRESS_REQUIRED`, `ADDRESS_LOCATION_REQUIRED`, `ADDRESS_OUT_OF_ZONE`, `DELIVERY_NOT_AVAILABLE` */
            422: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["OutOfZoneProblem"];
                };
            };
            /** @description restaurant-service недоступен (`RESTAURANT_UNAVAILABLE`) */
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
    updateCartItems: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                cartId: components["parameters"]["CartId"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CartItemsUpdate"];
            };
        };
        responses: {
            /** @description Обновлённая корзина (состав, применённые акции, сумма) */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Cart"];
                };
            };
            400: components["responses"]["BadRequest"];
            404: components["responses"]["NotFound"];
            /** @description Позиция недоступна — состав скорректирован (`ITEMS_UNAVAILABLE`, E1) либо корзина закрыта (`CART_CLOSED`) */
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["UnavailableItemsProblem"];
                };
            };
            /** @description menu-service недоступен (`MENU_UNAVAILABLE`) */
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
    createOrder: {
        parameters: {
            query?: never;
            header: {
                /** @description Уникальный ключ (1–128 печатных ASCII-символов), гарантирующий однократное создание заказа при повторной отправке. */
                "Idempotency-Key": components["parameters"]["IdempotencyKeyRequired"];
            };
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["OrderCreateRequest"];
            };
        };
        responses: {
            /** @description Заказ создан, присвоен номер */
            201: {
                headers: {
                    /** @description `true`, если это повтор уже обработанного запроса. */
                    "Idempotent-Replay"?: "true";
                    Location: components["headers"]["Location"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Order"];
                };
            };
            400: components["responses"]["BadRequest"];
            /** @description `UNAUTHORIZED` — `channel=staff_fax` без аутентификации */
            401: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            /** @description `FORBIDDEN` — `channel=staff_fax` без роли персонала или для ресторана вне привязки сотрудника */
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            /** @description Корзина не найдена (`CART_NOT_FOUND`) */
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            /** @description `ITEMS_UNAVAILABLE` (E1; список в `unavailableItems`), `PRICE_CHANGED`, `TOTAL_CHANGED` (S8: итог с учётом акций не совпал с `expectedTotal`), `RESTAURANT_CLOSED` (E2), `CART_CLOSED`, `CART_CHANGED` (корзина изменена параллельно), `IDEMPOTENCY_IN_PROGRESS` */
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["UnavailableItemsProblem"];
                };
            };
            /** @description `CART_EMPTY`, `FULFILLMENT_REQUIRED`, `CONTACT_REQUIRED`, `PAYMENT_METHOD_NOT_ALLOWED` (для `staff_fax` — и `online_card`), `IDEMPOTENCY_KEY_REUSED`, `DELIVERY_NOT_AVAILABLE`, `ADDRESS_LOCATION_REQUIRED`, `ADDRESS_OUT_OF_ZONE` (S7: повторная проверка зоны; расширения `distanceMeters`/`radiusMeters`) */
            422: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["OutOfZoneProblem"];
                };
            };
            /** @description menu-/restaurant-service недоступны (`MENU_UNAVAILABLE`, `RESTAURANT_UNAVAILABLE`); корзина сохранена */
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
    getOrder: {
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
            /** @description Данные заказа (состав, статус, суммы, способ получения/оплаты) */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Order"];
                };
            };
            404: components["responses"]["NotFound"];
        };
    };
    cancelOrder: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                orderId: components["parameters"]["OrderId"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CancelOrderRequest"];
            };
        };
        responses: {
            /** @description Заказ отменён */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Order"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            /** @description Заказ нельзя отменить в текущем статусе (`ORDER_STATE_CONFLICT`) */
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
        };
    };
    getOrderEta: {
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
            /** @description Ориентировочное время получения */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Eta"];
                };
            };
            404: components["responses"]["NotFound"];
        };
    };
    markOrderIssued: {
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
            /** @description Выдача зафиксирована, заказ закрыт */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Order"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            /** @description `ORDER_STATE_CONFLICT` (заказ не `ready`: ещё готовится, отменён, не получен), `ORDER_NOT_PAID` (оплата не принята) */
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            /** @description `NOT_PICKUP_ORDER` — заказ с доставкой выдаёт курьер (исключение — `failed/no_courier`, см. выше) */
            422: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
        };
    };
    setPickupSlot: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                orderId: components["parameters"]["OrderId"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["PickupSlotInput"];
            };
        };
        responses: {
            /** @description Интервал получения сохранён */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Order"];
                };
            };
            400: components["responses"]["BadRequest"];
            404: components["responses"]["NotFound"];
            /** @description Заказ нельзя менять в текущем статусе (`ORDER_STATE_CONFLICT`) */
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            422: components["responses"]["UnprocessableEntity"];
        };
    };
    searchOrder: {
        parameters: {
            query: {
                /** @description Номер заказа, формат `R<код>-<YYMMDD>-<NNNN>` */
                number: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Заказ найден */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["StaffOrder"];
                };
            };
            /** @description `INVALID_PARAMETER` / `INVALID_ORDER_NUMBER` — номер не указан или не соответствует формату */
            400: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            /** @description Заказ не найден или принадлежит другому ресторану (E1, `ORDER_NOT_FOUND`) */
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
