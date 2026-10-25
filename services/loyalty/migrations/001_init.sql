-- loyalty-service: акции и история изменений. Таблицы outbox и idempotency_keys создают модули pkg/kafka и pkg/idempotency.
-- Деньги — bigint копейки (ADR-13); процент хранится в сотых долях процента (1500 = 15 %).
CREATE TABLE promotions (
    id             uuid        PRIMARY KEY,
    title          text        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120),
    description    text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 1000),
    scope          text        NOT NULL CHECK (scope IN ('local', 'national')),
    restaurant_id  uuid,
    status         text        NOT NULL CHECK (status IN ('draft', 'active', 'completed')),
    priority       integer     NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 1000),
    starts_at      timestamptz NOT NULL,
    ends_at        timestamptz NOT NULL,
    currency       char(3)     NOT NULL DEFAULT 'RUB',
    discount_type  text        NOT NULL CHECK (discount_type IN ('percent', 'fixed')),
    discount_value bigint      NOT NULL CHECK (discount_value > 0 AND discount_value <= 10000000000),
    conditions     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    version        integer     NOT NULL CHECK (version >= 1),
    created_by     text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL,
    CHECK (ends_at > starts_at),
    CHECK ((scope = 'local') = (restaurant_id IS NOT NULL)),
    CHECK (discount_type <> 'percent' OR discount_value <= 10000)
);
-- выборка действующих акций (listActivePromotions, проверка конфликтов)
CREATE INDEX promotions_active_idx ON promotions (ends_at, restaurant_id) WHERE status = 'active';

CREATE TABLE promotion_history (
    promotion_id uuid        NOT NULL REFERENCES promotions (id),
    version      integer     NOT NULL,
    action       text        NOT NULL CHECK (action IN ('created', 'updated', 'activated', 'completed')),
    actor_id     text        NOT NULL DEFAULT '',
    occurred_at  timestamptz NOT NULL,
    snapshot     jsonb       NOT NULL,
    PRIMARY KEY (promotion_id, version)
);
