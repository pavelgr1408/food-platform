# Dev-стенд и работа срезами

Железо: MacBook Air M3 / 16 ГБ, Docker Desktop (лимит VM ≈ 10 ГБ / 6 CPU). Поднимаем **только** то, что нужно текущей задаче.

## Инфраструктура
| Компонент | Образ | Порт на хосте | Память |
|---|---|---|---|
| PostgreSQL 17 (БД и роль на сервис: `auth`, `restaurant`, `menu`, `orders`, `payment`, `loyalty`, `delivery`, `notification`; пароль = имя, **только dev**) | `postgres:17-alpine` | 5432 | ≈150 МБ |
| Kafka 3.9 KRaft, 1 брокер, автосоздание топиков выключено | `apache/kafka:3.9.0` | 29092 (с хоста), `kafka:9092` (внутри) | ≈700 МБ (heap 512m) |
| Redis (с S7) | `redis:7-alpine` | 6379 | ≈20 МБ — `COMPOSE_PROFILES=redis make up` |

`kafka-init` создаёт топики из `deploy/compose/kafka/topics.txt` — файл **генерируется** из AsyncAPI (`make topics`), CI проверяет актуальность.
Для каждого топика есть `<topic>.dlq`.

## Срезы
`make up SLICE="a b"` добавляет `deploy/compose/services/<a>.yml` и `<b>.yml`. Файл среза создаёт `make new-service`.
Бюджет по плану: S1 ≈ 300 МБ сверх инфраструктуры, S2 ≈ 2.2 ГБ всего, … полный набор — только в облаке.

Быстрый цикл активного сервиса — `go run` на хосте при поднятой инфраструктуре:
```bash
make up
DATABASE_URL='postgres://orders:orders@localhost:5432/orders?sslmode=disable' KAFKA_BROKERS=localhost:29092 \
  go run ./services/orders/cmd/orders-service
```
`go run` с `--migrate-only` применяет миграции и выходит (в k8s это pre-upgrade Job).

## Тесты
- `make test` — быстрые; интеграционные пропускаются без `TEST_DATABASE_URL`.
- `make test-int` — с Postgres из `make up` (временные БД `test_xxxxxxxx` создаются и удаляются автоматически).
- Kafka в тестах — in-memory брокер `kfake` (без Docker); с настоящим брокером — в Postman/e2e-срезах.

## Что нельзя
- Коммитить `.env`, `*.pem`, пароли и токены. Новые ключи — только через `make keys`. gitleaks в CI.
- Менять настройки Docker Desktop (кроме лимитов ресурсов).

## Каталог (S1): restaurant + menu
```bash
make up SLICE="restaurant menu"
make newman SERVICE=restaurant
make newman SERVICE=menu
make newman SERVICE=catalog
make contract-check
```
`SEED_ON_START=true` в срезе загружает демо-рестораны и меню (идемпотентно). `make contract-check` валидирует живые
ответы обоих сервисов против `contracts/openapi/*.yaml`.

Без Docker (так же работает CI-job `contract-check`): `make contract-check-ci` — сам собирает restaurant, menu и orders, создаёт БД в локальном Postgres (`DB_ADMIN_URL`, по умолчанию `postgres://postgres:postgres@localhost:5432/postgres`), запускает сервисы и прогоняет сценарии `catalog` и `orders`; нужен `psql` и `make py-deps`.

## Ядро заказа (S2): orders
```bash
make up SLICE="restaurant menu orders"
make newman SERVICE=orders
make contract-check SCENARIO=orders
```
`orders` вызывает `menu` (`validateMenuItems`) и `restaurant` по HTTP (`MENU_URL`, `RESTAURANT_URL`; в срезе — имена сервисов compose),
события `orders.state` / `orders.status` уходят в Kafka через outbox. Сценарий Postman идёт по ресторану №4 (Невский, 24/7), поэтому
не зависит от времени суток. Событие в Kafka (нужен срез с Kafka):
```bash
docker compose --project-directory . -f deploy/compose/infra.yml exec kafka /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server kafka:9092 --topic orders.state --from-beginning --property print.key=true --property print.headers=true --timeout-ms 5000
```

