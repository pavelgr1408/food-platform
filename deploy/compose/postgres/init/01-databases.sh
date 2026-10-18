#!/bin/bash
# БД и роль на сервис (database-per-service). Пароль = имя сервиса — ТОЛЬКО для dev.
# Сервис получает DATABASE_URL=postgres://<svc>:<svc>@postgres:5432/<svc>
set -euo pipefail
for svc in auth restaurant menu orders payment loyalty delivery notification; do
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres <<SQL
CREATE ROLE ${svc} LOGIN PASSWORD '${svc}';
CREATE DATABASE ${svc} OWNER ${svc};
SQL
done
