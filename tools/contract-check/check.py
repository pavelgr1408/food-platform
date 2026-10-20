#!/usr/bin/env python3
"""Контрактная проверка: живые ответы сервисов валидируются против схем contracts/openapi/*.yaml.

Использование (сервисы запущены, SEED_ON_START=true):
  .dev/venv/bin/python tools/contract-check/check.py catalog [--restaurant http://localhost:8082] [--menu http://localhost:8083]
  .dev/venv/bin/python tools/contract-check/check.py orders  [... --orders http://localhost:8084]   # нужен срез restaurant + menu + orders
  .dev/venv/bin/python tools/contract-check/check.py payment [... --payment http://localhost:8085]  # + payment и mock шлюза (:9091)
  .dev/venv/bin/python tools/contract-check/check.py auth    [... --auth http://localhost:8081]     # auth (SEED_ON_START=true)
  .dev/venv/bin/python tools/contract-check/check.py gateway [... --gateway http://localhost:8080]  # ответы периметра (Problem) через gateway

Для каждого запроса сценария находится операция по методу и шаблону пути, схема ответа берётся из
`responses.<status>.content.<media type>.schema` и проверяется jsonschema (Draft 2020-12, форматы uuid/date/…).
Ответ со статусом, которого нет в контракте, или без схемы при наличии тела — провал.
"""
import argparse, json, pathlib, re, sys, urllib.error, urllib.request
import yaml
from jsonschema import Draft202012Validator, FormatChecker
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012

ROOT = pathlib.Path(__file__).resolve().parents[2]
SPEC_DIR = ROOT / "contracts" / "openapi"


def load_registry():
    reg, docs = Registry(), {}
    for p in sorted(SPEC_DIR.glob("*.yaml")):
        doc = yaml.safe_load(p.read_text(encoding="utf-8"))
        docs[p.name] = doc
        reg = reg.with_resource(p.resolve().as_uri(), Resource.from_contents(doc, default_specification=DRAFT202012))
    return reg, docs


REG, DOCS = load_registry()
FAILS = []


def find_operation(spec, method, path):
    for tpl, item in DOCS[spec]["paths"].items():
        rx = "^" + re.sub(r"\{[^/]+\}", "[^/]+", tpl) + "$"
        if re.match(rx, path) and method.lower() in item:
            return item[method.lower()]
    return None


def resolve_ref(rdef, spec):
    """Ответ вида {$ref: '_common.yaml#/components/responses/X'} или '#/…' → сам объект ответа."""
    seen = 0
    while isinstance(rdef, dict) and "$ref" in rdef and seen < 5:
        file, _, ptr = rdef["$ref"].partition("#")
        doc = DOCS[file or spec]
        for part in [x for x in ptr.split("/") if x]:
            doc = doc[part]
        rdef, spec, seen = doc, (file or spec), seen + 1
    return rdef, spec


def check(spec, method, base, path, status_expected, body=None, headers=None, label=None):
    label = label or f"{method} {path}"
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method, headers={**({"Content-Type": "application/json"} if data else {}), **(headers or {})})
    try:
        resp = urllib.request.urlopen(req)
    except urllib.error.HTTPError as e:
        resp = e
    status, raw, ctype = resp.status if hasattr(resp, "status") else resp.code, resp.read(), resp.headers.get("Content-Type", "")
    errs = []
    if status != status_expected:
        errs.append(f"status {status}, ожидался {status_expected}")
    op = find_operation(spec, method, path.split("?")[0])
    if op is None:
        errs.append("операция не найдена в контракте")
    else:
        rdef = op["responses"].get(str(status))
        rspec = spec
        if rdef is not None:
            rdef, rspec = resolve_ref(rdef, spec)
        if rdef is None:
            errs.append(f"статус {status} не описан в контракте")
        elif raw:
            media = ctype.split(";")[0].strip()
            content = rdef.get("content", {})
            if media not in content:
                errs.append(f"media type {media!r} не описан (есть {list(content)})")
            elif "schema" in content[media]:
                schema = content[media]["schema"]
                v = Draft202012Validator(wrap(schema, rspec), registry=REG, format_checker=FormatChecker())
                for e in v.iter_errors(json.loads(raw)):
                    errs.append("/" + "/".join(map(str, e.absolute_path)) + ": " + e.message[:200])
        elif rdef.get("content"):
            errs.append("ответ без тела, а контракт ждёт тело")
    print(("OK   " if not errs else "FAIL ") + label + ("" if not errs else "\n       " + "\n       ".join(errs)))
    if errs:
        FAILS.append(label)
    return json.loads(raw) if raw and "json" in ctype else None


