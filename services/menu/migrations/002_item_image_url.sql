-- imageUrl позиции меню (contracts/openapi/menu.yaml 1.4.0): необязательная https-ссылка на фото.
-- NULL — фото нет. Сами файлы сервис не хранит (только URL), CSP фронта ограничивает хост img-src.
ALTER TABLE items
    ADD COLUMN image_url text,
    ADD CONSTRAINT items_image_url_check CHECK (image_url IS NULL OR (image_url ~ '^https://[^[:space:]]+$' AND length(image_url) <= 2048));