## Деньги (S3): payment
```bash
make up SLICE="restaurant menu orders payment mock-externals"
make newman SERVICE=payment
make contract-check SCENARIO=payment
```
`payment` получает заказы из Kafka (`orders.state`) и ходит в платёжный шлюз `GATEWAY_URL` — в срезе это `mock-externals` (:9091, модуль `payment`),
который по умолчанию подтверждает оплату вебхуком через `MOCK_DELAY` (2 с). Сценарии Postman «отказ», «таймаут → unknown», «потерянный вебхук» сами
переключают режимы мока через `/_admin/gateway/*` и возвращают их обратно. Коллекцию можно гонять повторно без пересоздания среза.
Ручная проверка двойного списания: `curl localhost:9091/_inspect/payments` — на заказ ровно одна операция шлюза.

## Производство и сквозной MVP (S4)
```bash
make up SLICE="restaurant menu orders payment mock-externals"
make newman SERVICE=e2e
```
`mock-externals` (по умолчанию `MOCKS=ping,payment,pos,production,courier`) содержит модуль `production` (:9092): читает `orders.state` (берёт заказ в работу, только если
`paymentStatus=paid` или оплата не предоплатная — ADR-10), публикует `orders.dish.status` (`queued → cooking → ready`) и `production.lead.time`.
Тайминги — `MOCK_PRODUCTION_QUEUE_DELAY` (2s), `MOCK_PRODUCTION_COOK_TIME` (3s) или на лету: `POST :9092/_admin/production/timing?queue=1s&cook=2s`.
Управление сценариями: `POST /_admin/production/mode?queue=off|on` («авария очереди», E3: заказы остаются `accepted`, растёт метрика
`orders_stuck_in_accepted_seconds`), `POST /_admin/production/leadtime?restaurantId=<id>&minutes=<n>` (зафиксировать время приготовления ресторана),
`POST /_admin/production/reset`, `GET /_inspect/production`. Коллекция `postman/e2e/` сама задаёт тайминги и возвращает настройки в конце.
Заказы без `production` в срезе остаются в `accepted`: orders честно показывает типовой ETA (`approximate=true`).

## Доставка курьером и оплата курьеру (S7)
```bash
COMPOSE_PROFILES=redis REDIS_ADDR=redis:6379 make up SLICE="restaurant menu orders payment delivery mock-externals"
make newman SERVICE=delivery        # delivery напрямую: probes, 404, подпись вебхуков
make newman SERVICE=e2e             # разделы 8–10: онлайн-оплата → курьер → координаты → delivered; оплата курьеру; E2
make contract-check SCENARIO=delivery
```
- `delivery-service` берёт заказ с доставкой из `orders.state`, когда он оплачен или оплата не предоплатная (ADR-10); курьер ищется параллельно с приготовлением.
  Координаты курьера пишутся в Redis (`REDIS_ADDR`, TTL `LOCATION_TTL`=60 с); без `REDIS_ADDR` — в память процесса (только dev).
- `mock-externals` получает модуль `courier` (:9094): `assigned → picked_up → en_route → delivered` вебхуками с HMAC, координаты идут по прямой от ресторана к адресу.
  Сценарии: `POST :9094/_admin/courier/mode?couriers=off|on&create=ok|timeout|error|reject&status=ok|error&webhooks=on|off`,
  `POST :9094/_admin/courier/next?outcome=delivered|undelivered|nocourier`, `POST :9094/_admin/courier/advance?id=<id>&status=<status>`,
  `POST :9094/_admin/courier/redeliver?id=<id>`, `POST :9094/_admin/courier/reset`, `GET :9094/_inspect/courier`. Шаги растягиваются `MOCK_DELAY`.
- Оплата курьеру (`paymentMethod=courier_card`): платёж заводит payment-service по `orders.state`, ссылка уходит событием `orders.payments.link`,
  delivery передаёт её курьерской системе; на `delivered` мок курьера просит мок шлюза завершить операцию (`POST :9091/_admin/gateway/pay?id=`),
  итог приходит вебхуком шлюза — деньги подтверждает только шлюз. Срок жизни ссылки — `COURIER_PAYMENT_TTL` (4 ч).
- Зона доставки (долг 10): адрес без координат — `422 ADDRESS_LOCATION_REQUIRED`, вне радиуса ресторана — `422 ADDRESS_OUT_OF_ZONE` (`distanceMeters`, `radiusMeters`, `hint=pickup`).
  Проверка выполняется при `setCartFulfillment` и повторно при `createOrder`; если restaurant-service недоступен, заказ не оформляется (fail-closed).
