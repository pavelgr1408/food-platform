-- S8: акции и скидки.
--   promotions_replica — реплика promotions.discount (loyalty-service): последний снимок акции по версии;
--                        событие с версией не больше сохранённой игнорируется. Завершённые акции остаются
--                        (status='completed'), чтобы запоздалый снимок старой версии их не «оживил».
--   orders.applied_promotions — снимок применённых при оформлении акций: [{"promotionId","title","discount"}],
--                        discount в копейках (ADR-13).
CREATE TABLE promotions_replica (
    promotion_id   UUID        PRIMARY KEY,
    version        INTEGER     NOT NULL CHECK (version >= 1),
    scope          TEXT        NOT NULL CHECK (scope IN ('national', 'local')),
    restaurant_id  UUID,
    title          TEXT        NOT NULL DEFAULT '',
    currency       CHAR(3)     NOT NULL DEFAULT 'RUB',
    discount_type  TEXT        NOT NULL CHECK (discount_type IN ('percent', 'fixed', 'gift')),
    discount_value BIGINT      NOT NULL CHECK (discount_value >= 0),
    priority       INTEGER     NOT NULL DEFAULT 0,
    valid_from     TIMESTAMPTZ NOT NULL,
    valid_to       TIMESTAMPTZ,
    status         TEXT        NOT NULL CHECK (status IN ('active', 'completed')),
    menu_item_ids  UUID[]      NOT NULL DEFAULT '{}',
    min_subtotal   BIGINT      NOT NULL DEFAULT 0 CHECK (min_subtotal >= 0),
    fulfillment    TEXT        NOT NULL DEFAULT '' CHECK (fulfillment IN ('', 'pickup', 'delivery')),
    stackable      BOOLEAN     NOT NULL DEFAULT FALSE,
    received_at    TIMESTAMPTZ NOT NULL,
    CHECK ((scope = 'local') = (restaurant_id IS NOT NULL))
);
-- расчёт читает только действующие акции
CREATE INDEX promotions_replica_active_idx ON promotions_replica (valid_to) WHERE status = 'active';

ALTER TABLE orders ADD COLUMN applied_promotions JSONB NOT NULL DEFAULT '[]'::jsonb;
