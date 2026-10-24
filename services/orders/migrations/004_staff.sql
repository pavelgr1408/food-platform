-- S6: персонал, выдача, отмена и возвраты.
--   created_by    — сотрудник, оформивший заказ по факсу (channel = staff_fax); владельца у такого заказа нет (гость);
--   ready_at      — момент перехода в ready: от него считается неполучение самовывоза (not_received);
--   refund_status — ход возврата денег по оплаченному, но не исполняемому заказу (из orders.payments.status).
ALTER TABLE orders ADD COLUMN created_by TEXT;
ALTER TABLE orders ADD COLUMN ready_at TIMESTAMPTZ;
ALTER TABLE orders ADD COLUMN refund_status TEXT CHECK (refund_status IN ('pending', 'refunded', 'failed'));

UPDATE orders o SET ready_at = (SELECT min(h.at) FROM order_status_history h WHERE h.order_id = o.id AND h.to_status = 'ready')
    WHERE o.status = 'ready';

-- таймер неполучения выбирает только готовые заказы на самовывоз
CREATE INDEX orders_ready_idx ON orders (ready_at) WHERE status = 'ready' AND fulfillment_type = 'pickup';
-- поиск по номеру (searchOrder) идёт по UNIQUE(number); для списков ресторана есть orders_restaurant_idx