- E2 «нет курьера»: за `SEARCH_TIMEOUT` (10 мин) доставка → `failed/no_courier`, `hint=pickup`; заказ остаётся `ready`, сотрудник выдаёт его `POST /orders/{id}/issuance`.
  Если его не забрали — janitor переводит в `not_received` (`pickup_timeout`) по `GREATEST(ready_at, delivery_updated_at)`.
- ETA: `etaAt` доставки от курьерской системы используется, пока свежий (`DELIVERY_ETA_TTL`=30 мин) и не раньше готовности; иначе — типичное время.

Сквозной сценарий через gateway с JWT (долг 40): стенд с `AUTH_MODE=jwks` и `auth gateway` в срезе, затем
```bash
AUTH_MODE=jwks make up SLICE="auth gateway restaurant menu orders payment delivery mock-externals"
make newman-e2e-gateway
```
Коллекция та же, окружение `postman/e2e/gateway.postman_environment.json` (клиентские вызовы идут на `:8080`; `/metrics` и probes — напрямую, gateway их не публикует),
токен сотрудника подставляет Makefile. Вебхуки курьерской системы идут мимо gateway (ingress, подпись `X-Signature`).

## Акции и скидки (S8)
```bash
make up SLICE="restaurant menu orders loyalty"
make newman SERVICE=loyalty         # loyalty напрямую: права, идемпотентность, конфликты E2, жизненный цикл, история
make newman SERVICE=e2e             # раздел 12: акция → promotions.discount → скидка в корзине и заказе (нужны Kafka и loyalty)
make loyalty-e2e                    # то же без Docker: реальные бинарники + in-memory Kafka (tools/kfake-broker)
```
- `make loyalty-e2e` сам проверяет, что порты 8082/8083/8084/8086/19092 свободны, и при конфликте завершается с кодом 2 и списком занятых портов (compose-срез сначала остановить: `make down SLICE=…`).
- Права на акции проверяет сам `loyalty-service` через `auth /auth/authorize` (`action=promotion.manage`, `AUTH_URL`); в `AUTH_MODE=dev` — локальные правила, зеркало политики auth
  (тест-таблица совпадает с auth). Владелец франшизы создаёт и ведёт только локальные акции своих ресторанов, HQ — любые; национальные акции — только HQ.
- Жизненный цикл: `draft → active → completed`; `scheduled` и `expired` выводятся из окна `startsAt/endsAt`. Две акции одного охвата и приоритета с пересечением по времени и позициям
  не могут быть активны вместе: `409 PROMOTION_CONFLICT` + `conflictingPromotionIds` (E2). Активации сериализуются advisory-lock'ом.
- Каждое изменение пишет полный снимок в `promotions.discount` (compact, ключ `promotionId`, `version`) через outbox; orders держит реплику (`promotions_replica`) и читает топик с начала,
  поэтому новая БД orders восстанавливает акции из компактного топика. Старая версия снимка игнорируется, битый снимок идёт в retry → `promotions.discount.dlq`.
- Расчёт в orders (`domain.Calculate`): проценты в сотых долях процента (1500 = 15 %), банковское округление один раз на акцию, фикс. скидка не больше суммы, итог не отрицательный,
  распределение по позициям без потери копеек. Кэш реплики — `PROMOTION_CACHE_TTL` (по умолчанию 2 с, `0s` — без кэша); при недоступной реплике берётся последний снимок.
- `createOrder` принимает необязательный `expectedTotal`: итог изменился (появилась/закончилась акция) → `409 TOTAL_CHANGED`, клиент перечитывает корзину. Скидка фиксируется в заказе
  (`appliedPromotions`) и не меняется при завершении акции.

