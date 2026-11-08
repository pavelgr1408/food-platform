import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { setupServer } from "msw/node";
import { createApi, isApiError, type Api, type StaffOrder } from "@fp/api";
import { RESTAURANTS, createDb, setActiveDb } from "./db";
import { createHandlers } from "./handlers";

/** Контракт мок-бэкенда персонала (orders.yaml, payment.yaml, loyalty.yaml): на эти коды ветвится web-app-staff. */
const db = createDb();
const server = setupServer(...createHandlers(db));
const R1 = RESTAURANTS[0]!.id, R2 = RESTAURANTS[1]!.id;

beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
afterAll(() => server.close());
beforeEach(() => { Object.assign(db, createDb(), { advanceEvery: 0, posDelayMs: 0 }); setActiveDb(db); });
afterEach(() => server.resetHandlers());

const newApi = (): Api => {
  const mem = new Map<string, string>();
  return createApi({ baseUrl: "http://test.local/api", storage: { getItem: (k) => mem.get(k) ?? null, setItem: (k, v) => void mem.set(k, v), removeItem: (k) => void mem.delete(k) } });
};
const as = async (phone: string) => { const a = newApi(); await a.auth.login(phone, "secret1"); return a; };
const staff = () => as("+79990000010"), owner = () => as("+79990000011"), hq = () => as("+79990000012"), customer = () => as("+79990000001");
const code = async (p: Promise<unknown>) => { try { await p; return "ok"; } catch (e) { return isApiError(e) ? e.code : String(e); } };

async function faxOrder(api: Api, o: { restaurant?: string; item?: string; qty?: number; method?: "pos" | "online_card"; fulfillment?: "pickup" } = {}) {
  const rid = o.restaurant ?? R1;
  const cart = await api.carts.create(rid, { guest: true });
  const c2 = await api.carts.putItems(cart.id, [{ menuItemId: o.item ?? "i-burger", quantity: o.qty ?? 1, modifierIds: [] }]);
  await api.carts.putFulfillment(cart.id, { type: o.fulfillment ?? "pickup" });
  return api.orders.create({ cartId: cart.id, channel: "staff_fax", paymentMethod: o.method ?? "pos", contact: { phone: "+79995550101", name: "Анна" }, expectedTotal: c2.total }, api.newIdempotencyKey());
}

describe("заказ по факсу", () => {
  it("оформляет сотрудник своего ресторана: заказ без владельца, с контактом и createdBy", async () => {
    const s = await staff();
    const { order } = await faxOrder(s);
    expect(order.channel).toBe("staff_fax");
    expect(order.status).toBe("accepted");
    const found = await s.orders.search(order.number);
    expect(found.contact?.phone).toBe("+79995550101");
    expect(found.createdBy).toBe("u-staff");
    // клиент (даже авторизованный) чужой факс-заказ не видит
    const c = await customer();
    expect(await code(c.orders.get(order.id))).toBe("ORDER_NOT_FOUND");
  });
  it("запрещает: онлайн-оплату, чужой ресторан, клиента без токена персонала; требует телефон", async () => {
    const s = await staff();
    expect(await code(faxOrder(s, { method: "online_card" }))).toBe("PAYMENT_METHOD_NOT_ALLOWED");
    expect(await code(faxOrder(s, { restaurant: R2, item: "i-s" }))).toBe("FORBIDDEN");
    expect(await code(faxOrder(await customer()))).toBe("FORBIDDEN");
    const cart = await s.carts.create(R1, { guest: true });
    await s.carts.putItems(cart.id, [{ menuItemId: "i-burger", quantity: 1, modifierIds: [] }]);
    await s.carts.putFulfillment(cart.id, { type: "pickup" });
    expect(await code(s.orders.create({ cartId: cart.id, channel: "staff_fax", paymentMethod: "pos" }, s.newIdempotencyKey()))).toBe("CONTACT_REQUIRED");
  });
});

