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
  get: () => read<LocalCart | null>("fp.cart", null),
  set: (c: LocalCart) => write("fp.cart", c),
  clear: () => { try { window.localStorage.removeItem("fp.cart"); } catch { /* noop */ } },
};

export interface LocalOrder { id: string; number: string; restaurantId: string; createdAt: string; totalAmount: number; currency: string }
export const localOrders = {
  list: () => read<LocalOrder[]>("fp.orders", []),
  add(o: LocalOrder) {
    const rest = this.list().filter((x) => x.id !== o.id);
    write("fp.orders", [o, ...rest].slice(0, 30));
  },
};

/** UC-PAY-08: paymentId есть только в ответе initiatePayment — запоминаем. */
export const localPayments = {
  get: (orderId: string) => read<Record<string, string>>("fp.payments", {})[orderId] ?? null,
  set(orderId: string, paymentId: string) {
    write("fp.payments", { ...read<Record<string, string>>("fp.payments", {}), [orderId]: paymentId });
  },
};

/** Последний контакт — подставляем в форму оформления. */
export const localContact = {
  get: () => read<{ name?: string; phone?: string; email?: string }>("fp.contact", {}),
  set: (c: { name?: string; phone?: string; email?: string }) => write("fp.contact", c),
};
