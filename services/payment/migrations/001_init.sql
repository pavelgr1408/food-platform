-- payment-service: доменные таблицы. outbox/processed_events/idempotency_keys создают модули
-- pkg/kafka и pkg/idempotency (см. main.go).

-- Реплика заказа: наполняется консьюмером orders.state (устаревшие revision игнорируются).
CREATE TABLE payment_orders (
    order_id      uuid        PRIMARY KEY,
    revision      integer     NOT NULL CHECK (revision >= 1),
    order_number  text        NOT NULL,
    restaurant_id text        NOT NULL,
    fulfillment   text        NOT NULL CHECK (fulfillment IN ('delivery', 'pickup')),
    method        text        NOT NULL CHECK (method IN ('online_card', 'courier_card', 'pos')),
    amount        bigint      NOT NULL CHECK (amount >= 0),   -- копейки (ADR-13)
    currency      text        NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    owner_user_id text        NOT NULL DEFAULT '',            -- пусто — гость
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Последний известный статус заказа (orders.status). Отдельно от реплики: топики не упорядочены между собой.
CREATE TABLE order_statuses (
    order_id  uuid        PRIMARY KEY,
    status    text        NOT NULL,
    status_at timestamptz NOT NULL
);

-- Попытки оплаты. Платёж пишется ДО вызова шлюза (вызов — вне транзакции).
CREATE TABLE payments (
    id               uuid        PRIMARY KEY,
    order_id         uuid        NOT NULL REFERENCES payment_orders (order_id),
    attempt          integer     NOT NULL CHECK (attempt >= 1),
    method           text        NOT NULL CHECK (method IN ('online_card', 'courier_card', 'pos')),
    status           text        NOT NULL CHECK (status IN ('pending', 'authorized', 'paid', 'declined', 'unknown')),
    amount           bigint      NOT NULL CHECK (amount >= 0),
    currency         text        NOT NULL,
    gateway_ref      text,
    payment_url      text        NOT NULL DEFAULT '',
    return_url       text        NOT NULL DEFAULT '',
    decline_reason   text        NOT NULL DEFAULT '',
    refund_required  boolean     NOT NULL DEFAULT false,      -- деньги получены по отменённому заказу (возврат — S6)
    poll_attempts    integer     NOT NULL DEFAULT 0,
    next_poll_at     timestamptz,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL,
    paid_at          timestamptz,
    UNIQUE (order_id, attempt)
);
-- «Не платим дважды»: не более одной живой попытки и не более одного успешного платежа на заказ.
CREATE UNIQUE INDEX payments_one_live ON payments (order_id) WHERE status IN ('pending', 'authorized', 'unknown');
CREATE UNIQUE INDEX payments_one_paid ON payments (order_id) WHERE status = 'paid';
CREATE UNIQUE INDEX payments_gateway_ref_uq ON payments (gateway_ref) WHERE gateway_ref IS NOT NULL;
CREATE INDEX payments_poll_idx ON payments (next_poll_at) WHERE status IN ('pending', 'unknown');

-- Принятые уведомления (дедупликация по eventId шлюза/кассы).
CREATE TABLE webhook_events (
    event_id    uuid        PRIMARY KEY,
    source      text        NOT NULL,
    payment_id  uuid        NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now()
);
