-- menu-service: меню на ресторан. Деньги — bigint копейки (ADR-13), float запрещён.
-- version растёт при каждом изменении содержимого (seed сверяет content_hash); на нём построен ETag.
-- Таблицы outbox/processed_events/idempotency_keys этому сервису не нужны (read-only каталог).

CREATE TABLE menus (
    restaurant_id uuid PRIMARY KEY,
    version       bigint      NOT NULL DEFAULT 1 CHECK (version >= 1),
    currency      char(3)     NOT NULL DEFAULT 'RUB',
    content_hash  text        NOT NULL,
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE categories (
    id            uuid PRIMARY KEY,
    restaurant_id uuid    NOT NULL REFERENCES menus (restaurant_id) ON DELETE CASCADE,
    name          text    NOT NULL,
    position      integer NOT NULL
);
CREATE INDEX categories_restaurant_idx ON categories (restaurant_id, position);

CREATE TABLE items (
    id            uuid PRIMARY KEY,
    restaurant_id uuid    NOT NULL REFERENCES menus (restaurant_id) ON DELETE CASCADE,
    category_id   uuid    NOT NULL REFERENCES categories (id) ON DELETE CASCADE,
    name          text    NOT NULL,
    description   text    NOT NULL DEFAULT '',
    price         bigint  NOT NULL CHECK (price >= 0 AND price <= 10000000000),
    available     boolean NOT NULL DEFAULT true,
    position      integer NOT NULL
);
-- validateMenuItems ищет позиции по (restaurant_id, id) — PK по id + фильтр по ресторану
CREATE INDEX items_restaurant_idx ON items (restaurant_id, category_id, position);

CREATE TABLE modifiers (
    id        uuid PRIMARY KEY,
    item_id   uuid    NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    name      text    NOT NULL,
    price     bigint  NOT NULL CHECK (price >= 0 AND price <= 10000000000),
    available boolean NOT NULL DEFAULT true,
    position  integer NOT NULL
);
CREATE INDEX modifiers_item_idx ON modifiers (item_id, position);