describe("очередь GET /orders/queue", () => {
  it("по умолчанию — незавершённые, старые первыми; фильтры и страницы", async () => {
    const s = await staff();
    const a = (await faxOrder(s)).order, b = (await faxOrder(s, { item: "i-fries" })).order;
    db.orders.get(a.id)!.order.createdAt = "2026-11-08T10:00:00.000Z";
    db.orders.get(b.id)!.order.createdAt = "2026-11-08T11:00:00.000Z";
    const page = await s.orders.queue({ restaurantId: R1 });
    expect(page.items.map((o) => o.number)).toEqual([a.number, b.number]);
    expect(page.total).toBe(2);
    expect((await s.orders.queue({ restaurantId: R1, sort: "newest", limit: 1 })).items[0]!.number).toBe(b.number);
    expect((await s.orders.queue({ restaurantId: R1, limit: 1, offset: 1 })).items[0]!.number).toBe(b.number);
    expect((await s.orders.queue({ restaurantId: R1, fulfillment: "delivery" })).total).toBe(0);
    db.orders.get(a.id)!.order.status = "cooking";
    expect((await s.orders.queue({ restaurantId: R1, status: ["cooking"] })).items.map((o) => o.id)).toEqual([a.id]);
    await s.orders.cancel(b.id, "other");
    expect((await s.orders.queue({ restaurantId: R1 })).total).toBe(1); // отменённый ушёл из очереди
    expect((await s.orders.queue({ restaurantId: R1, status: ["cancelled", "cooking"] })).total).toBe(2); // повторяющийся status
  });
  it("права и границы: гость 401, клиент 403, чужой ресторан 403, мусор 400", async () => {
    expect(await code(newApi().orders.queue())).toBe("UNAUTHORIZED");
    expect(await code((await customer()).orders.queue())).toBe("FORBIDDEN");
    const s = await staff();
    expect(await code(s.orders.queue({ restaurantId: R2 }))).toBe("FORBIDDEN");
    expect(await code(s.orders.queue({ limit: 51 }))).toBe("INVALID_PARAMETER");
    expect(await code(s.orders.queue({ limit: 0 }))).toBe("INVALID_PARAMETER");
    expect(await code(s.orders.queue({ offset: -1 }))).toBe("INVALID_PARAMETER");
    expect(await code(s.orders.queue({ restaurantId: "not-uuid" }))).toBe("INVALID_PARAMETER");
    expect(await code(s.orders.queue({ status: ["nope" as never] }))).toBe("INVALID_PARAMETER");
    expect(await code(s.orders.queue({ sort: "random" as never }))).toBe("INVALID_PARAMETER");
    // сотрудник без привязки и без restaurantId — 403, а не пустой список
    db.users.set("+79990000020", { id: "u-lonely", phone: "+79990000020", password: "secret1", roles: ["STAFF"], restaurants: [] });
    expect(await code((await as("+79990000020")).orders.queue())).toBe("FORBIDDEN");
    // HQ видит всё
    expect((await (await hq()).orders.queue({ restaurantId: R2 })).total).toBe(0);
  });
});

describe("поиск GET /orders/search", () => {
  it("регистр не важен; формат 400; чужой ресторан — 404 как несуществующий", async () => {
    const s = await staff();
    const { order } = await faxOrder(s);
    expect((await s.orders.search(order.number.toLowerCase())).id).toBe(order.id);
    expect(await code(s.orders.search("abc"))).toBe("INVALID_ORDER_NUMBER");
    expect(await code(s.orders.search("R1-000000-0001"))).toBe("ORDER_NOT_FOUND");
    db.orders.get(order.id)!.order.restaurantId = R2;
    expect(await code(s.orders.search(order.number))).toBe("ORDER_NOT_FOUND");
    expect((await (await hq()).orders.search(order.number)).id).toBe(order.id);
    expect(await code((await customer()).orders.search(order.number))).toBe("FORBIDDEN");
  });
});

