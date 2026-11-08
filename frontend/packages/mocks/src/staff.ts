import { http, HttpResponse, type HttpHandler } from "msw";
import type { OrderStatus } from "@fp/api";
import type { MockDb, MockOrder, MockPromotion, MockRevision, MockUser } from "./db";
import { API, advanceOrder, canAccessRestaurant, isStaff, paymentView, problem, settle } from "./util";

/**
 * Мок маршрутов персонала по contracts/openapi: очередь и поиск заказов, выдача, отмена, оплата на кассе
 * (orders.yaml, payment.yaml) и управление акциями (loyalty.yaml). Воспроизводит коды ошибок, на которые ветвится
 * web-app-staff (FRONTEND-CONTRACT, UC-STF-*, UC-PRM-*); бэкенд не копирует.
 */
interface Ctx { principal: (req: Request) => MockUser | null; activePromos: () => MockPromotion[] }

const ORDER_NUMBER = /^R\d{1,9}-\d{6}-\d{4,9}$/i;
const STATUSES: OrderStatus[] = ["pending_payment", "accepted", "cooking", "ready", "handed_over", "on_delivery", "delivered", "closed", "not_received", "cancelled"];
const CANCEL_REASONS = ["customer_request", "out_of_stock", "restaurant_closed", "other"];
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

const staffOrder = (m: MockOrder) => ({ ...m.order, ...(m.contact ? { contact: m.contact } : {}), ...(m.createdBy ? { createdBy: m.createdBy } : {}) });

