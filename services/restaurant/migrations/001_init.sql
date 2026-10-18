-- restaurant-service: рестораны, режим работы, исключения по датам.
-- Время работы — минуты от полуночи в часовом поясе ресторана (closes_min <= opens_min — ночной режим).
-- Таблицы outbox/processed_events/idempotency_keys этому сервису не нужны (read-only каталог).

CREATE TABLE restaurants (
    id                uuid PRIMARY KEY,
    name              text             NOT NULL,
    city              text             NOT NULL,
    address           text             NOT NULL,
    lat               double precision NOT NULL CHECK (lat BETWEEN -90 AND 90),
    lon               double precision NOT NULL CHECK (lon BETWEEN -180 AND 180),
    timezone          text             NOT NULL,
    delivery_enabled  boolean          NOT NULL DEFAULT false,
    delivery_radius_m integer          NOT NULL DEFAULT 0 CHECK (delivery_radius_m >= 0),
    active            boolean          NOT NULL DEFAULT true,
    created_at        timestamptz      NOT NULL DEFAULT now(),
    updated_at        timestamptz      NOT NULL DEFAULT now(),
    CHECK (delivery_enabled OR delivery_radius_m = 0)
);

-- Bounding-box prefilter поиска по радиусу; PostGIS сознательно не используется (см. README).
CREATE INDEX restaurants_geo_idx ON restaurants (lat, lon) WHERE active;

CREATE TABLE working_hours (
    restaurant_id uuid     NOT NULL REFERENCES restaurants (id) ON DELETE CASCADE,
    day_of_week   smallint NOT NULL CHECK (day_of_week BETWEEN 1 AND 7),
    opens_min     smallint NOT NULL CHECK (opens_min BETWEEN 0 AND 1439),
    closes_min    smallint NOT NULL CHECK (closes_min BETWEEN 1 AND 1440),
    PRIMARY KEY (restaurant_id, day_of_week, opens_min),
    CHECK (opens_min <> closes_min)
);

CREATE TABLE schedule_exceptions (
    id            bigserial PRIMARY KEY,
    restaurant_id uuid     NOT NULL REFERENCES restaurants (id) ON DELETE CASCADE,
    on_date       date     NOT NULL,
    opens_min     smallint CHECK (opens_min BETWEEN 0 AND 1439),
    closes_min    smallint CHECK (closes_min BETWEEN 1 AND 1440),
    note          text     NOT NULL DEFAULT '',
    -- закрытый день: оба NULL; рабочий интервал: оба заданы
    CHECK ((opens_min IS NULL) = (closes_min IS NULL)),
    CHECK (opens_min IS NULL OR opens_min <> closes_min)
);
CREATE INDEX schedule_exceptions_idx ON schedule_exceptions (restaurant_id, on_date);
