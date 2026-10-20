-- S5: целевые роли платформы и привязка сотрудников к ресторанам.
-- ADMIN → HQ_STAFF; COURIER и KITCHEN — внешние системы, ролей больше нет.
ALTER TABLE user_roles DROP CONSTRAINT chk_user_roles_role;
UPDATE user_roles SET role = 'HQ_STAFF' WHERE role = 'ADMIN';
DELETE FROM user_roles WHERE role IN ('COURIER', 'KITCHEN');
-- пользователь, у которого остались только удалённые роли, становится клиентом (токен требует хотя бы одну роль)
INSERT INTO user_roles(user_id, role)
SELECT u.id, 'CUSTOMER' FROM users u
WHERE NOT EXISTS (SELECT 1 FROM user_roles r WHERE r.user_id = u.id);
ALTER TABLE user_roles ADD CONSTRAINT chk_user_roles_role
    CHECK (role IN ('CUSTOMER', 'STAFF', 'FRANCHISE_OWNER', 'HQ_STAFF'));

-- Привязка к ресторану. ресторан живёт в restaurant-db, поэтому FK на него нет (база-на-сервис).
CREATE TABLE user_restaurants (
    user_id       UUID        NOT NULL,
    restaurant_id UUID        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_user_restaurants PRIMARY KEY (user_id, restaurant_id),
    CONSTRAINT fk_user_restaurants_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
);
CREATE INDEX ix_user_restaurants_restaurant ON user_restaurants (restaurant_id);