def wrap(schema, spec):
    """Схема из components документа spec: внутренние ссылки '#/…' резолвятся относительно файла."""
    uri = (SPEC_DIR / spec).resolve().as_uri()
    def fix(n):
        if isinstance(n, dict):
            out = {}
            for k, v in n.items():
                if k == "$ref" and isinstance(v, str):
                    out[k] = (uri + v) if v.startswith("#") else (SPEC_DIR / v.split("#")[0]).resolve().as_uri() + ("#" + v.split("#", 1)[1] if "#" in v else "")
                else:
                    out[k] = fix(v)
            return out
        if isinstance(n, list):
            return [fix(x) for x in n]
        return n
    return fix(schema)


def catalog(r, m):
    R1, R2, R4, R6 = (f"a1000000-0000-4000-8000-00000000000{i}" for i in (1, 2, 4, 6))
    S = "restaurant.yaml"
    check(S, "GET", r, "/restaurants?lat=55.7558&lon=37.6173", 200)
    check(S, "GET", r, "/restaurants?address=" + urllib.request.quote("Москва"), 200)
    check(S, "GET", r, "/restaurants", 200, label="GET /restaurants (без центра)")
    check(S, "GET", r, "/restaurants?address=" + urllib.request.quote("Екатеринбург"), 200, label="пустая выдача + hint")
    check(S, "GET", r, "/restaurants?lat=55.7280&lon=37.6030&deliveryOnly=true&radiusMeters=300", 200, label="hint=pickup")
    check(S, "GET", r, "/restaurants?lat=55.7", 400)
    check(S, "GET", r, f"/restaurants/{R1}", 200)
    check(S, "GET", r, f"/restaurants/{R2}", 200, label="GET /restaurants/{id} (ночной режим)")
    check(S, "GET", r, f"/restaurants/{R6}", 404)
    M = "menu.yaml"
    menu = check(M, "GET", m, f"/restaurants/{R1}/menu", 200)
    check(M, "GET", m, f"/restaurants/{R6}/menu", 404)
    items = [i for c in menu["categories"] for i in c["items"]]
    ok = next(i for i in items if i["available"] and i["modifiers"])
    off = next(i for i in items if not i["available"])
    v = f"/restaurants/{R1}/menu/validation"
    check(M, "POST", m, v, 200, {"items": [{"menuItemId": ok["id"], "quantity": 2, "modifierIds": [ok["modifiers"][0]["id"]]}]}, label="validate: valid")
    check(M, "POST", m, v, 200, {"items": [{"menuItemId": off["id"], "quantity": 1}, {"menuItemId": "c1000000-0000-4000-8000-0000000000aa", "quantity": 1}]}, label="validate: invalid + reasonCode")
    check(M, "POST", m, v, 400, {"items": []}, label="validate: пустой список")
    check(M, "POST", m, f"/restaurants/{R6}/menu/validation", 404, {"items": [{"menuItemId": ok["id"], "quantity": 1}]})
    # 304: тело пустое — проверяем статус и заголовок
    etag = urllib.request.urlopen(m + f"/restaurants/{R1}/menu").headers["ETag"]
    check(M, "GET", m, f"/restaurants/{R1}/menu", 304, headers={"If-None-Match": etag}, label="GET menu + If-None-Match → 304")


