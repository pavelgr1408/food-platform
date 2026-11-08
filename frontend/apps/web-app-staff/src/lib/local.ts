import { API_MODE } from "./env";

/** Ключи разделены по режиму API: данные MSW-моков не должны попадать на реальный стенд. */
const K = (name: string) => `fp.${API_MODE}.staff.${name}`;

function read<T>(key: string, fallback: T): T {
  try {
    const raw = window.localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : fallback;
  } catch {
    return fallback;
  }
}
function write(key: string, v: unknown) {
  try { window.localStorage.setItem(key, JSON.stringify(v)); } catch { /* приватный режим — живём без сохранения */ }
}
function drop(key: string) {
  try { window.localStorage.removeItem(key); } catch { /* noop */ }
}

/** Рабочий ресторан смены (UC-ACC-08): локальный фильтр интерфейса, права проверяет сервер. */
export const localWorkplace = {
  get: () => read<string | null>(K("restaurant"), null),
  set: (id: string) => write(K("restaurant"), id),
  clear: () => drop(K("restaurant")),
};

/** UC-STF-09: paymentId есть только в ответе initiatePosPayment (и в 409 PAYMENT_IN_PROGRESS) — в Order его нет, запоминаем на устройстве. */
export const localPayments = {
  get: (orderId: string) => read<Record<string, string>>(K("payments"), {})[orderId] ?? null,
  set(orderId: string, paymentId: string) {
    const all = read<Record<string, string>>(K("payments"), {});
    const keys = Object.keys(all);
    // не растём бесконечно: помним последние 200 платежей смены
    if (keys.length >= 200) for (const k of keys.slice(0, keys.length - 199)) delete all[k];
    write(K("payments"), { ...all, [orderId]: paymentId });
  },
};

/** Корзина заказа по факсу: гостевая корзина владеется знанием cartId (UC-STF-02). */
export interface FaxDraft { cartId: string; restaurantId: string }
export const localFax = {
  get: () => read<FaxDraft | null>(K("fax"), null),
  set: (d: FaxDraft) => write(K("fax"), d),
  clear: () => drop(K("fax")),
};

/** Выход: общий терминал кассы не хранит ни корзину, ни платежи, ни выбранный ресторан. */
export function clearStaffDeviceData() {
  for (const k of ["restaurant", "payments", "fax"]) drop(K(k));
}