## Push-уведомления (S9)
```bash
make up SLICE="restaurant menu orders notification mock-externals"
make newman SERVICE=notification    # токены устройств напрямую (AUTH_MODE=dev): изоляция пользователей, язык, валидация
make newman SERVICE=e2e             # раздел 13: заказ → orders.status → push на мок FCM, недействительный токен, отмена, гость без push
make notification-e2e               # то же без Docker: реальные бинарники + in-memory Kafka (tools/kfake-broker)
```
- `make notification-e2e` проверяет, что порты 8082/8083/8084/8088/9095/19092 свободны (код 2 и список занятых при конфликте).
- Провайдеры APNs/FCM/HMS — один мок на `:9095` (модуль `push` в mock-externals, `MOCKS=…,push`): принятые уведомления — `GET :9095/_inspect/pushes?orderId=…`, счётчики — `/_inspect/push`.
  Режимы: `POST /_admin/push/mode`, исход следующего запроса — `POST /_admin/push/next?outcome=ok|error|invalid`, сброс — `POST /_admin/push/reset`.
  Токен, содержащий `invalid`, получает «устройство не зарегистрировано», `reject` — постоянный отказ, `flaky` — первый запрос 503.
- Пауза повтора в compose-срезе сокращена до 1 с (`NOTIFICATION_SEND_BACKOFF`); в проде по умолчанию 2 с … 5 мин, 6 попыток.
- Через gateway для папки 13 нужны токены `customerToken`, `customer2Token`, `staffToken` (`--env-var`); push-токены регистрирует только зарегистрированный пользователь.

## Сценарии персонала (S6): касса, факс, выдача, отмена и возврат
```bash
make up SLICE="restaurant menu orders payment mock-externals"
make newman SERVICE=staff
make contract-check SCENARIO=staff
```
Роли персонала в dev-режиме задаются заголовками: `X-User-Id`, `X-User-Roles: STAFF|FRANCHISE_OWNER|HQ_STAFF`, `X-Restaurant-Ids` (HQ видит все рестораны). Матрица прав одинакова
везде: гость `401`, клиент `403`, сотрудник другого ресторана `404` (существование заказа не раскрывается).
`mock-externals` получает модуль `pos` (:9093): касса с фискальными данными (чек, ФН, ФД) в статусе и вебхуке, возвраты — как у шлюза.
Управление: `POST :9093/_admin/pos/mode?refund=error|reject|timeout|ok`, `GET :9093/_inspect/pos`; у шлюза то же: `POST :9091/_admin/gateway/mode?refund=…`.
Коллекция `staff` сама выключает очередь производства (отмена возможна только до приготовления), снимает задержку итога мока и возвращает всё в конце.
Автоматический `not_received` (готовый заказ на самовывоз не забрали) проверяется отдельно — таймер по умолчанию 2 часа:
```bash
NOT_RECEIVED_AFTER=10s JANITOR_INTERVAL=2s make up SLICE="restaurant menu orders payment mock-externals"
make newman-staff-timeout
```
Возврат вручную: `curl -XPOST localhost:9091/_admin/gateway/mode?refund=error`, отменить оплаченный заказ (`POST /orders/{id}/cancellation`) — возврат остаётся `pending` и
повторяется с нарастающей паузой; `…?refund=ok` — возврат проходит ровно один раз (ключ идемпотентности = `paymentId`).

## Периметр (S5): auth + gateway
```bash
make keys SERVICE=auth
AUTH_MODE=jwks make up SLICE="auth restaurant menu orders payment mock-externals gateway"
make newman SERVICE=gateway     # через http://localhost:8080
```
`AUTH_MODE=jwks` заставляет сервисы среза проверять JWT сами (локально по JWKS auth); без переменной они остаются в dev-режиме
(личность из заголовков `X-User-*`) — так работают остальные срезы и их Postman-коллекции. За gateway личность берётся **только** из токена.
Входы: клиент `+79990000000 / 123456`, персонал ресторана №4 `+79993333333 / staff123` (остальные — в `services/auth/README.md`).

Без Docker (CI и песочница): `make perimeter-check` (= `tools/perimeter/ci.sh`) собирает auth, restaurant, menu, orders, payment и gateway,
поднимает их на Postgres :5432 и прогоняет contract-check (`auth`, `gateway`), newman (`auth`, `gateway`) и сценарии, которые Postman не умеет:
блокировка пользователя после выдачи токена и отказ auth (fail-closed только на sensitive-маршрутах). `HOLD=1` оставляет стенд поднятым.
`make check-routes` сверяет таблицу маршрутов gateway с `x-routes` и контрактами сервисов.

В k8s: наружу смотрит только ingress → `gateway` (`/`) и → `payment` (`/webhooks/payments`, мимо gateway; лимиты частоты и размера — аннотациями ingress).
`/metrics` gateway — на отдельном порту `9100` (в чарте `metrics.port`), через ingress недоступен. `TRUSTED_PROXY_CIDRS` в `deploy/values/gateway.yaml`
нужно привести к CIDR вашего ingress-контроллера.

