-- Хранилище ключей идемпотентности (Idempotency-Key): ключ → (хэш запроса, ответ).
CREATE TABLE idempotency_keys (
    scope            TEXT        NOT NULL,           -- маршрут + владелец (чтобы ключи разных клиентов не пересекались)
    key              TEXT        NOT NULL,
    request_hash     BYTEA       NOT NULL,           -- sha256(метод, путь, query, тело)
    state            TEXT        NOT NULL CHECK (state IN ('in_progress', 'completed')),
    response_status  INT,
    response_headers JSONB,
    response_body    BYTEA,
    locked_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (scope, key)
);
CREATE INDEX idempotency_keys_expires_idx ON idempotency_keys (expires_at);
