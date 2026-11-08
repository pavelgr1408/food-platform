import type { Order, PaymentMethodType, StaffOrder } from "@fp/api";

export const PAY_LABEL: Record<PaymentMethodType, string> = { online_card: "Онлайн-оплата картой", pos: "На кассе", courier_card: "Картой курьеру" };
export const TENDER_LABEL = { card: "Картой", cash: "Наличными" } as const;
export const CHANNEL_LABEL = { online: "Онлайн", staff_fax: "По факсу" } as const;

/** Оплата на кассе возможна только для заказа с оплатой `pos`, пока он не оплачен и не завершён (UC-STF-06). */
export const POS_STATUSES: Order["status"][] = ["accepted", "cooking", "ready"];
export function canTakePosPayment(o: Pick<Order, "paymentMethod" | "paymentStatus" | "status">): boolean {
  return o.paymentMethod === "pos" && o.paymentStatus !== "paid" && POS_STATUSES.includes(o.status);
}

export type Verdict = { ok: true } | { ok: false; reason: string };

/** UC-STF-07: выдать можно готовый оплаченный заказ самовывоза (или доставки без курьера, E2). Причина отказа — человеку, а не код. */
export function issueVerdict(o: Pick<Order, "status" | "paymentStatus" | "paymentMethod" | "fulfillment" | "delivery">): Verdict {
  if (o.status === "closed" || o.status === "handed_over") return { ok: false, reason: "Заказ уже выдан" };
  if (o.status === "cancelled") return { ok: false, reason: "Заказ отменён" };
  const noCourier = o.delivery?.status === "failed" && o.delivery.failReason === "no_courier";
  if (o.fulfillment.type === "delivery" && !noCourier) return { ok: false, reason: "Заказ с доставкой выдаёт курьер" };
  if (o.status !== "ready") return { ok: false, reason: "Заказ ещё не готов" };
  if (o.paymentStatus !== "paid") return { ok: false, reason: o.paymentMethod === "pos" ? "Сначала примите оплату" : "Заказ не оплачен" };
  return { ok: true };
}

/** UC-STF-08: отмена до начала приготовления. */
export function cancelVerdict(o: Pick<Order, "status">): Verdict {
  if (o.status === "cancelled") return { ok: false, reason: "Заказ уже отменён" };
  if (o.status === "pending_payment" || o.status === "accepted") return { ok: true };
  if (o.status === "cooking" || o.status === "ready") return { ok: false, reason: "Заказ уже готовится — отменить нельзя" };
  return { ok: false, reason: "Заказ завершён — отменить нельзя" };
}

export type Tone = "neutral" | "ok" | "warn" | "hot";

/** Состояние оплаты одной фразой для кассира. */
export function payBadge(o: Pick<Order, "paymentMethod" | "paymentStatus" | "status">): { text: string; tone: Tone } {
  if (o.status === "cancelled" && o.paymentStatus !== "paid") return { text: "Не оплачен", tone: "neutral" };
  switch (o.paymentStatus) {
    case "paid": return { text: "Оплачен", tone: "ok" };
    case "unknown": return { text: "Проверяем платёж", tone: "warn" };
    case "declined": return { text: "Оплата отклонена", tone: "hot" };
    case "authorized": return { text: "Оплата в обработке", tone: "warn" };
    default:
      if (o.paymentMethod === "pos") return { text: "К оплате на кассе", tone: "warn" };
      if (o.paymentMethod === "courier_card") return { text: "Оплата курьеру", tone: "neutral" };
      return { text: "Ждёт онлайн-оплаты", tone: "neutral" };
  }
}

/** «2× Бургер, Картофель фри, ещё 3» — для строки очереди. */
export function itemsSummary(o: Pick<Order, "items">, max = 2): string {
  const parts = o.items.map((i) => (i.quantity > 1 ? `${i.quantity}× ${i.name}` : i.name));
  return parts.length > max ? `${parts.slice(0, max).join(", ")}, ещё ${parts.length - max}` : parts.join(", ");
}

/** Статус, после которого заказ уже не меняется сам, и деньги не движутся — опрос можно остановить. */
export function isSettled(o: Pick<StaffOrder, "status" | "paymentStatus" | "refundStatus">): boolean {
  const final = ["closed", "cancelled", "not_received", "delivered", "handed_over"].includes(o.status);
  // у отменённого неоплаченного заказа paymentStatus=pending уже ничего не значит; ждём только возврат и неизвестный исход
  return final && o.refundStatus !== "pending" && o.paymentStatus !== "unknown";
}

/** Валидный формат номера заказа (UC-STF-01), регистр не важен. */
export const ORDER_NUMBER = /^R\d{1,9}-\d{6}-\d{4,9}$/i;
