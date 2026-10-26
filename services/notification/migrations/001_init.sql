-- notification-service: токены устройств и очередь уведомлений.
-- Таблица processed_events (inbox) создаётся модулем pkg/kafka (см. main.go).

CREATE TABLE device_tokens (
    id                  uuid        PRIMARY KEY,
    user_id             text        NOT NULL CHECK (user_id <> ''),
    platform            text        NOT NULL CHECK (platform IN ('apns', 'fcm', 'huawei')),
    token               text        NOT NULL CHECK (length(token) BETWEEN 1 AND 4096),
    permissions_granted boolean     NOT NULL DEFAULT true,
    locale              text        NOT NULL DEFAULT 'ru' CHECK (locale IN ('ru', 'en')),
    registered_at       timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL,
    UNIQUE (platform, token)
);
CREATE INDEX device_tokens_user_idx ON device_tokens (user_id, registered_at DESC, id);

-- Очередь: строка создаётся консьюмером orders.status в транзакции inbox (по одной на устройство),
-- отправляет её отдельный процесс вне транзакции. UNIQUE (order_id, type, token_id) — защита от дублей:
-- повтор события, повторная обработка и параллельные консьюмеры не дают второго push.
CREATE TABLE notifications (
    id              uuid        PRIMARY KEY,
    order_id        uuid        NOT NULL,
    order_number    text        NOT NULL,
    type            text        NOT NULL,
    reason          text        NOT NULL DEFAULT '',
    priority        text        NOT NULL CHECK (priority IN ('normal', 'high')),
    title           text        NOT NULL,
    body            text        NOT NULL,
    token_id        uuid        NOT NULL REFERENCES device_tokens (id) ON DELETE CASCADE,
    status          text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed', 'skipped')),
    attempts        integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL,
    last_error      text        NOT NULL DEFAULT '',
    event_at        timestamptz NOT NULL,
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,
    sent_at         timestamptz,
    UNIQUE (order_id, type, token_id)
);
CREATE INDEX notifications_due_idx ON notifications (next_attempt_at) WHERE status = 'pending';
CREATE INDEX notifications_done_idx ON notifications (updated_at) WHERE status <> 'pending';
