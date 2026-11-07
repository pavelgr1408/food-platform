/* Сгенерировано scripts/gen.mjs из contracts/openapi. НЕ РЕДАКТИРОВАТЬ. */
export interface paths {
    "/orders/{orderId}/payment-methods": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Доступные способы оплаты
         * @description Способы оплаты для способа получения заказа: самовывоз — `online_card`, `pos`; доставка — `online_card`, `courier_card`. `enabled=true` — только способ, выбранный при оформлении заказа, и только пока его можно оплатить: для `online_card` заказ должен быть в `pending_payment` (не отменён, не оплачен); `courier_card` и `pos` всегда `enabled=false`: курьерский платёж создаёт система (S7), кассовый — персонал. Способ оплаты меняется пересозданием заказа; смена способа у существующего заказа не поддерживается. Платёжный сервис знает о заказе из события `orders.state`. (UC-PAY-01)
         */
        get: operations["getPaymentMethods"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/orders/{orderId}/payments": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Инициировать платёж
         * @description Создаёт попытку оплаты заказа. **Онлайн-оплата (UC-PAY-02, S3):** платёж записывается в БД (`pending`), затем — вне транзакции — создаётся оплата в шлюзе; в ответе платёжная ссылка (`paymentUrl`). Итог приходит вебхуком шлюза либо запрашивается reconciler-ом. Статусы публикуются в `orders.payments.status` (orders принимает заказ после `paid`).
         *
         *     `Idempotency-Key` **обязателен**: повтор с тем же ключом и телом возвращает тот же ответ (`Idempotent-Replay: true`) без нового платежа; тот же ключ с другим телом — `422 IDEMPOTENCY_KEY_REUSED`.
         *
         *     Одновременно у заказа не более одной «живой» попытки и не более одного успешного платежа (гарантируется уникальными индексами БД): пока есть `pending` — `409 PAYMENT_IN_PROGRESS`, пока есть `unknown` — `409 PAYMENT_STATUS_UNKNOWN` (E2), после `paid` — `409 ALREADY_PAID`. После `declined` можно создать новую попытку (новый ключ). Шлюз не ответил в срок — `202` с `status=unknown`: платёж мог быть создан, ждите итога (`getPayment`).
         *
         *     `courier_card` (UC-PAY-03) создаётся системой по событию заказа (S7, см. описание контракта), `pos` — отдельная операция `initiatePosPayment` (S6); для них здесь `422 PAYMENT_METHOD_NOT_SUPPORTED`.
         */
        post: operations["initiatePayment"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/orders/{orderId}/payments/{paymentId}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Статус платежа
         * @description Текущий статус платежа — из БД payment-service (без обращения к шлюзу: итог подтягивают вебхук и reconciler). Используется при `202`/`unknown` и для опроса после перехода на страницу оплаты. (UC-PAY-01/02 E2) Платёж заказа ресторана читает и его сотрудник (`STAFF`/`FRANCHISE_OWNER` с этим рестораном, `HQ_STAFF` — любой): так кассир видит итог оплаты на кассе (`fiscalData`) и ход возврата (`refund`); чужой заказ — `404`.
         */
        get: operations["getPayment"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/orders/{orderId}/payments/pos": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Инициировать оплату на кассе/терминале (персонал)
         * @description Инициализирует оплату на кассе/терминале при получении в ресторане (см. «Оплата на кассе» в описании сервиса). Даже наличная оплата проводится через кассу с терминальным чеком (Д6). Терминал подтверждает операцию вебхуком кассовой системы (в ответ `pending`, итог — `getPayment`). `Idempotency-Key` обязателен, как у `initiatePayment`. (UC-PAY-04)
         */
        post: operations["initiatePosPayment"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
}
export interface webhooks {
    paymentGatewayStatus: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Статус оплаты от платёжного шлюза
         * @description Асинхронное уведомление о статусе онлайн-оплаты (приёмник — payment-service, путь `POST /webhooks/payments/gateway`, мимо api-gateway). Подпись — `X-Signature` (секрет `GATEWAY_WEBHOOK_SECRET`). Обработка идемпотентна по `eventId`: повтор доставки возвращает `200` без эффекта. Если вебхук потерян, статус подтягивает reconciler (`GET status` шлюза). (UC-PAY-02)
         */
        post: operations["onPaymentGatewayStatus"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    posPaymentConfirmation: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Подтверждение оплаты от кассовой системы
         * @description Подтверждение оплаты при получении в ресторане (приёмник — payment-service, путь `POST /webhooks/payments/pos`). Та же обработка и тот же формат, что у шлюза, плюс `fiscalData` (сохраняется в платеже); секрет — `POS_WEBHOOK_SECRET`. Принимает только платежи `method=pos`, которые создаёт `initiatePosPayment`. Статус публикуется в `orders.payments.status`. (UC-PAY-04)
         */
        post: operations["onPosPaymentConfirmation"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
}
export interface components {
    schemas: {
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
        Payment: {
            amount: components["schemas"]["Money"];
            /** @description Номер попытки оплаты этого заказа. */
            attempt: number;
            /** Format: date-time */
            createdAt: string;
            /** @description Причина отказа (`status=declined`): код шлюза, `gateway_rejected`, `expired` (попытка не оплачена за `PENDING_EXPIRY`) или `order_cancelled` (заказ отменён, пока операция была незавершена). */
            declineReason?: string;
            /** @description Фискальные данные кассовой операции (номер чека, ФН/ФД/ФПД и т. п.), как их вернула касса; есть у оплаченных `pos`-платежей. */
            fiscalData?: {
                [key: string]: unknown;
            };
            /** Format: uuid */
            id: string;
            method: components["schemas"]["PaymentMethodType"];
            /** Format: uuid */
            orderId: string;
            /** Format: date-time */
            paidAt?: string;
            /**
             * Format: uri
             * @description Платёжная ссылка (онлайн / для оплаты курьеру). Нет у `unknown`, пока шлюз не ответил.
             */
            paymentUrl?: string;
            refund?: components["schemas"]["PaymentRefund"];
            status: components["schemas"]["PaymentStatus"];
            /**
             * @description Способ на кассе (только `method=pos`).
             * @enum {string}
             */
            tender?: "card" | "cash";
            /** Format: date-time */
            updatedAt: string;
        };
        PaymentConflictProblem: components["schemas"]["Problem"] & {
            /**
             * Format: uuid
             * @description Текущая попытка (`PAYMENT_IN_PROGRESS`, `PAYMENT_STATUS_UNKNOWN`, `ALREADY_PAID`).
             */
            paymentId?: string;
        };
        PaymentInitRequest: {
            method: components["schemas"]["PaymentMethodType"];
            /**
             * Format: uri
             * @description URL возврата после оплаты в форме провайдера (онлайн); схема `http`/`https`.
             */
            returnUrl?: string;
        };
        PaymentMethod: {
            /** @description Можно ли оплатить заказ этим способом прямо сейчас. */
            enabled: boolean;
            title: string;
            type: components["schemas"]["PaymentMethodType"];
        };
        /**
         * @description Онлайн-картой / курьеру (картой по ссылке) / на кассе.
         * @enum {string}
         */
        PaymentMethodType: "online_card" | "courier_card" | "pos";
        /** @description Возврат по оплаченному платежу (заказ отменён). Есть, только если возврат был запрошен. */
        PaymentRefund: {
            /** @description Код причины, если возврат не удался (`failed`). */
            reason?: string;
            /** Format: date-time */
            refundedAt?: string;
            /** Format: date-time */
            requestedAt?: string;
            status: components["schemas"]["RefundStatus"];
        };
        /** @enum {string} */
        PaymentStatus: "pending" | "authorized" | "paid" | "declined" | "unknown";
        PaymentStatusCallback: {
            amount?: components["schemas"]["Money"];
            /**
             * Format: uuid
             * @description Идентификатор уведомления; повтор доставки несёт тот же `eventId`.
             */
            eventId: string;
            /** @description Ссылка на операцию во внешней системе. */
            externalRef?: string;
            /** @description Фискальные данные кассы (вебхук `pos`, `status=paid`). */
            fiscalData?: {
                [key: string]: unknown;
            };
            /** Format: date-time */
            occurredAt?: string;
            /** Format: uuid */
            orderId: string;
            /**
             * Format: uuid
             * @description Наш идентификатор платежа (передаётся шлюзу как reference).
             */
            paymentId: string;
            /** @description Код причины отказа (`status=declined`). */
            reason?: string;
            /**
             * @description Итог операции; промежуточные статусы вебхуком не приходят.
             * @enum {string}
             */
            status: "paid" | "declined";
        };
        PosPaymentInitRequest: {
            /**
             * @description Способ на кассе; наличные также проводятся через терминал (Д6).
             * @enum {string}
             */
            tender: "card" | "cash";
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
        /** @description Заказ не найден или чужой (`ORDER_NOT_FOUND`); также — событие `orders.state` ещё не обработано (в ответе `Retry-After: 1`). */
        OrderNotFound: {
            headers: {
                /** @description Секунды до повтора. */
                "Retry-After"?: number;
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
        /** @description Уникальный ключ (1–128 печатных ASCII-символов), гарантирующий однократное создание платежа при повторной отправке. */
        IdempotencyKeyRequired: string;
        OrderId: string;
        PaymentId: string;
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
    getPaymentMethods: {
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
            /** @description Список способов оплаты */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PaymentMethod"][];
                };
            };
            404: components["responses"]["OrderNotFound"];
        };
    };
    initiatePayment: {
        parameters: {
            query?: never;
            header: {
                /** @description Уникальный ключ (1–128 печатных ASCII-символов), гарантирующий однократное создание платежа при повторной отправке. */
                "Idempotency-Key": components["parameters"]["IdempotencyKeyRequired"];
            };
            path: {
                orderId: components["parameters"]["OrderId"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["PaymentInitRequest"];
            };
        };
        responses: {
            /** @description Платёж создан, оплата в шлюзе инициализирована (`status=pending`) */
            201: {
                headers: {
                    /** @description `true`, если это повтор уже обработанного запроса. */
                    "Idempotent-Replay"?: "true";
                    Location: components["headers"]["Location"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Payment"];
                };
            };
            /** @description Шлюз не ответил в срок: платёж записан со `status=unknown` (E2). Создавать новую попытку нельзя; итог — `getPayment`. */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Payment"];
                };
            };
            400: components["responses"]["BadRequest"];
            /** @description Шлюз синхронно отказал в создании оплаты (`PAYMENT_DECLINED`, E1) — предложить повторить. Отказ банка приходит позже вебхуком (`status=declined`). */
            402: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            404: components["responses"]["OrderNotFound"];
            /** @description `ORDER_NOT_PAYABLE` (заказ отменён или уже принят без онлайн-оплаты), `PAYMENT_IN_PROGRESS`, `PAYMENT_STATUS_UNKNOWN`, `ALREADY_PAID`, `IDEMPOTENCY_IN_PROGRESS`. У `PAYMENT_IN_PROGRESS|PAYMENT_STATUS_UNKNOWN` в расширении `paymentId` — текущая попытка. */
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["PaymentConflictProblem"];
                };
            };
            /** @description `PAYMENT_METHOD_NOT_ALLOWED` (способ не совпадает со способом заказа), `PAYMENT_METHOD_NOT_SUPPORTED` (`courier_card` создаётся системой, `pos` — персоналом), `IDEMPOTENCY_KEY_REUSED` */
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
    getPayment: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                orderId: components["parameters"]["OrderId"];
                paymentId: components["parameters"]["PaymentId"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Текущий статус платежа */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Payment"];
                };
            };
            /** @description Заказ или платёж не найден (`ORDER_NOT_FOUND`, `PAYMENT_NOT_FOUND`) */
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
    initiatePosPayment: {
        parameters: {
            query?: never;
            header: {
                /** @description Уникальный ключ (1–128 печатных ASCII-символов), гарантирующий однократное создание платежа при повторной отправке. */
                "Idempotency-Key": components["parameters"]["IdempotencyKeyRequired"];
            };
            path: {
                orderId: components["parameters"]["OrderId"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["PosPaymentInitRequest"];
            };
        };
        responses: {
            /** @description Оплата на кассе инициирована (`status=pending`) */
            201: {
                headers: {
                    /** @description `true`, если это повтор уже обработанного запроса. */
                    "Idempotent-Replay"?: "true";
                    Location: components["headers"]["Location"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Payment"];
                };
            };
            /** @description Касса не ответила в срок — платёж записан со `status=unknown`, новую попытку создавать нельзя */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Payment"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            /** @description Касса синхронно отказала в операции (`PAYMENT_DECLINED`) — предложить иной способ (E1) */
            402: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            403: components["responses"]["Forbidden"];
            404: components["responses"]["OrderNotFound"];
            /** @description `ORDER_NOT_PAYABLE` (заказ отменён или завершён), `PAYMENT_IN_PROGRESS`, `PAYMENT_STATUS_UNKNOWN`, `ALREADY_PAID`, `IDEMPOTENCY_IN_PROGRESS` */
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["PaymentConflictProblem"];
                };
            };
            /** @description `PAYMENT_METHOD_NOT_ALLOWED` (способ оплаты заказа не `pos`), `IDEMPOTENCY_KEY_REUSED` */
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
    onPaymentGatewayStatus: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["PaymentStatusCallback"];
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
            /** @description Платёж неизвестен (`PAYMENT_NOT_FOUND`) */
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            /** @description `AMOUNT_MISMATCH` — сумма в уведомлении не совпадает с суммой платежа (не применяется, фиксируется метрикой `payment_amount_mismatch_total`) */
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
    onPosPaymentConfirmation: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["PaymentStatusCallback"];
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
            /** @description Платёж неизвестен (`PAYMENT_NOT_FOUND`) */
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/problem+json": components["schemas"]["Problem"];
                };
            };
            /** @description `AMOUNT_MISMATCH` */
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
}