def orders(r, m, o):
    """Ядро заказа (S2): живые ответы orders против orders.yaml. Нужен срез restaurant + menu + orders."""
    import time
    S = "orders.yaml"
    R4 = "a1000000-0000-4000-8000-000000000004"
    menu = json.loads(urllib.request.urlopen(m + f"/restaurants/{R4}/menu").read())
    items = [i for c in menu["categories"] for i in c["items"]]
    burger = next(i for i in items if i["name"] == "BLT Классик")
    cola = next(i for i in items if i["name"].startswith("Кола"))
    bacon = next(x for x in burger["modifiers"] if x["name"] == "Двойной бекон")
    run = str(int(time.time() * 1000))
    alice = {"X-User-Id": "alice", "X-User-Roles": "CUSTOMER"}

    check(S, "POST", o, "/carts", 401, {"restaurantId": R4}, label="createCart: без токена и guest → 401")
    check(S, "POST", o, "/carts", 404, {"restaurantId": "a1000000-0000-4000-8000-0000000000ff", "guest": True}, label="createCart: нет ресторана → 404")
    check(S, "POST", o, "/carts", 400, {"restaurantId": R4, "bogus": 1}, label="createCart: лишнее поле → 400")
    cart = check(S, "POST", o, "/carts", 201, {"restaurantId": R4, "guest": True, "contact": {"phone": "+79990000099"}}, label="createCart")
    cid = cart["id"]
    check(S, "GET", o, f"/carts/{cid}", 200, label="getCart")
    check(S, "GET", o, "/carts/a1000000-0000-4000-8000-0000000000ff", 404, label="getCart: 404")
    check(S, "PUT", o, f"/carts/{cid}/items", 200, {"items": [{"menuItemId": burger["id"], "quantity": 2, "modifierIds": [bacon["id"]]}, {"menuItemId": cola["id"], "quantity": 1}]}, label="updateCartItems")
    check(S, "PUT", o, f"/carts/{cid}/items", 409, {"items": [{"menuItemId": cola["id"], "quantity": 1}, {"menuItemId": "c1000000-0000-4000-8000-0000000000aa", "quantity": 1}]}, label="updateCartItems: E1 → 409 + unavailableItems")
    check(S, "PUT", o, f"/carts/{cid}/items", 400, {"items": [{"menuItemId": cola["id"], "quantity": 0}]}, label="updateCartItems: 400")
    check(S, "PUT", o, f"/carts/{cid}/fulfillment", 422, {"type": "delivery"}, label="setCartFulfillment: ADDRESS_REQUIRED")
    check(S, "PUT", o, f"/carts/{cid}/fulfillment", 200, {"type": "delivery", "address": {"raw": "Невский пр., 28", "location": {"lat": 59.93, "lon": 30.33}}}, label="setCartFulfillment: delivery")
    check(S, "PUT", o, f"/carts/{cid}/fulfillment", 200, {"type": "pickup"}, label="setCartFulfillment: pickup")
    body = {"cartId": cid, "paymentMethod": "online_card"}
    check(S, "POST", o, "/orders", 400, body, label="createOrder: без Idempotency-Key → 400")
    check(S, "POST", o, "/orders", 422, {"cartId": cid, "paymentMethod": "courier_card"}, headers={"Idempotency-Key": "cc-" + run + "-a"}, label="createOrder: способ оплаты не подходит → 422")
    order = check(S, "POST", o, "/orders", 201, body, headers={"Idempotency-Key": "cc-" + run}, label="createOrder")
    check(S, "POST", o, "/orders", 201, body, headers={"Idempotency-Key": "cc-" + run}, label="createOrder: повтор (replay) → 201")
    check(S, "POST", o, "/orders", 422, {"cartId": cid, "paymentMethod": "pos"}, headers={"Idempotency-Key": "cc-" + run}, label="createOrder: тот же ключ, другое тело → 422")
    check(S, "POST", o, "/orders", 409, body, headers={"Idempotency-Key": "cc-" + run + "-b"}, label="createOrder: корзина закрыта → 409")
    oid = order["id"]
    check(S, "GET", o, f"/orders/{oid}", 200, label="getOrder")
    check(S, "GET", o, "/orders/a1000000-0000-4000-8000-0000000000ff", 404, label="getOrder: 404")
    check(S, "GET", o, f"/orders/{oid}/eta", 200, label="getOrderEta")
    t = time.gmtime(time.time() + 7200)
    frm = time.strftime("%Y-%m-%dT%H:%M:%SZ", t)
    to = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() + 9000))
    check(S, "PUT", o, f"/orders/{oid}/pickup-slot", 200, {"from": frm, "to": to}, label="setPickupSlot")
    check(S, "PUT", o, f"/orders/{oid}/pickup-slot", 422, {"from": to, "to": frm}, label="setPickupSlot: 422")
    check(S, "PUT", o, f"/orders/{oid}/pickup-slot", 400, {"from": frm}, label="setPickupSlot: 400")
    # заказ пользователя и чужой доступ
    ac = check(S, "POST", o, "/carts", 201, {"restaurantId": R4}, headers=alice, label="createCart: пользователь")
    check(S, "PUT", o, f"/carts/{ac['id']}/items", 200, {"items": [{"menuItemId": cola["id"], "quantity": 1}]}, headers=alice, label="updateCartItems: пользователь")
    check(S, "PUT", o, f"/carts/{ac['id']}/fulfillment", 200, {"type": "pickup"}, headers=alice, label="setCartFulfillment: пользователь")
    check(S, "GET", o, f"/carts/{ac['id']}", 404, label="getCart: чужая корзина → 404")
    ao = check(S, "POST", o, "/orders", 201, {"cartId": ac["id"], "paymentMethod": "pos"}, headers={**alice, "Idempotency-Key": "ac-" + run}, label="createOrder: пользователь, pos → accepted")
    check(S, "GET", o, f"/orders/{ao['id']}", 404, label="getOrder: чужой заказ → 404")


