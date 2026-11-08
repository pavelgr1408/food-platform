import { http, HttpResponse, type HttpHandler } from "msw";
import type { Cart, CartItem, Order } from "@fp/api";
import { CITY, RESTAURANTS, haversine, type MockDb, type MockItem, type MockPromotion, type MockRestaurant } from "./db";
import { API, advanceOrder, canAccessRestaurant, isStaff, money, paymentView, problem, settle } from "./util";
import { createStaffHandlers } from "./staff";

/**
 * Мок gateway по семантике contracts/openapi (ADR-5). Это не копия бэкенда: он воспроизводит коды ошибок и
 * инварианты, на которые ветвится фронт (FRONTEND-CONTRACT), чтобы экраны можно было развивать и тестировать без стенда.
 */

const findItem = (r: MockRestaurant, id: string): MockItem | undefined => r.menu?.flatMap((c) => c.items).find((i) => i.id === id);
/** Витрина (PromotionPublic): без служебных полей управления. */
const publicPromo = ({ status: _s, version: _v, createdAt: _c, updatedAt: _u, createdBy: _b, ...p }: MockPromotion) => p;

export function createHandlers(db: MockDb): HttpHandler[] {
  const now = () => Date.now();
  const activePromos = () => [...db.promotions.values()].filter((p) => p.status === "active");
  const principal = (req: Request) => {
    const h = req.headers.get("Authorization");
    const t = h?.startsWith("Bearer ") ? h.slice(7) : null;
    const a = t ? db.access.get(t) : undefined;
    if (!a || a.exp < now()) return null;
    return [...db.users.values()].find((u) => u.id === a.userId) ?? null;
  };
  const issue = (userId: string) => {
    const accessToken = `mock-a-${db.id()}`, refreshToken = `mock-r-${db.id()}`;
    db.access.set(accessToken, { userId, exp: now() + 900_000 });
    db.refresh.set(refreshToken, userId);
    return { accessToken, refreshToken, tokenType: "Bearer", expiresIn: 900 };
  };

  /* ------------- cart math ------------- */
  const reprice = (c: Cart, r: MockRestaurant): Cart => {
    const items: CartItem[] = c.items.map((i) => {
      const mi = findItem(r, i.menuItemId)!;
      const mods = i.modifierIds.map((id) => mi.modifiers.find((m) => m.id === id)!);
      const unit = mi.price + mods.reduce((s, m) => s + m.price, 0);
      const ok = mi.available && mods.every((m) => m.available);
      return { ...i, name: mi.name, available: ok, unitPrice: money(unit), lineTotal: money(unit * i.quantity), modifiers: mods.map((m) => ({ id: m.id, name: m.name, price: money(m.price) })) };
    });
    const subtotal = items.filter((i) => i.available).reduce((s, i) => s + i.lineTotal.amount, 0);
    const t = now();
    const applied = activePromos().filter((p) => (p.scope === "national" || p.restaurantId === r.id) && Date.parse(p.startsAt) <= t && t < Date.parse(p.endsAt)
      && subtotal >= (p.conditions.minSubtotal ?? 0) && (!p.conditions.fulfillment || p.conditions.fulfillment === c.fulfillment?.type)
      && (!p.conditions.menuItemIds?.length || items.some((i) => i.available && p.conditions.menuItemIds!.includes(i.menuItemId))))
      .sort((a, b) => b.priority - a.priority).slice(0, 1)
      .map((p) => ({ promotionId: p.id, title: p.title, discount: money(p.discount.type === "percent" ? Math.round((subtotal * p.discount.value) / 10000) : Math.min(p.discount.value, subtotal)) }));
    const discount = applied.reduce((s, a) => s + a.discount.amount, 0);
    return { ...c, items, subtotal: money(subtotal), appliedPromotions: applied, discountTotal: money(discount), total: money(subtotal - discount) };
  };

  const ownerOf = (req: Request) => principal(req)?.id ?? null;

  const staff = createStaffHandlers(db, { principal, activePromos });
  /* Маршруты персонала раньше общих: `/orders/queue` и `/orders/search` иначе совпали бы с `/orders/:id`. */
  return [
    ...staff,
    /* ---------------- auth ---------------- */
    http.post(`${API}/auth/register`, async ({ request }) => {
      const b = (await request.json()) as { phone?: string; password?: string };
      if (!b.phone || !/^\+[1-9][0-9]{7,14}$/.test(b.phone) || !b.password || b.password.length < 6) return problem(400, "VALIDATION_FAILED", "Некорректные данные");
      if (db.users.has(b.phone)) return problem(409, "PHONE_ALREADY_REGISTERED", "Телефон уже зарегистрирован");
      const u = { id: db.id(), phone: b.phone, password: b.password, roles: ["CUSTOMER" as const] };
      db.users.set(b.phone, u);
      return HttpResponse.json({ userId: u.id, phone: u.phone, status: "ACTIVE" }, { status: 201 });
    }),
    http.post(`${API}/auth/login`, async ({ request }) => {
      const b = (await request.json()) as { phone?: string; password?: string };
      const u = b.phone ? db.users.get(b.phone) : undefined;
      if (!u || u.password !== b.password) return problem(401, "INVALID_CREDENTIALS", "Неверный телефон или пароль");
      return HttpResponse.json(issue(u.id));
    }),
    http.post(`${API}/auth/refresh`, async ({ request }) => {
      const { refreshToken } = (await request.json()) as { refreshToken?: string };
      const uid = refreshToken ? db.refresh.get(refreshToken) : undefined;
      if (!refreshToken || !uid) return problem(401, "INVALID_REFRESH_TOKEN", "Недействительный refresh-токен");
      db.refresh.delete(refreshToken); // одноразовый
      return HttpResponse.json(issue(uid));
    }),
    http.post(`${API}/auth/logout`, async ({ request }) => {
      const { refreshToken } = (await request.json()) as { refreshToken?: string };
      if (!refreshToken || !db.refresh.delete(refreshToken)) return problem(401, "INVALID_REFRESH_TOKEN", "Недействительный refresh-токен");
      return HttpResponse.json({ message: "ok" });
    }),
    http.get(`${API}/auth/me`, ({ request }) => {
      const u = principal(request);
      if (!u) return problem(401, "INVALID_TOKEN", "Токен недействителен");
      return HttpResponse.json({ userId: u.id, phone: u.phone, roles: u.roles, status: "ACTIVE", restaurants: u.restaurants ?? [] });
    }),

    /* ---------------- restaurants ---------------- */
    http.get(`${API}/restaurants`, ({ request }) => {
      const sp = new URL(request.url).searchParams;
      const num = (k: string) => (sp.has(k) ? Number(sp.get(k)) : undefined);
      const bool = (k: string) => (sp.has(k) ? (["true", "false"].includes(sp.get(k)!) ? sp.get(k) === "true" : "bad") : undefined);
      const lat = num("lat"), lon = num("lon");
      if ((lat === undefined) !== (lon === undefined)) return problem(400, "COORDINATES_INCOMPLETE", "Нужны обе координаты");
      const radius = num("radiusMeters") ?? 5000, limit = num("limit") ?? 20, offset = num("offset") ?? 0;
      const dOnly = bool("deliveryOnly"), oNow = bool("openNow");
      if ([lat, lon, radius, limit, offset].some((v) => v !== undefined && Number.isNaN(v)) || (lat !== undefined && (Math.abs(lat) > 90 || Math.abs(lon!) > 180))
        || radius < 100 || radius > 50000 || limit < 1 || limit > 100 || offset < 0 || dOnly === "bad" || oNow === "bad") return problem(400, "INVALID_PARAMETER", "Некорректный параметр");

      let center: [number, number] | undefined = lat !== undefined ? [lat, lon!] : undefined;
      let unknownAddress = false;
      const address = sp.get("address")?.trim().toLowerCase();
      if (!center && address) {
        const key = Object.keys(CITY).find((c) => address.includes(c));
        if (key) center = CITY[key]; else unknownAddress = true;
      }
      const build = (deliveryOnly: boolean) => RESTAURANTS.map((r) => {
        const dist = center ? haversine(center, [r.lat, r.lon]) : undefined;
        const deliveryAvailable = r.deliveryEnabled && (dist === undefined || dist <= r.deliveryRadius);
        return { r, dist, deliveryAvailable };
      }).filter((x) => unknownAddress ? false : (center ? x.dist! <= radius : true))
        .filter((x) => (deliveryOnly ? x.deliveryAvailable : true) && (oNow === true ? x.r.open : true))
        .sort((a, b) => (a.dist !== undefined ? a.dist - b.dist! : a.r.name.localeCompare(b.r.name)));
      const all = build(dOnly === true);
      const page = all.slice(offset, offset + limit).map(({ r, dist, deliveryAvailable }) => ({
        id: r.id, name: r.name, address: r.address, city: r.city, location: { lat: r.lat, lon: r.lon },
        ...(dist !== undefined ? { distanceMeters: dist } : {}), deliveryAvailable, openNow: r.open,
      }));
      let hint: string | undefined;
      if (all.length === 0) hint = unknownAddress ? "change_address" : dOnly === true && build(false).length > 0 ? "pickup" : radius < 50000 ? "expand_radius" : "change_address";
      return HttpResponse.json({ items: page, total: all.length, limit, offset, ...(center ? { center: { lat: center[0], lon: center[1] } } : {}), ...(hint ? { hint } : {}) });
    }),
    http.get(`${API}/restaurants/:id`, ({ params }) => {
      const r = RESTAURANTS.find((x) => x.id === params.id);
      if (!r) return problem(404, "RESTAURANT_NOT_FOUND", "Ресторан не найден");
      const future = new Date(Date.now() + 5 * 86400_000).toISOString().slice(0, 10);
      return HttpResponse.json({
        id: r.id, name: r.name, address: r.address, city: r.city, location: { lat: r.lat, lon: r.lon }, deliveryAvailable: r.deliveryEnabled, openNow: r.open,
        timezone: r.timezone, deliveryEnabled: r.deliveryEnabled, deliveryRadiusMeters: r.deliveryRadius,
        workingHours: [1, 2, 3, 4, 5, 6, 7].map((d) => ({ dayOfWeek: d, opensAt: "09:00", closesAt: d >= 5 ? "24:00" : "22:00" })),
        scheduleExceptions: [{ date: future, closed: true, note: "Санитарный день" }],
      });
    }),
    http.get(`${API}/restaurants/:id/menu`, ({ params }) => {
      const r = RESTAURANTS.find((x) => x.id === params.id);
      if (!r?.menu) return problem(404, "MENU_NOT_FOUND", "Меню не найдено");
      return HttpResponse.json({ restaurantId: r.id, menuVersion: 1, categories: r.menu.map((c) => ({ id: c.id, name: c.name, items: c.items.map((i) => ({ id: i.id, name: i.name, description: i.description, available: i.available, price: money(i.price), modifiers: i.modifiers.map((m) => ({ id: m.id, name: m.name, available: m.available, price: money(m.price) })) })) })) });
    }),
    http.post(`${API}/restaurants/:id/menu/validation`, async ({ params, request }) => {
      const r = RESTAURANTS.find((x) => x.id === params.id);
      if (!r?.menu) return problem(404, "MENU_NOT_FOUND", "Меню не найдено");
      const b = (await request.json()) as { items: { menuItemId: string; modifierIds?: string[]; quantity: number }[] };
      const lines = b.items.map((l) => {
        const it = findItem(r, l.menuItemId);
        if (!it) return { menuItemId: l.menuItemId, quantity: l.quantity, available: false, reasonCode: "ITEM_NOT_FOUND" };
        if (!it.available) return { menuItemId: l.menuItemId, quantity: l.quantity, available: false, reasonCode: "ITEM_UNAVAILABLE", name: it.name };
        return { menuItemId: l.menuItemId, quantity: l.quantity, available: true, name: it.name, currentPrice: money(it.price), lineTotal: money(it.price * l.quantity) };
      });
      return HttpResponse.json({ menuVersion: 1, valid: lines.every((l) => l.available), lines, total: money(lines.reduce((s, l) => s + (l.lineTotal?.amount ?? 0), 0)) });
    }),
    http.get(`${API}/promotions`, ({ request }) => {
      const rid = new URL(request.url).searchParams.get("restaurantId");
      const t = now();
      return HttpResponse.json(activePromos().filter((p) => Date.parse(p.startsAt) <= t && t < Date.parse(p.endsAt) && (p.scope === "national" || (rid && p.restaurantId === rid))).map(publicPromo));
    }),

    /* ---------------- carts ---------------- */
    http.post(`${API}/carts`, async ({ request }) => {
      const b = (await request.json()) as { restaurantId?: string; guest?: boolean; contact?: { phone?: string } };
      const owner = ownerOf(request);
      if (!owner && !b.guest) return problem(401, "UNAUTHENTICATED", "Нужен токен или guest=true");
      const r = RESTAURANTS.find((x) => x.id === b.restaurantId);
      if (!r) return problem(404, "RESTAURANT_NOT_FOUND", "Ресторан не найден");
      const cart: Cart = reprice({ id: db.id(), restaurantId: r.id, status: "open", items: [], subtotal: money(0), discountTotal: money(0), total: money(0), appliedPromotions: [] }, r);
      db.carts.set(cart.id, { cart, restaurantId: r.id, owner, contact: b.contact });
      return HttpResponse.json(cart, { status: 201, headers: { Location: `/carts/${cart.id}` } });
    }),
    http.get(`${API}/carts/:id`, ({ params }) => {
      const c = db.carts.get(params.id as string);
      if (!c) return problem(404, "CART_NOT_FOUND", "Корзина не найдена");
      const r = RESTAURANTS.find((x) => x.id === c.restaurantId)!;
      c.cart = reprice(c.cart, r);
      return HttpResponse.json(c.cart);
    }),
    http.put(`${API}/carts/:id/items`, async ({ params, request }) => {
      const c = db.carts.get(params.id as string);
      if (!c) return problem(404, "CART_NOT_FOUND", "Корзина не найдена");
      if (c.cart.status === "ordered") return problem(409, "CART_CLOSED", "Корзина уже оформлена");
      const r = RESTAURANTS.find((x) => x.id === c.restaurantId)!;
      const { items } = (await request.json()) as { items: { menuItemId: string; modifierIds?: string[]; quantity: number }[] };
      if (items.length > 50 || items.some((i) => !Number.isInteger(i.quantity) || i.quantity < 1 || i.quantity > 99)) return problem(400, "INVALID_PARAMETER", "Некорректное количество");
      const bad: { menuItemId: string; reasonCode: string; reason: string }[] = [];
      const keep = items.filter((l) => {
        const it = findItem(r, l.menuItemId);
        if (!it) { bad.push({ menuItemId: l.menuItemId, reasonCode: "ITEM_NOT_FOUND", reason: "Позиция не найдена" }); return false; }
        if (!it.available) { bad.push({ menuItemId: l.menuItemId, reasonCode: "ITEM_UNAVAILABLE", reason: "Позиция недоступна" }); return false; }
        const badMod = (l.modifierIds ?? []).find((id) => !it.modifiers.find((m) => m.id === id)?.available);
        if (badMod) { bad.push({ menuItemId: l.menuItemId, reasonCode: "MODIFIER_UNAVAILABLE", reason: "Добавка недоступна" }); return false; }
        return true;
      });
      c.cart = reprice({ ...c.cart, items: keep.map((l) => ({ menuItemId: l.menuItemId, modifierIds: l.modifierIds ?? [], quantity: l.quantity, name: "", available: true, unitPrice: money(0), lineTotal: money(0) })) }, r);
      // корзина сохраняется без недоступных, затем 409 (FRONTEND-CONTRACT, UC-CRT-05)
      if (bad.length) return problem(409, "ITEMS_UNAVAILABLE", "Некоторые позиции недоступны", { unavailableItems: bad });
      return HttpResponse.json(c.cart);
    }),
    http.put(`${API}/carts/:id/fulfillment`, async ({ params, request }) => {
      const c = db.carts.get(params.id as string);
      if (!c) return problem(404, "CART_NOT_FOUND", "Корзина не найдена");
      if (c.cart.status === "ordered") return problem(409, "CART_CLOSED", "Корзина уже оформлена");
      const r = RESTAURANTS.find((x) => x.id === c.restaurantId)!;
      const b = (await request.json()) as { type: "pickup" | "delivery"; address?: { raw?: string; location?: { lat: number; lon: number } } };
      if (b.type === "delivery") {
        if (!r.deliveryEnabled) return problem(422, "DELIVERY_NOT_AVAILABLE", "Ресторан не доставляет", { hint: "pickup" });
        if (!b.address?.raw?.trim()) return problem(422, "ADDRESS_REQUIRED", "Нужен адрес");
        if (!b.address.location) return problem(422, "ADDRESS_LOCATION_REQUIRED", "Нужна точка на карте");
        const { lat, lon } = b.address.location;
        if (!Number.isFinite(lat) || !Number.isFinite(lon) || Math.abs(lat) > 90 || Math.abs(lon) > 180) return problem(400, "INVALID_PARAMETER", "Некорректные координаты");
        const d = haversine([lat, lon], [r.lat, r.lon]);
        if (d > r.deliveryRadius) return problem(422, "ADDRESS_OUT_OF_ZONE", "Адрес вне зоны доставки", { distanceMeters: d, radiusMeters: r.deliveryRadius, hint: "pickup" });
      }
      c.cart = reprice({ ...c.cart, fulfillment: b.type === "pickup" ? { type: "pickup" } : { type: "delivery", address: b.address } }, r);
      return HttpResponse.json(c.cart);
    }),

    /* ---------------- orders ---------------- */
    http.post(`${API}/orders`, async ({ request }) => {
      const key = request.headers.get("Idempotency-Key");
      if (!key) return problem(400, "IDEMPOTENCY_KEY_REQUIRED", "Нужен Idempotency-Key");
      const raw = await request.text();
      const prev = db.idem.get(key);
      if (prev) {
        if (prev.sig !== raw) return problem(422, "IDEMPOTENCY_KEY_REUSED", "Ключ уже использован с другим запросом");
        return HttpResponse.json(prev.body as object, { status: prev.status, headers: { "Idempotent-Replay": "true", "Content-Type": (prev.status >= 400 ? "application/problem+json" : "application/json") } });
      }
      const respond = (res: Response, status: number, body: unknown) => { if (status < 500) db.idem.set(key, { sig: raw, status, body }); return res; };
      const fail = (status: number, code: string, title: string, extra: Record<string, unknown> = {}) =>
        respond(problem(status, code, title, extra), status, { type: "about:blank", title, status, code, ...extra });

      const b = JSON.parse(raw) as { cartId: string; channel: string; paymentMethod: Order["paymentMethod"]; contact?: { phone?: string; name?: string; email?: string }; expectedTotal?: { amount: number } };
      const c = db.carts.get(b.cartId);
      if (!c) return fail(404, "CART_NOT_FOUND", "Корзина не найдена");
      if (c.cart.status === "ordered") return fail(409, "CART_CLOSED", "Корзина уже оформлена");
      const r = RESTAURANTS.find((x) => x.id === c.restaurantId)!;
      if (c.cart.items.length === 0) return fail(422, "CART_EMPTY", "Корзина пуста");
      const unavailable = c.cart.items.filter((i) => !i.available);
      if (unavailable.length) return fail(409, "ITEMS_UNAVAILABLE", "Позиции недоступны", { unavailableItems: unavailable.map((i) => ({ menuItemId: i.menuItemId, reasonCode: "ITEM_UNAVAILABLE", reason: "Позиция недоступна" })) });
      if (!c.cart.fulfillment) return fail(422, "FULFILLMENT_REQUIRED", "Выберите способ получения");
      const user = principal(request);
      const fax = b.channel === "staff_fax";
      // UC-STF-05: заказ по факсу оформляет только сотрудник своего ресторана; онлайн-оплата запрещена, телефон клиента обязателен
      if (fax) {
        if (!user) return fail(401, "UNAUTHORIZED", "Нужна авторизация");
        if (!isStaff(user) || !canAccessRestaurant(user, c.restaurantId)) return fail(403, "FORBIDDEN", "Нет доступа к ресторану");
        if (!b.contact?.phone) return fail(422, "CONTACT_REQUIRED", "Нужен телефон клиента");
        if (b.paymentMethod === "online_card") return fail(422, "PAYMENT_METHOD_NOT_ALLOWED", "Онлайн-оплата недоступна для заказа по факсу");
      } else if (!user && !b.contact?.phone) return fail(422, "CONTACT_REQUIRED", "Нужен телефон");
      const allowed = c.cart.fulfillment.type === "pickup" ? ["online_card", "pos"] : ["online_card", "courier_card"];
      if (!allowed.includes(b.paymentMethod)) return fail(422, "PAYMENT_METHOD_NOT_ALLOWED", "Способ оплаты недоступен");
      if (!r.open) return fail(409, "RESTAURANT_CLOSED", "Ресторан закрыт");
      if (db.priceChangeOnNextOrder) {
        db.priceChangeOnNextOrder = false;
        c.cart = { ...c.cart, total: money(c.cart.total.amount + 1000), subtotal: money(c.cart.subtotal.amount + 1000) };
        return fail(409, "PRICE_CHANGED", "Цены изменились");
      }
      if (b.expectedTotal && b.expectedTotal.amount !== c.cart.total.amount) return fail(409, "TOTAL_CHANGED", "Итог изменился");

      const n = db.orders.size + 1;
      const d = new Date();
      const yymmdd = `${String(d.getUTCFullYear()).slice(2)}${String(d.getUTCMonth() + 1).padStart(2, "0")}${String(d.getUTCDate()).padStart(2, "0")}`;
      const online = b.paymentMethod === "online_card";
      const order: Order = {
        id: db.id(), number: `R${RESTAURANTS.indexOf(r) + 1}-${yymmdd}-${String(n).padStart(4, "0")}`, restaurantId: r.id,
        status: online ? "pending_payment" : "accepted", paymentStatus: "pending", paymentMethod: b.paymentMethod, channel: fax ? "staff_fax" : "online",
        items: c.cart.items, subtotal: c.cart.subtotal, discountTotal: c.cart.discountTotal, appliedPromotions: c.cart.appliedPromotions, total: c.cart.total,
        fulfillment: c.cart.fulfillment, createdAt: d.toISOString(),
      };
      db.orders.set(order.id, {
        order, polls: 0, owner: fax ? null : user?.id ?? null, attempt: 0,
        contact: { ...(b.contact ?? {}), ...(!b.contact?.phone && user && !fax ? { phone: user.phone } : {}) }, ...(fax ? { createdBy: user!.id } : {}),
      });
      c.cart = { ...c.cart, status: "ordered", orderId: order.id };
      return respond(HttpResponse.json(order, { status: 201, headers: { Location: `/orders/${order.id}` } }), 201, order);
    }),
    http.get(`${API}/orders/:id`, ({ params, request }) => {
      const m = db.orders.get(params.id as string);
      const u = principal(request);
      // сотрудник читает заказы своих ресторанов, в том числе безвладельческие (факс); чужое — 404
      const staffOk = isStaff(u) && canAccessRestaurant(u, m?.order.restaurantId ?? "");
      if (!m || (m.owner && m.owner !== u?.id && !staffOk) || (!m.owner && m.order.channel === "staff_fax" && !staffOk)) return problem(404, "ORDER_NOT_FOUND", "Заказ не найден");
      advanceOrder(db, m);
      return HttpResponse.json(m.order);
    }),
    http.get(`${API}/orders/:id/eta`, ({ params }) => {
      const m = db.orders.get(params.id as string);
      if (!m) return problem(404, "ORDER_NOT_FOUND", "Заказ не найден");
      const o = m.order;
      const mins = { pending_payment: 25, accepted: 20, cooking: 12, ready: 0 }[o.status as string] ?? 0;
      const at = new Date(Date.now() + mins * 60_000).toISOString();
      return HttpResponse.json({ approximate: true, estimateMinutes: o.fulfillment.type === "delivery" && mins > 0 ? mins + 15 : mins, readyAt: at, ...(o.fulfillment.type === "delivery" ? { deliveryAt: new Date(Date.now() + (mins + 15) * 60_000).toISOString() } : {}) });
    }),
    http.put(`${API}/orders/:id/pickup-slot`, async ({ params, request }) => {
      const m = db.orders.get(params.id as string);
      if (!m) return problem(404, "ORDER_NOT_FOUND", "Заказ не найден");
      const { from, to } = (await request.json()) as { from: string; to: string };
      const f = Date.parse(from), t = Date.parse(to);
      if (m.order.fulfillment.type !== "pickup" || !(f < t) || f < Date.now() - 60_000 || t - f > 4 * 3600_000 || f > Date.now() + 7 * 86400_000) return problem(422, "INVALID_SLOT", "Недопустимый слот");
      if (!["pending_payment", "accepted"].includes(m.order.status)) return problem(409, "ORDER_STATE_CONFLICT", "Заказ уже готовится");
      m.order.pickupSlot = { from, to };
      return HttpResponse.json({ approximate: false, estimateMinutes: Math.max(0, Math.round((f - Date.now()) / 60000)), readyAt: from });
    }),

    /* ---------------- payment ---------------- */
    http.get(`${API}/orders/:id/payment-methods`, ({ params }) => {
      const m = db.orders.get(params.id as string);
      if (!m) return problem(404, "ORDER_NOT_FOUND", "Заказ ещё не дошёл до платёжного сервиса", {}, { "Retry-After": "1" });
      const o = m.order;
      const sel = o.paymentMethod;
      const payable = o.paymentStatus !== "paid" && o.status === "pending_payment";
      const types = o.fulfillment.type === "pickup" ? (["online_card", "pos"] as const) : (["online_card", "courier_card"] as const);
      const title = { online_card: "Картой онлайн", pos: "На кассе", courier_card: "Картой курьеру" };
      return HttpResponse.json(types.map((t) => ({ type: t, title: title[t], enabled: t === sel && t === "online_card" && payable })));
    }),
    http.post(`${API}/orders/:id/payments`, async ({ params, request }) => {
      if (!request.headers.get("Idempotency-Key")) return problem(400, "IDEMPOTENCY_KEY_REQUIRED", "Нужен Idempotency-Key");
      const m = db.orders.get(params.id as string);
      if (!m) return problem(404, "ORDER_NOT_FOUND", "Заказ не найден", {}, { "Retry-After": "1" });
      const o = m.order;
      const b = (await request.json()) as { method: string; returnUrl?: string };
      if (o.paymentMethod !== "online_card" || b.method !== "online_card") return problem(422, "PAYMENT_METHOD_NOT_SUPPORTED", "Способ не поддерживается");
      if (o.paymentStatus === "paid") return problem(409, "ALREADY_PAID", "Заказ уже оплачен");
      if (o.status !== "pending_payment") return problem(409, "ORDER_NOT_PAYABLE", "Заказ нельзя оплатить");
      if (o.paymentStatus === "pending" && m.paymentId && m.authorizedAt) return problem(409, "PAYMENT_IN_PROGRESS", "Платёж обрабатывается", { paymentId: m.paymentId });
      m.attempt++;
      m.paymentId = db.id();
      m.authorizedAt = now();
      o.paymentStatus = "pending";
      const url = `${new URL(request.url).origin}/mock-gateway?orderId=${o.id}&return=${encodeURIComponent(b.returnUrl ?? "/")}`;
      return HttpResponse.json({ id: m.paymentId, orderId: o.id, method: "online_card", status: "pending", attempt: m.attempt, amount: o.total, paymentUrl: url, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() }, { status: 201 });
    }),
    http.get(`${API}/orders/:id/payments/:pid`, ({ params }) => {
      const m = db.orders.get(params.id as string);
      if (!m || m.paymentId !== params.pid) return problem(404, "PAYMENT_NOT_FOUND", "Платёж не найден");
      settle(m);
      return HttpResponse.json(paymentView(m));
    }),

    /* ---------------- delivery ---------------- */
    http.get(`${API}/orders/:id/delivery`, ({ params }) => {
      const m = db.orders.get(params.id as string);
      if (!m || m.order.fulfillment.type !== "delivery" || m.order.status === "pending_payment") return problem(404, "DELIVERY_NOT_FOUND", "Доставка ещё не создана", {}, { "Retry-After": "1" });
      const s = m.order.status;
      const status = s === "delivered" || s === "closed" ? "delivered" : s === "on_delivery" ? "en_route" : s === "ready" ? "assigned" : "searching";
      return HttpResponse.json({ orderId: m.order.id, status, updatedAt: new Date().toISOString(), etaAt: new Date(Date.now() + 20 * 60_000).toISOString(), payOnDelivery: m.order.paymentMethod === "courier_card",
        ...(status === "assigned" || status === "en_route" ? { courier: { name: "Алексей", phone: "+79990001122" } } : {}) });
    }),
    http.get(`${API}/orders/:id/delivery/courier-location`, ({ params }) => {
      const m = db.orders.get(params.id as string);
      if (!m || m.order.status !== "on_delivery") return problem(404, "LOCATION_NOT_AVAILABLE", "Нет данных о курьере");
      return HttpResponse.json({ location: { lat: 55.7577 + (m.polls % 10) * 0.001, lon: 37.6137 + (m.polls % 10) * 0.001 }, updatedAt: new Date().toISOString() });
    }),

    /* ---------------- notification ---------------- */
    http.post(`${API}/notifications/device-tokens`, async ({ request }) => {
      const u = principal(request);
      if (!u) return problem(401, "UNAUTHORIZED", "Нужна авторизация");
      const b = (await request.json()) as { platform: string; token: string; permissionsGranted?: boolean };
      const ex = db.devices.get(b.token);
      const row = { id: ex?.id ?? db.id(), owner: u.id, platform: b.platform, permissionsGranted: b.permissionsGranted ?? true, registeredAt: ex?.registeredAt ?? new Date().toISOString() };
      db.devices.set(b.token, row);
      return HttpResponse.json({ ...row, token: b.token, locale: "ru" }, { status: ex ? 200 : 201 });
    }),
    http.get(`${API}/notifications/device-tokens`, ({ request }) => {
      const u = principal(request);
      if (!u) return problem(401, "UNAUTHORIZED", "Нужна авторизация");
      return HttpResponse.json([...db.devices.entries()].filter(([, d]) => d.owner === u.id).map(([token, d]) => ({ ...d, token: "***", locale: "ru", _t: token })).map(({ _t, ...d }) => d));
    }),
    http.delete(`${API}/notifications/device-tokens/:id`, ({ params, request }) => {
      const u = principal(request);
      if (!u) return problem(401, "UNAUTHORIZED", "Нужна авторизация");
      const e = [...db.devices.entries()].find(([, d]) => d.id === params.id && d.owner === u.id);
      if (!e) return problem(404, "DEVICE_TOKEN_NOT_FOUND", "Устройство не найдено");
      db.devices.delete(e[0]);
      return new HttpResponse(null, { status: 204 });
    }),
  ];
}

/** Завершить платёж на мок-шлюзе (страница /mock-gateway). */
export function settleMockPayment(db: MockDb, orderId: string, ok: boolean) {
  const m = db.orders.get(orderId);
  if (!m) return;
  if (ok) { m.order.paymentStatus = "paid"; m.order.status = "accepted"; }
  else m.order.paymentStatus = "declined";
  m.authorizedAt = undefined;
}