describe("оплата на кассе POST /orders/{id}/payments/pos", () => {
  it("201 → pending → paid с чеком; повтор с тем же ключом = тот же ответ; ALREADY_PAID", async () => {
    const s = await staff();
    const { order } = await faxOrder(s);
    const key = s.newIdempotencyKey();
    const r1 = await s.payments.pos(order.id, "cash", key);
    expect(r1.status).toBe(201);
    expect(r1.payment.tender).toBe("cash");
    const r2 = await s.payments.pos(order.id, "cash", key);
    expect(r2.payment.id).toBe(r1.payment.id);
    const found = await s.orders.search(order.number);
    expect(found.paymentStatus).toBe("paid");
    const p = await s.payments.get(order.id, r1.payment.id);
    expect(p.status).toBe("paid");
    expect(p.fiscalData).toBeTruthy();
    expect(await code(s.payments.pos(order.id, "card", s.newIdempotencyKey()))).toBe("ALREADY_PAID");
  });
  it("валидация: ключ обязателен, tender, ключ с другим телом, не-pos заказ, не персонал", async () => {
    const s = await staff();
    const { order } = await faxOrder(s);
    const key = s.newIdempotencyKey();
    await s.payments.pos(order.id, "card", key);
    expect(await code(s.payments.pos(order.id, "cash", key))).toBe("IDEMPOTENCY_KEY_REUSED");
    expect(await code(s.payments.pos(order.id, "bitcoin" as never, s.newIdempotencyKey()))).toBe("VALIDATION_FAILED");
    expect(await code((await customer()).payments.pos(order.id, "card", s.newIdempotencyKey()))).toBe("FORBIDDEN");
    const raw = await fetch(`http://test.local/api/orders/${order.id}/payments/pos`, { method: "POST", headers: { Authorization: `Bearer ${await s.session.getAccessToken()}`, "Content-Type": "application/json" }, body: JSON.stringify({ tender: "card" }) });
    expect((await raw.json()).code).toBe("IDEMPOTENCY_KEY_REQUIRED");
    // заказ с оплатой курьеру кассой не оплатить
    db.orders.get(order.id)!.order.paymentMethod = "courier_card"; db.orders.get(order.id)!.order.paymentStatus = "pending";
    db.orders.get(order.id)!.pos = undefined;
    expect(await code(s.payments.pos(order.id, "card", s.newIdempotencyKey()))).toBe("PAYMENT_METHOD_NOT_ALLOWED");
  });
  it("копейки …13 — отказ кассы (402), …99 — исход неизвестен (202, повторно нельзя)", async () => {
    const s = await staff();
    const declined = (await faxOrder(s, { item: "i-fries", qty: 1 })).order; // 9900 → правим сумму
    db.orders.get(declined.id)!.order.total = { amount: 24913, currency: "RUB" };
    expect(await code(s.payments.pos(declined.id, "card", s.newIdempotencyKey()))).toBe("PAYMENT_DECLINED");
    expect((await s.orders.search(declined.number)).paymentStatus).toBe("pending"); // заказ остаётся неоплаченным
    const silent = (await faxOrder(s)).order;
    db.orders.get(silent.id)!.order.total = { amount: 24999, currency: "RUB" };
    db.posDelayMs = 60_000;
    const r = await s.payments.pos(silent.id, "card", s.newIdempotencyKey());
    expect(r.status).toBe(202);
    expect(r.payment.status).toBe("unknown");
    expect(await code(s.payments.pos(silent.id, "card", s.newIdempotencyKey()))).toBe("PAYMENT_STATUS_UNKNOWN");
  });
});

describe("выдача и отмена", () => {
  it("выдача: ORDER_STATE_CONFLICT → ORDER_NOT_PAID → 200 closed → повтор 200", async () => {
    const s = await staff();
    const { order } = await faxOrder(s);
    expect(await code(s.orders.issue(order.id))).toBe("ORDER_STATE_CONFLICT"); // ещё не готов
    db.orders.get(order.id)!.order.status = "ready";
    expect(await code(s.orders.issue(order.id))).toBe("ORDER_NOT_PAID");
    await s.payments.pos(order.id, "card", s.newIdempotencyKey());
    const done = await s.orders.issue(order.id);
    expect(done.status).toBe("closed");
    expect((await s.orders.issue(order.id)).status).toBe("closed");
    expect(await code(s.orders.cancel(order.id, "other"))).toBe("ORDER_STATE_CONFLICT"); // закрытый не отменить
  });
  it("выдача: доставка 422 NOT_PICKUP_ORDER; чужой ресторан 404; клиент 403", async () => {
    const s = await staff();
    const { order } = await faxOrder(s);
    db.orders.get(order.id)!.order.fulfillment = { type: "delivery" };
    expect(await code(s.orders.issue(order.id))).toBe("NOT_PICKUP_ORDER");
    db.orders.get(order.id)!.order.restaurantId = R2;
    expect(await code(s.orders.issue(order.id))).toBe("ORDER_NOT_FOUND");
    expect(await code((await customer()).orders.issue(order.id))).toBe("FORBIDDEN");
  });
  it("отмена: причина обязательна; accepted → cancelled; cooking нельзя; оплаченный запускает возврат pending → refunded", async () => {
    const s = await staff();
    const a = (await faxOrder(s)).order;
    expect(await code(s.orders.cancel(a.id, "nope" as never))).toBe("VALIDATION_FAILED");
    const c = await s.orders.cancel(a.id, "customer_request");
    expect(c.status).toBe("cancelled");
    expect(c.statusReason).toBe("customer_request");
    expect((await s.orders.cancel(a.id, "other")).status).toBe("cancelled"); // повтор — 200
    const b = (await faxOrder(s)).order;
    db.orders.get(b.id)!.order.status = "cooking";
    expect(await code(s.orders.cancel(b.id, "other"))).toBe("ORDER_STATE_CONFLICT");
    const paid = (await faxOrder(s)).order;
    await s.payments.pos(paid.id, "card", s.newIdempotencyKey());
    await s.orders.search(paid.number); // settle
    const refunded = await s.orders.cancel(paid.id, "out_of_stock");
    expect(refunded.refundStatus).toBe("pending");
    db.orders.get(paid.id)!.refund!.requestedAt = new Date(Date.now() - 5000).toISOString();
    expect((await s.orders.search(paid.number)).refundStatus).toBe("refunded");
  });
});

