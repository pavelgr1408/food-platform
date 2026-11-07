import { API_MODE } from "./env";

/** Ключи разделены по режиму API: заказы из MSW-моков не должны попадать в историю реального стенда. */
const K = (name: string) => `fp.${API_MODE}.${name}`;

/** Локальное состояние устройства: cartId гостя, история заказов (API списка нет — пробел №2), paymentId. Всё в try/catch. */
function read<T>(key: string, fallback: T): T {
  try {
    const raw = window.localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : fallback;
  } catch {
    return fallback;
  }
}
function write(key: string, v: unknown) {
  try {
    window.localStorage.setItem(key, JSON.stringify(v));
  } catch {
    /* приватный режим — живём без сохранения */
  }
}

export interface LocalCart { cartId: string; restaurantId: string }
export const localCart = {
  get: () => read<LocalCart | null>(K("cart"), null),
  set: (c: LocalCart) => write(K("cart"), c),
  clear: () => { try { window.localStorage.removeItem(K("cart")); } catch { /* noop */ } },
};

export interface LocalOrder { id: string; number: string; restaurantId: string; createdAt: string; totalAmount: number; currency: string }
export const localOrders = {
  list: () => read<LocalOrder[]>(K("orders"), []),
  add(o: LocalOrder) {
    const rest = this.list().filter((x) => x.id !== o.id);
    write(K("orders"), [o, ...rest].slice(0, 30));
  },
  remove(ids: string[]) {
    if (ids.length) write(K("orders"), this.list().filter((x) => !ids.includes(x.id)));
  },
};

/** Выход: на общем устройстве не оставляем чужие заказы, платежи, контакты и корзину. */
export function clearDeviceData() {
  for (const k of ["orders", "payments", "contact", "cart"]) {
    try { window.localStorage.removeItem(K(k)); } catch { /* noop */ }
  }
}

/** UC-PAY-08: paymentId есть только в ответе initiatePayment — запоминаем. */
export const localPayments = {
  get: (orderId: string) => read<Record<string, string>>(K("payments"), {})[orderId] ?? null,
  set(orderId: string, paymentId: string) {
    write(K("payments"), { ...read<Record<string, string>>(K("payments"), {}), [orderId]: paymentId });
  },
};

/** Последний контакт — подставляем в форму оформления. */
export const localContact = {
  get: () => read<{ name?: string; phone?: string; email?: string }>(K("contact"), {}),
  set: (c: { name?: string; phone?: string; email?: string }) => write(K("contact"), c),
};
