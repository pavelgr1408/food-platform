-- orders-service: корзины, заказы, история статусов, нумерация.
-- Деньги — bigint копейки (ADR-13). Таблицы outbox / processed_events / idempotency_keys
-- создаются модулями pkg/kafka и pkg/idempotency (см. main.go).

CREATE TABLE carts (
    id               UUID PRIMARY KEY,
    restaurant_id    UUID        NOT NULL,
    owner_user_id    TEXT,                                  -- NULL: гость (владеет тот, кто знает id)
    status           TEXT        NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'ordered')),
    order_id         UUID,
    revision         BIGINT      NOT NULL DEFAULT 1,
    currency         CHAR(3)     NOT NULL DEFAULT 'RUB',
    fulfillment_type TEXT        CHECK (fulfillment_type IN ('delivery', 'pickup')),
    address_raw      TEXT        NOT NULL DEFAULT '',
    address_lat      DOUBLE PRECISION,
    address_lon      DOUBLE PRECISION,
    contact_name     TEXT        NOT NULL DEFAULT '',
    contact_phone    TEXT        NOT NULL DEFAULT '',
    contact_email    TEXT        NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((status = 'ordered') = (order_id IS NOT NULL))
);
-- сборка заброшенных корзин
CREATE INDEX carts_stale_idx ON carts (updated_at) WHERE status = 'open';

CREATE TABLE cart_items (
    cart_id      UUID     NOT NULL REFERENCES carts (id) ON DELETE CASCADE,
    position     SMALLINT NOT NULL,
    menu_item_id UUID     NOT NULL,
    name         TEXT     NOT NULL,
    quantity     INT      NOT NULL CHECK (quantity BETWEEN 1 AND 99),
    modifier_ids UUID[]   NOT NULL DEFAULT '{}',
    unit_price   BIGINT   NOT NULL CHECK (unit_price >= 0),
    line_total   BIGINT   NOT NULL CHECK (line_total >= 0),
    PRIMARY KEY (cart_id, position)
);

CREATE TABLE orders (
    id               UUID PRIMARY KEY,
    number           TEXT        NOT NULL UNIQUE,
    cart_id          UUID        NOT NULL UNIQUE,           -- одна корзина — один заказ (защита на уровне БД)
    restaurant_id    UUID        NOT NULL,
    owner_user_id    TEXT,
    channel          TEXT        NOT NULL DEFAULT 'online' CHECK (channel IN ('online', 'staff_fax')),
    status           TEXT        NOT NULL CHECK (status IN ('pending_payment', 'accepted', 'cooking', 'ready',
                                 'on_delivery', 'delivered', 'handed_over', 'closed', 'not_received', 'cancelled')),
    status_reason    TEXT        NOT NULL DEFAULT '',
    payment_method   TEXT        NOT NULL CHECK (payment_method IN ('online_card', 'courier_card', 'pos')),
    payment_status   TEXT        NOT NULL DEFAULT 'pending' CHECK (payment_status IN ('pending', 'authorized', 'paid', 'declined', 'unknown')),
    fulfillment_type TEXT        NOT NULL CHECK (fulfillment_type IN ('delivery', 'pickup')),
    address_raw      TEXT        NOT NULL DEFAULT '',
    address_lat      DOUBLE PRECISION,
    address_lon      DOUBLE PRECISION,
    contact_name     TEXT        NOT NULL DEFAULT '',
    contact_phone    TEXT        NOT NULL DEFAULT '',
    contact_email    TEXT        NOT NULL DEFAULT '',
    pickup_from      TIMESTAMPTZ,
    pickup_to        TIMESTAMPTZ,
    currency         CHAR(3)     NOT NULL,
    subtotal         BIGINT      NOT NULL CHECK (subtotal >= 0),
    discount_total   BIGINT      NOT NULL DEFAULT 0 CHECK (discount_total >= 0),
    total            BIGINT      NOT NULL CHECK (total >= 0),
    revision         INT         NOT NULL DEFAULT 1,        -- версия orders.state (ADR-10)
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (total = subtotal - discount_total),
    CHECK ((pickup_from IS NULL) = (pickup_to IS NULL))
);
CREATE INDEX orders_restaurant_idx ON orders (restaurant_id, created_at DESC);
CREATE INDEX orders_owner_idx ON orders (owner_user_id, created_at DESC) WHERE owner_user_id IS NOT NULL;
-- таймаут неоплаты выбирает только эти строки
CREATE INDEX orders_unpaid_idx ON orders (created_at) WHERE status = 'pending_payment';

CREATE TABLE order_items (
    order_id     UUID     NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    position     SMALLINT NOT NULL,
    menu_item_id UUID     NOT NULL,
    name         TEXT     NOT NULL,
    quantity     INT      NOT NULL CHECK (quantity BETWEEN 1 AND 99),
    modifier_ids UUID[]   NOT NULL DEFAULT '{}',
    unit_price   BIGINT   NOT NULL CHECK (unit_price >= 0),
    line_total   BIGINT   NOT NULL CHECK (line_total >= 0),
    PRIMARY KEY (order_id, position)
);

-- MG3: каждый переход статуса журналируется.
CREATE TABLE order_status_history (
    order_id    UUID        NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    seq         INT         NOT NULL,
    from_status TEXT        NOT NULL DEFAULT '',
    to_status   TEXT        NOT NULL,
    reason      TEXT        NOT NULL DEFAULT '',
    actor       TEXT        NOT NULL,
    at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (order_id, seq)
);

-- Нумерация: короткий код ресторана (глобально уникален) + счётчик на день.
CREATE TABLE restaurant_codes (
    restaurant_id UUID PRIMARY KEY,
    code          INT GENERATED ALWAYS AS IDENTITY UNIQUE
);
CREATE TABLE order_counters (
    restaurant_id UUID NOT NULL,
    day           DATE NOT NULL,
    last_seq      INT  NOT NULL,
    PRIMARY KEY (restaurant_id, day)
);
