-- imageUrl ресторана (contracts/openapi/restaurant.yaml 1.3.0): необязательная https-ссылка на фото.
-- NULL — фото нет. Сами файлы сервис не хранит (только URL).
ALTER TABLE restaurants
    ADD COLUMN image_url text,
    ADD CONSTRAINT restaurants_image_url_check CHECK (image_url IS NULL OR (image_url ~ '^https://[^[:space:]]+$' AND length(image_url) <= 2048));