describe("акции: список управления и жизненный цикл", () => {
  const base = (over: Partial<Parameters<Api["promotions"]["create"]>[0]> = {}) => ({
    title: "Скидка", scope: "local" as const, restaurantId: R1, discount: { type: "percent" as const, value: 1500 }, currency: "RUB",
    startsAt: "2026-11-01T09:00:00.000Z", endsAt: "2027-01-10T09:00:00.000Z", priority: 20, conditions: { stackable: false }, ...over,
  });

  it("охват: владелец видит только local своих, HQ — все; кассир и клиент — 403 ROLE_REQUIRED", async () => {
    const o = await owner(), h = await hq();
    expect((await o.promotions.manage()).items.map((p) => p.id)).toEqual(["p-10"]);
    expect((await h.promotions.manage()).total).toBe(2);
    expect(await code(o.promotions.manage({ scope: "national" }))).toBe("SCOPE_NOT_ALLOWED");
    expect(await code(o.promotions.manage({ restaurantId: R2 }))).toBe("NOT_BOUND_TO_RESTAURANT");
    expect(await code((await staff()).promotions.manage())).toBe("ROLE_REQUIRED");
    expect(await code((await customer()).promotions.manage())).toBe("ROLE_REQUIRED");
    expect(await code(newApi().promotions.manage())).toBe("UNAUTHORIZED");
    expect(await code(o.promotions.manage({ limit: 99 }))).toBe("INVALID_PARAMETER");
    expect((await h.promotions.manage({ status: ["draft"] })).total).toBe(0);
  });
  it("создание: Idempotency-Key, повтор, валидация 422 и охват 403", async () => {
    const o = await owner();
    const key = o.newIdempotencyKey();
    const p = await o.promotions.create(base(), key);
    expect(p.status).toBe("draft");
    expect(p.version).toBe(1);
    expect((await o.promotions.create(base(), key)).id).toBe(p.id);
    expect(await code(o.promotions.create(base({ title: "Другая" }), key))).toBe("IDEMPOTENCY_KEY_REUSED");
    expect((await o.promotions.manage()).total).toBe(2);
    for (const bad of [base({ title: "  " }), base({ discount: { type: "percent", value: 10001 } }), base({ discount: { type: "fixed", value: 0 } }), base({ endsAt: "2026-11-01T09:00:00.000Z" }), base({ restaurantId: undefined }), base({ priority: 1001 })])
      expect(await code(o.promotions.create(bad, o.newIdempotencyKey()))).toBe("PROMOTION_INVALID");
    expect(await code(o.promotions.create(base({ scope: "national", restaurantId: undefined }), o.newIdempotencyKey()))).toBe("SCOPE_NOT_ALLOWED");
    expect(await code(o.promotions.create(base({ restaurantId: R2 }), o.newIdempotencyKey()))).toBe("NOT_BOUND_TO_RESTAURANT");
    expect((await (await hq()).promotions.create(base({ scope: "national", restaurantId: undefined }), o.newIdempotencyKey())).scope).toBe("national");
  });
  it("активация: конфликт 409 с id; повтор 200; завершение; завершённую не менять; удалить можно только черновик", async () => {
    const o = await owner();
    const a = await o.promotions.create(base({ title: "A" }), o.newIdempotencyKey());
    const b = await o.promotions.create(base({ title: "B" }), o.newIdempotencyKey());
    expect((await o.promotions.activate(a.id)).status).toBe("active");
    expect((await o.promotions.activate(a.id)).status).toBe("active"); // повтор
    try { await o.promotions.activate(b.id); throw new Error("ждали конфликт"); } catch (e) {
      expect(isApiError(e) && e.code).toBe("PROMOTION_CONFLICT");
      expect(isApiError(e) && e.extra.conflictingPromotionIds).toEqual([a.id]);
    }
    // приоритет разводит акции
    expect((await o.promotions.update(b.id, base({ title: "B", priority: 21 }))).version).toBe(2);
    expect((await o.promotions.activate(b.id)).status).toBe("active");
    expect(await code(o.promotions.remove(a.id))).toBe("PROMOTION_NOT_DRAFT");
    const done = await o.promotions.complete(a.id);
    expect(done.status).toBe("completed");
    expect((await o.promotions.complete(a.id)).status).toBe("completed"); // повтор
    expect(await code(o.promotions.update(a.id, base()))).toBe("PROMOTION_COMPLETED");
    expect(await code(o.promotions.activate(a.id))).toBe("PROMOTION_COMPLETED");
    const hist = await o.promotions.history(a.id);
    expect(hist.map((h) => h.action)).toEqual(["created", "activated", "completed"]);
    const d = await o.promotions.create(base({ title: "Черновик" }), o.newIdempotencyKey());
    await o.promotions.remove(d.id);
    expect(await code(o.promotions.get(d.id))).toBe("PROMOTION_NOT_FOUND");
    expect(await code(o.promotions.remove(d.id))).toBe("PROMOTION_NOT_FOUND"); // повтор удаления
  });
  it("охват нельзя менять (422), истёкшую не активировать, чужую не открыть", async () => {
    const o = await owner(), h = await hq();
    const p = await o.promotions.create(base(), o.newIdempotencyKey());
    expect(await code(h.promotions.update(p.id, base({ scope: "national", restaurantId: undefined })))).toBe("SCOPE_IMMUTABLE");
    expect(await code(o.promotions.update(p.id, base({ scope: "national", restaurantId: undefined })))).toBe("SCOPE_NOT_ALLOWED");
    const old = await o.promotions.create(base({ startsAt: "2026-01-01T00:00:00.000Z", endsAt: "2026-02-01T00:00:00.000Z" }), o.newIdempotencyKey());
    expect(await code(o.promotions.activate(old.id))).toBe("PROMOTION_EXPIRED");
    expect(await code(o.promotions.get("p-fx"))).toBe("SCOPE_NOT_ALLOWED"); // национальная акция владельцу недоступна
  });
  it("активная акция меняет цену корзины, завершённая — перестаёт", async () => {
    const o = await owner();
    const s = await staff();
    const promo = await o.promotions.create(base({ discount: { type: "fixed", value: 5000 }, priority: 50 }), o.newIdempotencyKey());
    const cart = async () => { const c = await s.carts.create(R1, { guest: true }); return s.carts.putItems(c.id, [{ menuItemId: "i-burger", quantity: 1, modifierIds: [] }]); };
    expect((await cart()).discountTotal.amount).toBe(0); // черновик не применяется
    await o.promotions.activate(promo.id);
    const c = await cart();
    expect(c.discountTotal.amount).toBe(5000);
    expect(c.appliedPromotions[0]!.title).toBe("Скидка");
    await o.promotions.complete(promo.id);
    expect((await cart()).discountTotal.amount).toBe(0);
  });
});

describe("типы ответов", () => {
  it("очередь возвращает StaffOrder с контактом", async () => {
    const s = await staff();
    await faxOrder(s);
    const o: StaffOrder = (await s.orders.queue({ restaurantId: R1 })).items[0]!;
    expect(o.contact?.name).toBe("Анна");
  });
});
