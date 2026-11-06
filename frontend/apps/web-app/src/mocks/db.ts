import type { Cart, Order, Principal } from "@fp/api";

/** Состояние мок-бэкенда. Живёт в памяти страницы (MSW-обработчики исполняются в основном потоке). */
export interface MockItem { id: string; name: string; description?: string; price: number; available: boolean; modifiers: { id: string; name: string; price: number; available: boolean }[] }
export interface MockRestaurant {
  id: string; name: string; address: string; city: string; lat: number; lon: number; open: boolean; timezone: string;
  deliveryEnabled: boolean; deliveryRadius: number; menu?: { id: string; name: string; items: MockItem[] }[];
}

export const RESTAURANTS: MockRestaurant[] = [
  {
    id: "11111111-1111-4111-8111-111111111111", name: "Лоток на Тверской", address: "ул. Тверская, 7", city: "Москва", lat: 55.7577, lon: 37.6137, open: true, timezone: "Europe/Moscow", deliveryEnabled: true, deliveryRadius: 5000,
    menu: [
      { id: "c1", name: "Бургеры", items: [
        { id: "i-burger", name: "Бургер «Классика»", description: "Говяжья котлета, сыр, соус", price: 24900, available: true, modifiers: [{ id: "m-cheese", name: "Двойной сыр", price: 5000, available: true }, { id: "m-bacon", name: "Бекон", price: 7000, available: true }, { id: "m-jal", name: "Халапеньо", price: 3000, available: false }] },
        { id: "i-chicken", name: "Чикен-ролл", description: "Курица, салат, соус", price: 19990, available: true, modifiers: [] },
        { id: "i-vegan", name: "Веган-бургер", price: 22900, available: false, modifiers: [] },
      ] },
      { id: "c2", name: "Закуски и напитки", items: [
        { id: "i-fries", name: "Картофель фри", price: 9900, available: true, modifiers: [{ id: "m-sauce", name: "Сырный соус", price: 2500, available: true }] },
        { id: "i-cola", name: "Лимонад", description: "0,5 л", price: 12990, available: true, modifiers: [] },
      ] },
    ],
  },
  { id: "22222222-2222-4222-8222-222222222222", name: "Лоток у вокзала", address: "Комсомольская пл., 3", city: "Москва", lat: 55.7763, lon: 37.6554, open: false, timezone: "Europe/Moscow", deliveryEnabled: false, deliveryRadius: 0,
    menu: [{ id: "c1", name: "Основное", items: [{ id: "i-s", name: "Сэндвич", price: 15000, available: true, modifiers: [] }] }] },
  { id: "33333333-3333-4333-8333-333333333333", name: "Лоток на Невском", address: "Невский пр., 28", city: "Санкт-Петербург", lat: 59.9343, lon: 30.3351, open: true, timezone: "Europe/Moscow", deliveryEnabled: true, deliveryRadius: 3000 },
];

export const CITY: Record<string, [number, number]> = { "москва": [55.7558, 37.6173], "санкт-петербург": [59.9343, 30.3351], "казань": [55.7887, 49.1221], "екатеринбург": [56.8389, 60.6057] };

export function haversine(a: [number, number], b: [number, number]): number {
  const R = 6371000, rad = (d: number) => (d * Math.PI) / 180;
  const dLat = rad(b[0] - a[0]), dLon = rad(b[1] - a[1]);
  const h = Math.sin(dLat / 2) ** 2 + Math.cos(rad(a[0])) * Math.cos(rad(b[0])) * Math.sin(dLon / 2) ** 2;
  return Math.round(2 * R * Math.asin(Math.sqrt(h)));
}

export interface MockUser { id: string; phone: string; password: string; roles: Principal["roles"] }
export interface MockCart { cart: Cart; restaurantId: string; owner: string | null; contact?: { phone?: string } }
export interface MockOrder { order: Order; polls: number; owner: string | null; paymentId?: string; attempt: number; authorizedAt?: number }

export function createDb() {
  const db = {
    users: new Map<string, MockUser>([["+79990000001", { id: "u-1", phone: "+79990000001", password: "secret1", roles: ["CUSTOMER"] }]]),
    access: new Map<string, { userId: string; exp: number }>(),
    refresh: new Map<string, string>(),
    carts: new Map<string, MockCart>(),
    orders: new Map<string, MockOrder>(),
    idem: new Map<string, { sig: string; status: number; body: unknown }>(),
    devices: new Map<string, { id: string; owner: string; platform: string; permissionsGranted: boolean; registeredAt: string }>(),
    /** Каждые N опросов заказа статус продвигается (демо). 0 — не двигать (тесты). */
    advanceEvery: 2,
    /** Сценарии для демо/тестов: следующая оплата будет отклонена; цены изменятся на заказе. */
    declineNextPayment: false,
    priceChangeOnNextOrder: false,
    seq: 0,
    id() { return crypto.randomUUID(); },
  };
  return db;
}
export type MockDb = ReturnType<typeof createDb>;
export let activeDb: MockDb = createDb();
export const setActiveDb = (d: MockDb) => { activeDb = d; };

/* ---- Персистентность мок-БД между перезагрузками (редирект на мок-шлюз — полная перезагрузка страницы) ---- */
const KEY = "fp.mockdb";
const MAPS = ["users", "access", "refresh", "carts", "orders", "idem", "devices"] as const;

export function saveDb(db: MockDb, storage: Pick<Storage, "setItem"> = window.localStorage) {
  try {
    const dump: Record<string, unknown> = { seq: db.seq };
    for (const k of MAPS) dump[k] = [...db[k].entries()];
    storage.setItem(KEY, JSON.stringify(dump));
  } catch { /* переполнение/приватный режим — моки живут до перезагрузки */ }
}

export function loadDb(db: MockDb, storage: Pick<Storage, "getItem"> = window.localStorage) {
  try {
    const raw = storage.getItem(KEY);
    if (!raw) return;
    const dump = JSON.parse(raw) as Record<string, [string, never][]> & { seq?: number };
    for (const k of MAPS) if (Array.isArray(dump[k])) (db[k] as Map<string, unknown>) = new Map(dump[k]) as never;
    db.seq = dump.seq ?? db.seq;
  } catch { /* битый дамп игнорируем */ }
}

export function resetPersistedDb() {
  try { window.localStorage.removeItem(KEY); } catch { /* noop */ }
}