def payment(r, m, o, p):
    """Деньги (S3): живые ответы payment против payment.yaml. Нужен срез restaurant + menu + orders + payment + mock шлюза.
    С Kafka заказ создаётся через orders и доезжает до payment событием orders.state. Без Kafka (CI) заказ
    засевается в БД payment скриптом ci.sh: CC_PAYMENT_ORDER=<uuid> (сумма — CC_PAYMENT_AMOUNT, копейки)."""
    import os, time
    S = "payment.yaml"
    run = str(int(time.time() * 1000))
    oid, amount = os.environ.get("CC_PAYMENT_ORDER"), int(os.environ.get("CC_PAYMENT_AMOUNT", "0"))
    if not oid:
        R4 = "a1000000-0000-4000-8000-000000000004"
        menu = json.loads(urllib.request.urlopen(m + f"/restaurants/{R4}/menu").read())
        burger = next(i for c in menu["categories"] for i in c["items"] if i["name"] == "BLT Классик")
        cart = check("orders.yaml", "POST", o, "/carts", 201, {"restaurantId": R4, "guest": True, "contact": {"phone": "+79990000098"}}, label="[подготовка] createCart")
        check("orders.yaml", "PUT", o, f"/carts/{cart['id']}/items", 200, {"items": [{"menuItemId": burger["id"], "quantity": 1}]}, label="[подготовка] updateCartItems")
        check("orders.yaml", "PUT", o, f"/carts/{cart['id']}/fulfillment", 200, {"type": "pickup"}, label="[подготовка] setCartFulfillment")
        order = check("orders.yaml", "POST", o, "/orders", 201, {"cartId": cart["id"], "paymentMethod": "online_card"}, headers={"Idempotency-Key": "pc-" + run}, label="[подготовка] createOrder")
        oid, amount = order["id"], order["total"]["amount"]
        for _ in range(30):  # ждём orders.state в payment
            try:
                urllib.request.urlopen(p + f"/orders/{oid}/payment-methods")
                break
            except urllib.error.HTTPError:
                time.sleep(0.5)
    ghost = "a1000000-0000-4000-8000-0000000000ff"
    check(S, "GET", p, f"/orders/{ghost}/payment-methods", 404, label="getPaymentMethods: заказ не найден → 404")
    check(S, "GET", p, f"/orders/{oid}/payment-methods", 200, label="getPaymentMethods")
    body = {"method": "online_card"}
    check(S, "POST", p, f"/orders/{oid}/payments", 400, body, label="initiatePayment: без Idempotency-Key → 400")
    check(S, "POST", p, f"/orders/{ghost}/payments", 404, body, headers={"Idempotency-Key": "pc-" + run + "-g"}, label="initiatePayment: заказ не найден → 404")
    check(S, "POST", p, f"/orders/{oid}/payments", 400, {"method": "bitcoin"}, headers={"Idempotency-Key": "pc-" + run + "-m"}, label="initiatePayment: неизвестный способ → 400")
    check(S, "POST", p, f"/orders/{oid}/payments", 422, {"method": "courier_card"}, headers={"Idempotency-Key": "pc-" + run + "-c"}, label="initiatePayment: способ не совпадает с заказом → 422")
    pay = check(S, "POST", p, f"/orders/{oid}/payments", 201, body, headers={"Idempotency-Key": "pc-" + run}, label="initiatePayment")
    if pay and amount and pay["amount"]["amount"] != amount:
        FAILS.append("сумма платежа != сумме заказа"); print("FAIL сумма платежа не равна сумме заказа")
    check(S, "POST", p, f"/orders/{oid}/payments", 201, body, headers={"Idempotency-Key": "pc-" + run}, label="initiatePayment: повтор (replay) → 201")
    check(S, "POST", p, f"/orders/{oid}/payments", 422, {"method": "pos"}, headers={"Idempotency-Key": "pc-" + run}, label="initiatePayment: тот же ключ, другое тело → 422")
    if pay:
        pid = pay["id"]
        check(S, "POST", p, f"/orders/{oid}/payments", 409, body, headers={"Idempotency-Key": "pc-" + run + "-2"}, label="initiatePayment: платёж уже идёт → 409")
        check(S, "GET", p, f"/orders/{oid}/payments/{pid}", 200, label="getPayment")
        check(S, "GET", p, f"/orders/{oid}/payments/{ghost}", 404, label="getPayment: 404")
        for _ in range(60):  # шлюз подтверждает вебхуком (или опросом reconciler)
            st = json.loads(urllib.request.urlopen(p + f"/orders/{oid}/payments/{pid}").read())["status"]
            if st == "paid":
                break
            time.sleep(0.5)
        check(S, "GET", p, f"/orders/{oid}/payments/{pid}", 200, label=f"getPayment: после шлюза ({st})")
        if st != "paid":
            FAILS.append("платёж не дошёл до paid"); print("FAIL платёж не дошёл до paid, статус", st)
        check(S, "POST", p, f"/orders/{oid}/payments", 409, body, headers={"Idempotency-Key": "pc-" + run + "-3"}, label="initiatePayment: уже оплачен → 409")
    # вебхуки описаны в разделе webhooks контракта (не paths): HMAC и дубликаты проверяет postman/payment


