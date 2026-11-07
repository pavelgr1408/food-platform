-- Долг №81: снимок модификаторов в строке корзины и заказа.
--   modifiers — [{"id","name","price"}], price в копейках (ADR-13); порядок и состав как у modifier_ids.
--   NULL — строка создана до миграции: снимка нет, названия клиент берёт из меню (восстановить их
--   задним числом нельзя). Новые строки всегда с массивом (пустой '[]', если модификаторов нет).
ALTER TABLE cart_items  ADD COLUMN modifiers JSONB CHECK (modifiers IS NULL OR jsonb_typeof(modifiers) = 'array');
ALTER TABLE order_items ADD COLUMN modifiers JSONB CHECK (modifiers IS NULL OR jsonb_typeof(modifiers) = 'array');
