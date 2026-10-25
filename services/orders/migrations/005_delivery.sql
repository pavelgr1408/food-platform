-- S7: доставка.
--   delivery_*  — снимок статуса доставки из orders.delivery.status (delivery-service). Применяется по
--                 delivery_updated_at: событие не новее сохранённого игнорируется. Снимок хранится и тогда, когда
--                 заказ ещё готовится: переход ready → on_delivery выполняется в момент готовности.
ALTER TABLE orders ADD COLUMN delivery_status TEXT CHECK (delivery_status IN
    ('searching', 'assigned', 'picked_up', 'en_route', 'delivered', 'failed'));
ALTER TABLE orders ADD COLUMN delivery_fail_reason TEXT CHECK (delivery_fail_reason IN ('no_courier', 'undelivered', 'cancelled'));
ALTER TABLE orders ADD COLUMN delivery_eta_at TIMESTAMPTZ;
ALTER TABLE orders ADD COLUMN delivery_updated_at TIMESTAMPTZ;

-- таймер неполучения выбирает готовые заказы, которые клиенту предложено забрать самому (failed/no_courier)
CREATE INDEX orders_ready_pickup_fallback_idx ON orders (ready_at)
    WHERE status = 'ready' AND fulfillment_type = 'delivery' AND delivery_status = 'failed';
