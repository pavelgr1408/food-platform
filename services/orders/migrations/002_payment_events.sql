-- S3: orders потребляет orders.payments.status. Время последнего применённого события оплаты
-- защищает от переупорядочивания (события одного заказа упорядочены ключом партиции, но реплик relay может быть несколько).
ALTER TABLE orders ADD COLUMN payment_updated_at TIMESTAMPTZ;