def auth(a):
    S = "auth.yaml"
    phone = "+7999" + str(int(__import__("time").time()) % 10_000_000).zfill(7)
    check(S, "POST", a, "/auth/register", 201, {"phone": phone, "password": "Passw0rd!x"})
    check(S, "POST", a, "/auth/register", 409, {"phone": phone, "password": "Passw0rd!x"}, label="register: дубль телефона → 409 Problem")
    check(S, "POST", a, "/auth/register", 400, {"phone": "abc", "password": "x"}, label="register: валидация → 400 Problem")
    t = check(S, "POST", a, "/auth/login", 200, {"phone": phone, "password": "Passw0rd!x"})
    check(S, "POST", a, "/auth/login", 401, {"phone": phone, "password": "wrong-password"}, label="login: неверный пароль → 401 Problem")
    check(S, "POST", a, "/auth/login", 403, {"phone": "+79992222222", "password": "blocked123"}, label="login: заблокирован → 403 Problem")
    bearer = {"Authorization": "Bearer " + t["accessToken"]}
    check(S, "GET", a, "/auth/me", 200, headers=bearer)
    check(S, "GET", a, "/auth/me", 401, label="me: без токена → 401 Problem")
    check(S, "POST", a, "/auth/introspect", 200, {"token": t["accessToken"]}, label="introspect: active")
    check(S, "POST", a, "/auth/introspect", 200, {"token": "garbage"}, label="introspect: inactive")
    check(S, "POST", a, "/auth/authorize", 200, {"action": "order.manage", "restaurantId": "a1000000-0000-4000-8000-000000000004"}, headers=bearer, label="authorize: клиент → allowed=false")
    staff = check(S, "POST", a, "/auth/login", 200, {"phone": "+79993333333", "password": "staff123"}, label="login: сотрудник")
    check(S, "POST", a, "/auth/authorize", 200, {"action": "order.manage", "restaurantId": "a1000000-0000-4000-8000-000000000004"},
          headers={"Authorization": "Bearer " + staff["accessToken"]}, label="authorize: сотрудник своего ресторана → allowed=true")
    check(S, "POST", a, "/auth/authorize", 400, {"action": "nope"}, headers=bearer, label="authorize: неизвестное действие → 400 Problem")
    check(S, "GET", a, "/auth/.well-known/jwks.json", 200)
    r = check(S, "POST", a, "/auth/refresh", 200, {"refreshToken": t["refreshToken"]})
    check(S, "POST", a, "/auth/refresh", 401, {"refreshToken": t["refreshToken"]}, label="refresh: повтор использованного → 401 Problem")
    check(S, "POST", a, "/auth/logout", 200, {"refreshToken": r["refreshToken"]})
    check(S, "POST", a, "/auth/logout", 401, {"refreshToken": r["refreshToken"]}, label="logout: повтор → 401 Problem")


