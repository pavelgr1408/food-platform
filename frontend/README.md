# frontend/ — клиенты «Лоток» (S10, ADR-5/12/14/15)

Монорепо npm workspaces. Источник требований — `claude/FRONTEND-CONTRACT.md` (юзкейсы `UC-*`), визуальный язык — `claude/DESIGN-SYSTEM.md`, правда по форматам — `contracts/openapi/`.

| Путь | Что |
|---|---|
| `apps/web-app` | SPA гостя и покупателя (React 19 + TS + Vite + Tailwind v4 + TanStack Query + React Router) |
| `apps/web-app-staff` | клиент персонала (:3001): касса (очередь, поиск по номеру, оплата на кассе, выдача, отмена), заказ по факсу, управление акциями |
| `packages/ui` | общие компоненты, форматирование, тексты ошибок, карта (`@fp/ui`, `@fp/ui/map`) |
| `packages/mocks` | мок-бэкенд MSW на `contracts/openapi`: гость/покупатель и персонал (`@fp/mocks`, `@fp/mocks/browser`) |
| `packages/api` | типы из OpenAPI (`src/generated`, **не править**), клиент: `problem+json`, single-flight refresh, `Idempotency-Key` |
| `packages/shared` | деньги (int64 копейки → «549,90 ₽», одна функция), телефон E.164, пароль по байтам |
| `packages/design-tokens` | `tokens.css` / `tokens.json` («Лоток», `--lt-*`) |

Вход в `web-app-staff` (mock и seed auth): `+79990000010` STAFF (касса, факс) · `+79990000011` FRANCHISE_OWNER (+ акции своего ресторана) · `+79990000012` HQ_STAFF (вся сеть), пароль `secret1`.
Запуск: `make web-staff-dev-mock` (моки) или `make web-staff-dev` (gateway :8080); e2e — `make web-staff-e2e`; срез — `make up SLICE="… gateway web-app-staff"` (порт 3001).

## Быстрый старт

```
cd frontend
npm ci
npm run dev:mock --workspace web-app
```

Открыть http://localhost:3000. Это режим `mock`: MSW в браузере отвечает по семантике OpenAPI, бэкенд не нужен. Оплата — на мок-странице шлюза; из консоли браузера доступны `__mock.db.priceChangeOnNextOrder = true` (следующий заказ получит 409 `PRICE_CHANGED`) и `__mock.reset()`.

## Режимы

| `VITE_API_MODE` | Что | Когда |
|---|---|---|
| `mock` | MSW по OpenAPI, состояние в localStorage | разработка экранов, демо, e2e |
| `real` (по умолчанию) | запросы на `VITE_API_BASE_URL` (`/api` → dev-proxy Vite → `GATEWAY_URL`, по умолчанию `http://localhost:8080`) | работа со стендом |

Браузер ходит на `/api/*` того же origin, поэтому CORS не нужен; SPA-маршруты (`/orders/:id`) не конфликтуют с API. Прямой доступ к gateway (`VITE_API_BASE_URL=http://localhost:8080`) требует `CORS_ALLOWED_ORIGINS=http://localhost:3000` и (для удаления токена устройства) `DELETE` в gateway — ветка `2026-11-06-fix-gateway-cors-delete`.

`VITE_PAYMENT_MODE`: `redirect` (по умолчанию) — уход на `paymentUrl` и возврат по `returnUrl`; `poll` — остаться на странице заказа. На локальном стенде нужен `poll`: страница `/pay/{id}` у `mock-externals` отдаёт JSON без возврата, итог приходит вебхуком за ~2 с. Исход мока задаётся суммой: заказ с копейками `…13` — отказ банка, `…99` — «тишина» (итог только сверкой).

## Подключение к бэкенду

Вариант 1 — Vite на хосте, бэкенд в Docker (удобно для разработки):

```
cd ~/food-platform
AUTH_MODE=jwks make up SLICE="auth restaurant menu orders payment delivery loyalty notification mock-externals gateway"
cd frontend && npm ci
cp apps/web-app/.env.example apps/web-app/.env.local
npm run dev
```

Вариант 2 — всё в Docker (nginx отдаёт SPA и проксирует `/api/` на gateway):

```
AUTH_MODE=jwks make up SLICE="auth restaurant menu orders payment delivery loyalty notification mock-externals gateway web-app"
```

Открыть http://localhost:3000. Чтобы пройти путь покупателя нужны рестораны с меню: `restaurant` и `menu` засеваются при старте (`SEED_ON_START`).

Проверка связки: `curl -s localhost:3000/api/restaurants | head -c 300` — должен вернуться JSON со списком ресторанов.

## Проверки

```
make web-check     # типы из OpenAPI не разошлись + typecheck + lint + unit + build
make web-e2e       # Playwright (mobile + desktop) против сборки в режиме mock
make web-gen       # перегенерировать src/generated после изменения contracts/openapi
```

## Файл go.mod здесь не случайный

`frontend/go.mod` — заглушка: отделяет каталог от Go-модуля репозитория, иначе `go vet ./...` заходит в `node_modules` (там есть `.go`-файлы сторонних пакетов).
