import { HttpResponse } from "msw";
import type { OrderStatus } from "@fp/api";
import type { MockDb, MockOrder, MockUser } from "./db";

export const API = "*/api";
export const RUB = "RUB";
export const money = (amount: number) => ({ amount, currency: RUB });

export function problem(status: number, code: string, title: string, extra: Record<string, unknown> = {}, headers: Record<string, string> = {}) {
  return HttpResponse.json({ type: "about:blank", title, status, code, ...extra }, { status, headers: { "Content-Type": "application/problem+json", ...headers } });
}

export const STAFF_ROLES = ["STAFF", "FRANCHISE_OWNER", "HQ_STAFF"] as const;
export const isStaff = (u: MockUser | null): u is MockUser => !!u && u.roles.some((r) => (STAFF_ROLES as readonly string[]).includes(r));
/** HQ видит все рестораны; остальные — только привязанные (`restaurants[]` в токене). */
export const canAccessRestaurant = (u: MockUser, restaurantId: string) => u.roles.includes("HQ_STAFF") || (u.restaurants ?? []).includes(restaurantId);

/** Демо-продвижение заказа по опросам (имитация производства). advanceEvery ≤ 0 — не двигать (тесты). */
const PICKUP: OrderStatus[] = ["accepted", "cooking", "ready"];
const DELIVERY: OrderStatus[] = ["accepted", "cooking", "ready", "on_delivery", "delivered"];
export function advanceOrder(db: MockDb, m: MockOrder) {
  settle(m);
  const o = m.order;
  if (db.advanceEvery <= 0 || o.status === "pending_payment" || o.status === "cancelled") return;
  m.polls++;
  if (m.polls % db.advanceEvery !== 0) return;
  const chain = o.fulfillment.type === "delivery" ? DELIVERY : PICKUP;
  const i = chain.indexOf(o.status);
  if (i >= 0 && i < chain.length - 1) o.status = chain[i + 1]!;
}

/** Ленивое «подтверждение терминала» и возврата: в реальной системе это вебхук кассы / событие payment. */
export function settle(m: MockOrder, now = Date.now()) {
  const o = m.order;
  if (m.pos && now >= m.pos.settleAt && (o.paymentStatus === "pending" || o.paymentStatus === "unknown")) {
    o.paymentStatus = "paid";
    m.pos.paidAt = new Date(m.pos.settleAt).toISOString();
  }
  if (m.refund && !m.refund.refundedAt && now - Date.parse(m.refund.requestedAt) > 2000) {
    m.refund.refundedAt = new Date().toISOString();
    o.refundStatus = "refunded";
  }
}

/** Payment по схеме payment.yaml; для кассы — tender и fiscalData, при возврате — refund. */
export function paymentView(m: MockOrder) {
  const o = m.order;
  const paid = o.paymentStatus === "paid";
  return {
    id: m.paymentId, orderId: o.id, method: o.paymentMethod, status: o.paymentStatus, attempt: m.attempt, amount: o.total,
    ...(o.paymentStatus === "declined" ? { declineReason: "Недостаточно средств" } : {}),
    ...(m.pos ? { tender: m.pos.tender } : {}),
    ...(m.pos && paid ? { paidAt: m.pos.paidAt, fiscalData: { receiptNumber: `ЧЕК-${o.number}`, fiscalDrive: "9960440300000001", fiscalDocument: 1000 + m.attempt } } : {}),
    ...(m.refund ? { refund: { status: m.refund.refundedAt ? "refunded" : "pending", requestedAt: m.refund.requestedAt, ...(m.refund.refundedAt ? { refundedAt: m.refund.refundedAt } : {}) } } : {}),
    createdAt: o.createdAt, updatedAt: new Date().toISOString(),
  };
}
