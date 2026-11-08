import type { OrderStatus } from "@fp/api";

export const STATUS_LABEL: Record<OrderStatus, string> = {
  pending_payment: "Ждёт оплаты",
  accepted: "Принят",
  cooking: "Готовится",
  ready: "Готов",
  handed_over: "Выдан",
  on_delivery: "Курьер в пути",
  delivered: "Доставлен",
  closed: "Завершён",
  not_received: "Не получен",
  cancelled: "Отменён",
};

export const REASON_LABEL: Record<string, string> = {
  payment_timeout: "Не оплачен вовремя",
  customer_request: "По просьбе клиента",
  out_of_stock: "Блюда закончились",
  restaurant_closed: "Ресторан закрыт",
  other: "Другая причина",
  pickup_timeout: "Заказ не забрали за 2 часа",
  undelivered: "Не удалось доставить",
};

/** Словарь причин; неизвестный (внутренний) код не показываем — например «production», «courier_picked_up». */
export const reasonText = (code?: string | null): string | null => (code ? REASON_LABEL[code] ?? null : null);

export const TERMINAL: OrderStatus[] = ["closed", "cancelled", "not_received", "delivered", "handed_over"];
export const isTerminal = (s: OrderStatus) => TERMINAL.includes(s);

/** Степпер: последовательность настоящая (DESIGN-SYSTEM §4). */
export function stepsFor(fulfillment: "pickup" | "delivery", online: boolean): OrderStatus[] {
  const head: OrderStatus[] = online ? ["pending_payment", "accepted"] : ["accepted"];
  const mid: OrderStatus[] = ["cooking", "ready"];
  return fulfillment === "delivery" ? [...head, ...mid, "on_delivery", "delivered"] : [...head, ...mid, "handed_over"];
}
