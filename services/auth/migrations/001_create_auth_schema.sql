CREATE TABLE users (
    id UUID PRIMARY KEY,
    phone VARCHAR(32) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    last_login_at TIMESTAMPTZ NULL,
    CONSTRAINT chk_users_status CHECK (status IN ('ACTIVE','BLOCKED','DELETED','PENDING_VERIFICATION'))
);
CREATE UNIQUE INDEX ux_users_phone ON users(phone);
CREATE TABLE user_roles (
    user_id UUID NOT NULL,
    role VARCHAR(64) NOT NULL,
    CONSTRAINT pk_user_roles PRIMARY KEY(user_id,role),
    CONSTRAINT fk_user_roles_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT chk_user_roles_role CHECK (role IN ('CUSTOMER','ADMIN','COURIER','KITCHEN'))
);
CREATE TABLE refresh_tokens (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    token_hash VARCHAR(255) NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL,
    device_id UUID NULL,
    CONSTRAINT fk_refresh_tokens_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX ix_refresh_tokens_user_id ON refresh_tokens(user_id);
CREATE UNIQUE INDEX ux_refresh_tokens_token_hash ON refresh_tokens(token_hash);
CREATE INDEX ix_refresh_tokens_expires_at ON refresh_tokens(expires_at);
CREATE TABLE user_devices (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    platform VARCHAR(32) NOT NULL,
    push_token TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT fk_user_devices_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT chk_user_devices_platform CHECK (platform IN ('WEB','IOS','ANDROID'))
);
CREATE INDEX ix_user_devices_user_id ON user_devices(user_id);