export function createStaffHandlers(db: MockDb, ctx: Ctx): HttpHandler[] {
  const { principal, activePromos } = ctx;

  /** null — можно; иначе готовый ответ 401/403. */
  const needStaff = (req: Request): { u: MockUser } | { res: Response } => {
    const u = principal(req);
    if (!u) return { res: problem(401, "UNAUTHORIZED", "Нужна авторизация") };
    if (!isStaff(u)) return { res: problem(403, "FORBIDDEN", "Только для персонала") };
    return { u };
  };
  /** Заказ доступен сотруднику, если ресторан заказа в его привязке; чужой неотличим от несуществующего (404). */
  const orderFor = (u: MockUser, id: string): MockOrder | undefined => {
    const m = db.orders.get(id);
    return m && canAccessRestaurant(u, m.order.restaurantId) ? m : undefined;
  };
  const notFound = () => problem(404, "ORDER_NOT_FOUND", "Заказ не найден");

  /* ------------------------------ заказы ------------------------------ */
  const queue = http.get(`${API}/orders/queue`, ({ request }) => {
    const a = needStaff(request);
    if ("res" in a) return a.res;
    const sp = new URL(request.url).searchParams;
    const bad = () => problem(400, "INVALID_PARAMETER", "Некорректный параметр");
    const restaurantId = sp.get("restaurantId");
    if (restaurantId !== null && !UUID.test(restaurantId)) return bad();
    const statuses = sp.getAll("status") as OrderStatus[];
    if (statuses.length > 10 || statuses.some((s) => !STATUSES.includes(s))) return bad();
    const fulfillment = sp.get("fulfillment");
    if (fulfillment !== null && fulfillment !== "pickup" && fulfillment !== "delivery") return bad();
    const sort = sp.get("sort") ?? "oldest";
    if (sort !== "oldest" && sort !== "newest") return bad();
    const limit = sp.has("limit") ? Number(sp.get("limit")) : 20, offset = sp.has("offset") ? Number(sp.get("offset")) : 0;
    if (!Number.isInteger(limit) || limit < 1 || limit > 50 || !Number.isInteger(offset) || offset < 0) return bad();

    const u = a.u;
    if (restaurantId && !canAccessRestaurant(u, restaurantId)) return problem(403, "FORBIDDEN", "Нет доступа к ресторану");
    if (!restaurantId && !u.roles.includes("HQ_STAFF") && !(u.restaurants ?? []).length) return problem(403, "FORBIDDEN", "Нет привязки к ресторанам");

    let list = [...db.orders.values()].filter((m) => (restaurantId ? m.order.restaurantId === restaurantId : canAccessRestaurant(u, m.order.restaurantId)));
    list.forEach((m) => advanceOrder(db, m));
    list = list.filter((m) => (statuses.length ? statuses.includes(m.order.status) : m.order.status !== "closed" && m.order.status !== "cancelled"))
      .filter((m) => !fulfillment || m.order.fulfillment.type === fulfillment);
    const dir = sort === "oldest" ? 1 : -1;
    list.sort((x, y) => dir * (Date.parse(x.order.createdAt) - Date.parse(y.order.createdAt) || x.order.id.localeCompare(y.order.id)));
    return HttpResponse.json({ items: list.slice(offset, offset + limit).map(staffOrder), total: list.length, limit, offset });
  });

  const search = http.get(`${API}/orders/search`, ({ request }) => {
    const a = needStaff(request);
    if ("res" in a) return a.res;
    const number = new URL(request.url).searchParams.get("number")?.trim() ?? "";
    if (!number) return problem(400, "INVALID_PARAMETER", "Номер не указан");
    if (!ORDER_NUMBER.test(number)) return problem(400, "INVALID_ORDER_NUMBER", "Номер заказа не соответствует формату");
    const m = [...db.orders.values()].find((x) => x.order.number.toLowerCase() === number.toLowerCase());
    if (!m || !canAccessRestaurant(a.u, m.order.restaurantId)) return notFound();
    advanceOrder(db, m);
    return HttpResponse.json(staffOrder(m));
  });

  const issuance = http.post(`${API}/orders/:id/issuance`, ({ params, request }) => {
    const a = needStaff(request);
    if ("res" in a) return a.res;
    const m = orderFor(a.u, params.id as string);
    if (!m) return notFound();
    settle(m);
    const o = m.order;
    if (o.status === "closed") return HttpResponse.json(o); // повтор — 200 без эффекта
    if (o.fulfillment.type !== "pickup") return problem(422, "NOT_PICKUP_ORDER", "Заказ с доставкой выдаёт курьер");
    if (o.status !== "ready") return problem(409, "ORDER_STATE_CONFLICT", "Заказ не готов к выдаче");
    if (o.paymentStatus !== "paid") return problem(409, "ORDER_NOT_PAID", "Оплата не принята");
    o.status = "closed";
    o.statusReason = "handed_over";
    return HttpResponse.json(o);
  });

  const cancellation = http.post(`${API}/orders/:id/cancellation`, async ({ params, request }) => {
    const a = needStaff(request);
    if ("res" in a) return a.res;
    const m = orderFor(a.u, params.id as string);
    if (!m) return notFound();
    const body = (await request.json().catch(() => ({}))) as { reason?: string };
    if (!body.reason || !CANCEL_REASONS.includes(body.reason)) return problem(400, "VALIDATION_FAILED", "Укажите причину отмены");
    settle(m);
    const o = m.order;
    if (o.status === "cancelled") return HttpResponse.json(o);
    if (o.status !== "pending_payment" && o.status !== "accepted") return problem(409, "ORDER_STATE_CONFLICT", "Заказ уже готовится");
    o.status = "cancelled";
    o.statusReason = body.reason;
    if (o.paymentStatus === "paid") { o.refundStatus = "pending"; m.refund = { requestedAt: new Date().toISOString() }; }
    return HttpResponse.json(o);
  });

  /* ------------------------- оплата на кассе ------------------------- */
  const pos = http.post(`${API}/orders/:id/payments/pos`, async ({ params, request }) => {
    const a = needStaff(request);
    if ("res" in a) return a.res;
    const key = request.headers.get("Idempotency-Key");
    if (!key) return problem(400, "IDEMPOTENCY_KEY_REQUIRED", "Нужен Idempotency-Key");
    const m = orderFor(a.u, params.id as string);
    if (!m) return notFound();
    const raw = await request.text();
    const prev = db.idem.get(`pos:${key}`);
    if (prev) {
      if (prev.sig !== raw) return problem(422, "IDEMPOTENCY_KEY_REUSED", "Ключ уже использован с другим запросом");
      return HttpResponse.json(prev.body as object, { status: prev.status, headers: { "Idempotent-Replay": "true" } });
    }
    let body: { tender?: string } = {};
    try { body = JSON.parse(raw) as { tender?: string }; } catch { /* пустое тело */ }
    if (body.tender !== "card" && body.tender !== "cash") return problem(400, "VALIDATION_FAILED", "Укажите способ: card или cash");
    settle(m);
    const o = m.order;
    if (o.paymentMethod !== "pos") return problem(422, "PAYMENT_METHOD_NOT_ALLOWED", "Способ оплаты заказа — не касса");
    if (o.paymentStatus === "paid") return problem(409, "ALREADY_PAID", "Заказ уже оплачен", { paymentId: m.paymentId });
    if (o.status === "cancelled" || o.status === "closed") return problem(409, "ORDER_NOT_PAYABLE", "Заказ нельзя оплатить");
    if (o.paymentStatus === "unknown") return problem(409, "PAYMENT_STATUS_UNKNOWN", "Исход предыдущей оплаты неизвестен", { paymentId: m.paymentId });
    if (m.pos && o.paymentStatus === "pending") return problem(409, "PAYMENT_IN_PROGRESS", "Платёж обрабатывается", { paymentId: m.paymentId });

    // исход задаётся копейками суммы, как у онлайн-мока: …13 — отказ кассы, …99 — «тишина» (итог только сверкой)
    const kop = o.total.amount % 100;
    if (kop === 13) { m.attempt++; return problem(402, "PAYMENT_DECLINED", "Касса отказала в операции"); }
    m.attempt++;
    m.paymentId = db.id();
    const unknown = kop === 99;
    m.pos = { tender: body.tender, settleAt: Date.now() + (unknown ? db.posDelayMs * 3 : db.posDelayMs), outcome: unknown ? "unknown" : "paid" };
    o.paymentStatus = unknown ? "unknown" : "pending";
    const view = paymentView(m);
    const status = unknown ? 202 : 201;
    db.idem.set(`pos:${key}`, { sig: raw, status, body: view });
    return HttpResponse.json(view, { status, headers: status === 201 ? { Location: `/orders/${o.id}/payments/${m.paymentId}` } : {} });
  });

  /* ------------------------------ акции ------------------------------ */
  type Access = { ok: true } | { ok: false; res: Response };
  const forbid = (code: string) => ({ ok: false, res: problem(403, code, "Недостаточно прав") }) as const;
  /** Решение auth /authorize: HQ — всё; владелец — только local своих ресторанов; остальные роли — нет. */
  const promoAccess = (u: MockUser, scope: string, restaurantId?: string): Access => {
    if (u.roles.includes("HQ_STAFF")) return { ok: true };
    if (!u.roles.includes("FRANCHISE_OWNER")) return forbid("ROLE_REQUIRED");
    if (scope === "national") return forbid("SCOPE_NOT_ALLOWED");
    if (!restaurantId || !(u.restaurants ?? []).includes(restaurantId)) return forbid("NOT_BOUND_TO_RESTAURANT");
    return { ok: true };
  };
  const needPromoUser = (req: Request): { u: MockUser } | { res: Response } => {
    const u = principal(req);
    if (!u) return { res: problem(401, "UNAUTHORIZED", "Нужна авторизация") };
    if (!u.roles.includes("HQ_STAFF") && !u.roles.includes("FRANCHISE_OWNER")) return { res: problem(403, "ROLE_REQUIRED", "Нужна роль владельца или HQ") };
    return { u };
  };

  /** PromotionInput → ошибка 422/400 или готовые поля. */
  type Fields = Omit<MockPromotion, "id" | "status" | "version" | "createdAt" | "updatedAt" | "createdBy">;
  const parse = (b: Record<string, unknown>): { fields: Fields } | { res: Response } => {
    const invalid = (m: string) => ({ res: problem(422, "PROMOTION_INVALID", m) });
    const shape = (m: string) => ({ res: problem(400, "VALIDATION_FAILED", m) });
    if (typeof b.title !== "string" || (b.scope !== "local" && b.scope !== "national") || typeof b.startsAt !== "string" || typeof b.endsAt !== "string"
      || typeof b.discount !== "object" || b.discount === null) return shape("Не заполнены обязательные поля");
    const d = b.discount as { type?: unknown; value?: unknown };
    if ((d.type !== "percent" && d.type !== "fixed") || typeof d.value !== "number") return shape("Некорректная скидка");
    const title = b.title.trim();
    if (title.length < 1 || title.length > 120) return invalid("Название: от 1 до 120 символов");
    if (typeof b.description === "string" && b.description.length > 1000) return invalid("Описание: не более 1000 символов");
    const s = Date.parse(b.startsAt), e = Date.parse(b.endsAt);
    if (Number.isNaN(s) || Number.isNaN(e) || e <= s) return invalid("Окончание должно быть позже начала");
    if (!Number.isInteger(d.value)) return invalid("Скидка — целое число");
    if (d.type === "percent" && (d.value < 1 || d.value > 10000)) return invalid("Процент: от 0,01 % до 100 %");
    if (d.type === "fixed" && (d.value < 1 || d.value > 1e10)) return invalid("Фиксированная скидка: больше 0");
    if (b.scope === "local" && !b.restaurantId) return invalid("Для локальной акции нужен ресторан");
    if (b.scope === "national" && b.restaurantId) return invalid("Национальная акция не привязана к ресторану");
    const c = (b.conditions ?? {}) as { fulfillment?: unknown; menuItemIds?: unknown; minSubtotal?: unknown; stackable?: unknown };
    if (c.fulfillment !== undefined && c.fulfillment !== "pickup" && c.fulfillment !== "delivery") return invalid("Способ получения: pickup или delivery");
    if (c.menuItemIds !== undefined && (!Array.isArray(c.menuItemIds) || c.menuItemIds.length > 100 || new Set(c.menuItemIds).size !== c.menuItemIds.length)) return invalid("Позиции: не более 100, без повторов");
    if (c.minSubtotal !== undefined && (!Number.isInteger(c.minSubtotal) || (c.minSubtotal as number) < 0 || (c.minSubtotal as number) > 1e10)) return invalid("Минимальная сумма некорректна");
    const priority = b.priority === undefined ? 0 : b.priority;
    if (!Number.isInteger(priority) || (priority as number) < 0 || (priority as number) > 1000) return invalid("Приоритет: от 0 до 1000");
    return {
      fields: {
        scope: b.scope, ...(b.restaurantId ? { restaurantId: String(b.restaurantId) } : {}), title, ...(typeof b.description === "string" && b.description ? { description: b.description } : {}),
        discount: { type: d.type, value: d.value }, currency: typeof b.currency === "string" ? b.currency : "RUB",
        conditions: {
          ...(c.fulfillment ? { fulfillment: c.fulfillment as "pickup" | "delivery" } : {}), ...(Array.isArray(c.menuItemIds) && c.menuItemIds.length ? { menuItemIds: c.menuItemIds as string[] } : {}),
          ...(c.minSubtotal !== undefined ? { minSubtotal: c.minSubtotal as number } : {}), stackable: c.stackable === true,
        },
        priority: priority as number, startsAt: b.startsAt, endsAt: b.endsAt,
      },
    };
  };

  /** E2: две нестекуемые акции одного приоритета с пересечением по времени, позициям и охвату не активны вместе. */
  const conflicts = (p: MockPromotion): string[] =>
    activePromos().filter((o) => o.id !== p.id && !o.conditions.stackable && !p.conditions.stackable && o.priority === p.priority
      && Date.parse(o.startsAt) < Date.parse(p.endsAt) && Date.parse(p.startsAt) < Date.parse(o.endsAt)
      && (o.scope === "national" || p.scope === "national" || o.restaurantId === p.restaurantId)
      && (!o.conditions.menuItemIds?.length || !p.conditions.menuItemIds?.length || o.conditions.menuItemIds.some((i) => p.conditions.menuItemIds!.includes(i))))
      .map((o) => o.id);

  const record = (p: MockPromotion, action: MockRevision["action"], actor: string) => {
    const hist = db.promotionHistory.get(p.id) ?? [];
    hist.push({ version: p.version, action, actor, at: p.updatedAt, snapshot: { ...p } });
    db.promotionHistory.set(p.id, hist);
  };
  const touch = (p: MockPromotion) => { p.version++; p.updatedAt = new Date().toISOString(); };

  const manage = http.get(`${API}/promotions/manage`, ({ request }) => {
    const a = needPromoUser(request);
    if ("res" in a) return a.res;
    const sp = new URL(request.url).searchParams;
    const bad = () => problem(400, "INVALID_PARAMETER", "Некорректный параметр");
    const statuses = sp.getAll("status");
    if (statuses.length > 3 || statuses.some((s) => !["draft", "active", "completed"].includes(s))) return bad();
    const scope = sp.get("scope");
    if (scope !== null && scope !== "local" && scope !== "national") return bad();
    const restaurantId = sp.get("restaurantId");
    if (restaurantId !== null && !UUID.test(restaurantId)) return bad();
    const limit = sp.has("limit") ? Number(sp.get("limit")) : 20, offset = sp.has("offset") ? Number(sp.get("offset")) : 0;
    if (!Number.isInteger(limit) || limit < 1 || limit > 50 || !Number.isInteger(offset) || offset < 0) return bad();
    const u = a.u, hq = u.roles.includes("HQ_STAFF");
    if (!hq) {
      if (scope === "national") return problem(403, "SCOPE_NOT_ALLOWED", "Недостаточно прав");
      if (restaurantId && !(u.restaurants ?? []).includes(restaurantId)) return problem(403, "NOT_BOUND_TO_RESTAURANT", "Недостаточно прав");
    }
    const list = [...db.promotions.values()]
      .filter((p) => hq || (p.scope === "local" && (u.restaurants ?? []).includes(p.restaurantId ?? "")))
      .filter((p) => !statuses.length || statuses.includes(p.status))
      .filter((p) => !scope || p.scope === scope)
      .filter((p) => !restaurantId || p.restaurantId === restaurantId)
      .sort((x, y) => Date.parse(y.updatedAt) - Date.parse(x.updatedAt) || x.id.localeCompare(y.id));
    return HttpResponse.json({ items: list.slice(offset, offset + limit), total: list.length, limit, offset });
  });

  const create = http.post(`${API}/promotions`, async ({ request }) => {
    const a = needPromoUser(request);
    if ("res" in a) return a.res;
    const key = request.headers.get("Idempotency-Key");
    if (!key) return problem(400, "IDEMPOTENCY_KEY_REQUIRED", "Нужен Idempotency-Key");
    const raw = await request.text();
    const prev = db.idem.get(`promo:${key}`);
    if (prev) {
      if (prev.sig !== raw) return problem(422, "IDEMPOTENCY_KEY_REUSED", "Ключ уже использован с другим запросом");
      return HttpResponse.json(prev.body as object, { status: prev.status, headers: { "Idempotent-Replay": "true" } });
    }
    let body: Record<string, unknown>;
    try { body = JSON.parse(raw) as Record<string, unknown>; } catch { return problem(400, "INVALID_JSON", "Некорректный JSON"); }
    const r = parse(body);
    if ("res" in r) return r.res;
    const acc = promoAccess(a.u, r.fields.scope, r.fields.restaurantId);
    if (!acc.ok) return acc.res;
    const now = new Date().toISOString();
    const p: MockPromotion = { id: db.id(), ...r.fields, status: "draft", version: 1, createdAt: now, updatedAt: now, createdBy: a.u.id };
    db.promotions.set(p.id, p);
    record(p, "created", a.u.id);
    db.idem.set(`promo:${key}`, { sig: raw, status: 201, body: p });
    return HttpResponse.json(p, { status: 201, headers: { Location: `/promotions/${p.id}` } });
  });

  /** Общая прелюдия операций над существующей акцией: токен → роль → 404 → охват. */
  const withPromo = (req: Request, id: string): { u: MockUser; p: MockPromotion } | { res: Response } => {
    const a = needPromoUser(req);
    if ("res" in a) return a;
    const p = db.promotions.get(id);
    if (!p) return { res: problem(404, "PROMOTION_NOT_FOUND", "Акция не найдена") };
    const acc = promoAccess(a.u, p.scope, p.restaurantId);
    if (!acc.ok) return { res: acc.res };
    return { u: a.u, p };
  };

  const get = http.get(`${API}/promotions/:id`, ({ params, request }) => {
    const x = withPromo(request, params.id as string);
    return "res" in x ? x.res : HttpResponse.json(x.p);
  });

  const put = http.put(`${API}/promotions/:id`, async ({ params, request }) => {
    const x = withPromo(request, params.id as string);
    if ("res" in x) return x.res;
    const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
    if (!body) return problem(400, "INVALID_JSON", "Некорректный JSON");
    const r = parse(body);
    if ("res" in r) return r.res;
    // попытка выйти за пределы охвата — 403, как в createPromotion; смена охвата — 422
    const acc = promoAccess(x.u, r.fields.scope, r.fields.restaurantId);
    if (!acc.ok) return acc.res;
    const p = x.p;
    if (p.status === "completed") return problem(409, "PROMOTION_COMPLETED", "Акция завершена и не меняется");
    if (r.fields.scope !== p.scope || r.fields.restaurantId !== p.restaurantId) return problem(422, "SCOPE_IMMUTABLE", "Охват акции не меняется");
    const next = { ...p, ...r.fields, conditions: r.fields.conditions };
    if (p.status === "active") {
      if (Date.parse(next.endsAt) <= Date.now()) return problem(422, "PROMOTION_EXPIRED", "Срок акции истёк");
      const c = conflicts(next);
      if (c.length) return problem(409, "PROMOTION_CONFLICT", "Акция конфликтует с действующей", { conflictingPromotionIds: c });
    }
    Object.assign(p, next);
    touch(p);
    record(p, "updated", x.u.id);
    return HttpResponse.json(p);
  });

  const del = http.delete(`${API}/promotions/:id`, ({ params, request }) => {
    const x = withPromo(request, params.id as string);
    if ("res" in x) return x.res;
    if (x.p.status !== "draft") return problem(409, "PROMOTION_NOT_DRAFT", "Удалить можно только черновик");
    db.promotions.delete(x.p.id);
    db.promotionHistory.delete(x.p.id);
    return new HttpResponse(null, { status: 204 });
  });

  const activation = http.post(`${API}/promotions/:id/activation`, ({ params, request }) => {
    const x = withPromo(request, params.id as string);
    if ("res" in x) return x.res;
    const p = x.p;
    if (p.status === "active") return HttpResponse.json(p);
    if (p.status === "completed") return problem(409, "PROMOTION_COMPLETED", "Акция завершена и не меняется");
    if (Date.parse(p.endsAt) <= Date.now()) return problem(422, "PROMOTION_EXPIRED", "Срок акции истёк");
    const c = conflicts(p);
    if (c.length) return problem(409, "PROMOTION_CONFLICT", "Акция конфликтует с действующей", { conflictingPromotionIds: c });
    p.status = "active";
    touch(p);
    record(p, "activated", x.u.id);
    return HttpResponse.json(p);
  });

  const completion = http.post(`${API}/promotions/:id/completion`, ({ params, request }) => {
    const x = withPromo(request, params.id as string);
    if ("res" in x) return x.res;
    const p = x.p;
    if (p.status === "completed") return HttpResponse.json(p);
    p.status = "completed";
    touch(p);
    record(p, "completed", x.u.id);
    return HttpResponse.json(p);
  });

  const history = http.get(`${API}/promotions/:id/history`, ({ params, request }) => {
    const x = withPromo(request, params.id as string);
    if ("res" in x) return x.res;
    return HttpResponse.json((db.promotionHistory.get(x.p.id) ?? []).map((h) => ({ version: h.version, action: h.action, actorId: h.actor, occurredAt: h.at, snapshot: h.snapshot })));
  });

  /* `/promotions/manage` раньше `/promotions/:id` (в реальном API id — UUID, коллизии нет) */
  return [queue, search, issuance, cancellation, pos, manage, create, history, activation, completion, get, put, del];
}