def gateway(g):
    """Ответы периметра: схемы Problem из _common.yaml (у gateway.yaml нет paths — только таблица маршрутов)."""
    problem = {"$ref": "_common.yaml#/components/schemas/Problem"}
    v = Draft202012Validator(wrap(problem, "_common.yaml"), registry=REG, format_checker=FormatChecker())

    def probe(method, path, status, code, headers=None, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(g + path, data=data, method=method, headers={**({"Content-Type": "application/json"} if data else {}), **(headers or {})})
        try:
            resp = urllib.request.urlopen(req)
        except urllib.error.HTTPError as e:
            resp = e
        st = resp.status if hasattr(resp, "status") else resp.code
        raw, ctype = resp.read(), resp.headers.get("Content-Type", "")
        errs = []
        if st != status:
            errs.append(f"status {st}, ожидался {status}")
        if not ctype.startswith("application/problem+json"):
            errs.append(f"Content-Type {ctype!r}")
        else:
            doc = json.loads(raw)
            errs += ["/" + "/".join(map(str, e.absolute_path)) + ": " + e.message[:200] for e in v.iter_errors(doc)]
            if doc.get("code") != code:
                errs.append(f"code {doc.get('code')!r}, ожидался {code!r}")
        if len(resp.headers.get_all("X-Request-Id") or []) != 1:
            errs.append("X-Request-Id должен быть ровно один")
        label = f"{method} {path} → {status} {code}"
        print(("OK   " if not errs else "FAIL ") + label + ("" if not errs else "\n       " + "\n       ".join(errs)))
        if errs:
            FAILS.append(label)

    probe("GET", "/auth/me", 401, "UNAUTHORIZED")
    probe("GET", "/auth/me", 401, "UNAUTHORIZED", headers={"Authorization": "Bearer garbage"})
    probe("POST", "/auth/introspect", 404, "ROUTE_NOT_FOUND", body={"token": "x"})
    probe("GET", "/metrics", 404, "ROUTE_NOT_FOUND")
    probe("POST", "/webhooks/payments/gateway", 404, "ROUTE_NOT_FOUND", body={})
    t = json.loads(urllib.request.urlopen(urllib.request.Request(g + "/auth/login", data=json.dumps({"phone": "+79990000000", "password": "123456"}).encode(), headers={"Content-Type": "application/json"})).read())
    probe("GET", "/orders/search?number=1", 403, "FORBIDDEN", headers={"Authorization": "Bearer " + t["accessToken"]})
    # 413 здесь не проверяем: gateway отвечает сразу и закрывает соединение, а urllib ещё досылает 1 МиБ тела и
    # получает ConnectionResetError (зависит от буферов ОС). Эту проверку делает Postman (postman/gateway, «Ограничения»).


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("scenario", choices=["catalog", "orders", "payment", "auth", "gateway"])
    ap.add_argument("--auth", default="http://localhost:8081")
    ap.add_argument("--gateway", default="http://localhost:8080")
    ap.add_argument("--restaurant", default="http://localhost:8082")
    ap.add_argument("--menu", default="http://localhost:8083")
    ap.add_argument("--orders", default="http://localhost:8084")
    ap.add_argument("--payment", default="http://localhost:8085")
    a = ap.parse_args()
    if a.scenario == "catalog":
        catalog(a.restaurant, a.menu)
    elif a.scenario == "auth":
        auth(a.auth)
    elif a.scenario == "gateway":
        gateway(a.gateway)
    elif a.scenario == "orders":
        orders(a.restaurant, a.menu, a.orders)
    else:
        payment(a.restaurant, a.menu, a.orders, a.payment)
    print(f"\nпровалов: {len(FAILS)}")
    sys.exit(1 if FAILS else 0)
