-- delivery-service: доменные таблицы. Outbox/inbox создаёт модуль pkg/kafka (см. main.go).
-- Деньги — bigint копейки (ADR-13). Время — timestamptz (UTC).

-- Доставка одного заказа. Источник — orders.state (заказ с доставкой, оплаченный или с оплатой курьеру).
CREATE TABLE deliveries (
    id               uuid PRIMARY KEY,
    order_id         uuid        NOT NULL UNIQUE,
    order_number     text        NOT NULL,
    restaurant_id    uuid        NOT NULL,
    owner_user_id    text        NOT NULL DEFAULT '',          -- '' — гость (владеет по знанию UUID)
    revision         integer     NOT NULL CHECK (revision >= 1), -- последняя применённая revision orders.state
    status           text        NOT NULL CHECK (status IN ('searching','assigned','picked_up','en_route','delivered','failed')),
    fail_reason      text        NOT NULL DEFAULT '' CHECK (fail_reason IN ('','no_courier','undelivered','cancelled')),
    address          text        NOT NULL DEFAULT '',
    dest_lat         double precision NOT NULL CHECK (dest_lat BETWEEN -90 AND 90),
    dest_lon         double precision NOT NULL CHECK (dest_lon BETWEEN -180 AND 180),
    contact_name     text        NOT NULL DEFAULT '',
    contact_phone    text        NOT NULL DEFAULT '',
    amount           bigint      NOT NULL CHECK (amount >= 0),
    currency         text        NOT NULL DEFAULT 'RUB',
    pay_on_delivery  boolean     NOT NULL DEFAULT false,        -- оплата курьеру (courier_card, UC-PAY-03)
    payment_url      text        NOT NULL DEFAULT '',           -- из orders.payments.link
    link_pushed      boolean     NOT NULL DEFAULT false,        -- ссылка передана курьерской системе
    courier_ref      text        NOT NULL DEFAULT '',           -- id доставки у курьерской системы ('' — ещё не запрошена)
    courier_id       text        NOT NULL DEFAULT '',
    courier_name     text        NOT NULL DEFAULT '',
    courier_phone    text        NOT NULL DEFAULT '',
    eta_at           timestamptz,
    paid_reported    boolean     NOT NULL DEFAULT false,        -- курьерская система сообщила об оплате (информативно)
    cancelled        boolean     NOT NULL DEFAULT false,        -- заказ отменён: доставку снимаем
    search_started_at timestamptz NOT NULL,
    next_poll_at     timestamptz,                               -- NULL — опрос не нужен (терминальный статус)
    poll_attempts    integer     NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL
);
CREATE INDEX deliveries_poll_idx ON deliveries (next_poll_at) WHERE next_poll_at IS NOT NULL;
CREATE INDEX deliveries_restaurant_idx ON deliveries (restaurant_id, created_at DESC);

-- Журнал статусов доставки (MG3: всё журналируется).
CREATE TABLE delivery_history (
    id          bigserial PRIMARY KEY,
    delivery_id uuid        NOT NULL REFERENCES deliveries (id) ON DELETE CASCADE,
    status      text        NOT NULL,
    reason      text        NOT NULL DEFAULT '',
    source      text        NOT NULL,                           -- state | webhook | poll | timeout | cancel
    at          timestamptz NOT NULL
);
CREATE INDEX delivery_history_idx ON delivery_history (delivery_id, id);

-- Вебхуки курьерской системы: дедупликация повторной доставки по eventId.
CREATE TABLE webhook_events (
    event_id    text PRIMARY KEY,
    delivery_id uuid        NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now()
);

-- Платёжные ссылки, пришедшие раньше заказа (топики не упорядочены между собой).
CREATE TABLE payment_links (
    order_id    uuid PRIMARY KEY,
    url         text        NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now()
);

-- Заказы, отменённые раньше, чем пришло orders.state (редкий порядок событий): доставку не создаём.
CREATE TABLE cancelled_orders (
    order_id    uuid PRIMARY KEY,
    cancelled_at timestamptz NOT NULL DEFAULT now()
);
