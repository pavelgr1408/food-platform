-- S6: оплата на кассе (pos) и возвраты.

-- Касса: способ (card|cash) и фискальный документ, как его вернула касса (JSON).
ALTER TABLE payments
    ADD COLUMN tender              text CHECK (tender IN ('card', 'cash')),
    ADD COLUMN fiscal_data         jsonb,
    -- Возврат — отдельный процесс поверх оплаченного платежа (статус платежа остаётся paid).
    ADD COLUMN refund_status       text CHECK (refund_status IN ('pending', 'refunded', 'failed')),
    ADD COLUMN refund_ref          text,
    ADD COLUMN refund_reason       text NOT NULL DEFAULT '',
    ADD COLUMN refund_requested_at timestamptz,
    ADD COLUMN refunded_at         timestamptz,
    ADD COLUMN refund_attempts     integer NOT NULL DEFAULT 0,
    ADD COLUMN refund_next_at      timestamptz;

-- tender обязателен ровно для pos.
ALTER TABLE payments ADD CONSTRAINT payments_tender_pos CHECK ((method = 'pos') = (tender IS NOT NULL));

-- Очередь возвратов (выбирает refund-loop).
CREATE INDEX payments_refund_idx ON payments (refund_next_at) WHERE refund_status = 'pending';

-- «Свип» возвратов: оплаченные платежи отменённых заказов.
CREATE INDEX order_statuses_cancelled_idx ON order_statuses (order_id) WHERE status = 'cancelled';