## Smoke периметра в k8s (долг 37)
```bash
make k8s-smoke-perimeter          # собрать образы, поднять стенд, newman + проверки через ingress (~10 мин в первый раз)
make k8s-smoke-perimeter-checks   # повторить только проверки на поднятом стенде
make k8s-clean                    # снести (ingress-nginx остаётся)
```
Нужны Docker Desktop с включённым Kubernetes (или kind: `KIND_CLUSTER=kind`), `kubectl`, `helm`, `python3`, `curl`, newman/npx. Скрипт отказывается
работать с нелокальным kube-context (обход: `SMOKE_ANY_CONTEXT=1`). Что поднимается в namespace `food`: Postgres (БД auth/restaurant/menu/orders/payment),
одноузловой Kafka (KRaft) с топиками из `topics.txt`, auth, restaurant, menu, orders, payment (1 реплика), mock-externals, gateway; в `ingress-nginx` —
контроллер `INGRESS_NGINX_VERSION` (по умолчанию `controller-v1.15.1`; проект ingress-nginx официально свёрнут, но манифест установим; для прода стоит заранее выбрать
замену: Gateway API или другой контроллер). Хост ingress — `INGRESS_HOST` (по умолчанию `localhost`, чтобы не править `/etc/hosts`).

Что проверяется (`tools/k8s-smoke/checks.py`):

| Проверка | Как |
|---|---|
| ровно два Ingress: `/` → gateway, `/webhooks/payments` → payment; все Service — ClusterIP; ingress смотрит на порт `http` | kubectl |
| `/metrics` (и `//metrics`, `/%6detrics`, `/webhooks/payments/metrics`) через ingress не отдаётся; `metrics` — отдельный порт Service | HTTP + kubectl |
| `/auth/introspect`, `/auth/authorize` через gateway — 404 | HTTP |
| вебхуки идут мимо gateway: без подписи — 401 `INVALID_SIGNATURE` от payment, с верной подписью и неизвестным платежом — 400, просроченная/чужая — 401 | HMAC-подписанные запросы |
| тело вебхука 100 КБ — 413 на ingress (`proxy-body-size: 64k`) | HTTP |
| `TRUSTED_PROXY_CIDRS` gateway покрывает IP pod'а controller | kubectl |
| лимит частоты вебхуков на ingress (503) срабатывает, но не отсекает всё | 600 параллельных запросов |
| лимит gateway по **реальному** IP: всплеск → 429 + `Retry-After`; подделка и ротация `X-Forwarded-For` его не обходят | 600/300/200 запросов |
| лимит логина (30/мин) → 429; **другой** реальный клиент (pod `curlimages/curl` в кластере) этим лимитом не затронут | 60 неверных логинов + pod |
| newman `gateway`, папки 0, 1, 2, 4 — через ingress | newman |

Папки 3 и 5 коллекции `gateway` в этот прогон не входят: они рассчитаны на прямой доступ к gateway (подделка `X-Forwarded-For`, вебхук «через gateway»),
за настоящим ingress ведут себя иначе — их роль выполняют проверки выше. Лимит логина на стенде поднят до 30/30 (newman входит с одного IP).

**`TRUSTED_PROXY_CIDRS`.** Smoke подставляет `/32` текущего pod'а controller (`--set-string env.TRUSTED_PROXY_CIDRS=<ip>/32`): IP pod'а меняется
при его перезапуске, поэтому в values лежит подсеть pod'ов (`10.0.0.0/8` — заглушка). В реальном окружении возьмите подсеть pod'ов, откуда ходит controller
(`kubectl get nodes -o jsonpath='{.items[*].spec.podCIDR}'`; в Docker Desktop поле пустое, pod'ы там из `10.1.0.0/16`) или, лучше, выделите controller'у отдельный namespace/подсеть и доверяйте только ей.

Отладка без кластера (эмулируется nginx перед `make perimeter-check` с `HOLD=1`): `checks.py --no-kubectl --source 127.0.0.2 --alt-source 127.0.0.3 --ingress-url http://127.0.0.1:<порт>`;
gateway при этом запускают с `TRUSTED_PROXY_CIDRS=127.0.0.1/32`.
