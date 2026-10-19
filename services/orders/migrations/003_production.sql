-- S4: производство и жизненный цикл заказа.
--   accepted_at           — момент принятия заказа (оплата подтверждена или предоплаты нет); от него считается ETA
--                           и «возраст» заказа для метрики orders_stuck_in_accepted_seconds;
--   production_updated_at — updatedAt последнего применённого снимка orders.dish.status (защита от переупорядочивания);
--   production_lead_time_replica — последняя известная загрузка производства по ресторанам (production.lead.time).
ALTER TABLE orders ADD COLUMN accepted_at TIMESTAMPTZ;
ALTER TABLE orders ADD COLUMN production_updated_at TIMESTAMPTZ;

UPDATE orders o SET accepted_at = COALESCE(
        (SELECT min(h.at) FROM order_status_history h WHERE h.order_id = o.id AND h.to_status = 'accepted'), o.created_at)
    WHERE o.status <> 'pending_payment' AND NOT (o.status = 'cancelled' AND NOT EXISTS (
        SELECT 1 FROM order_status_history h WHERE h.order_id = o.id AND h.to_status = 'accepted'));

-- метрика и поиск «зависших» заказов выбирают только принятые
CREATE INDEX orders_accepted_idx ON orders (accepted_at) WHERE status = 'accepted';

CREATE TABLE production_lead_time_replica (
    restaurant_id     UUID             PRIMARY KEY,
    lead_time_minutes INT              NOT NULL CHECK (lead_time_minutes >= 0),
    current_load      DOUBLE PRECISION NOT NULL DEFAULT 0,
    measured_at       TIMESTAMPTZ      NOT NULL,   -- время измерения у производства
    received_at       TIMESTAMPTZ      NOT NULL DEFAULT now()
);
