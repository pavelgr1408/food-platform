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
        { id: "i-wings", name: "Крылышки BBQ", description: "6 штук, соус барбекю", price: 27900, available: true, modifiers: [] },
      ] },
      { id: "c3", name: "Десерты и кофе", items: [
        { id: "i-cheese", name: "Чизкейк «Нью-Йорк»", description: "Классический, с ягодным соусом", price: 17900, available: true, modifiers: [] },
        { id: "i-cappu", name: "Капучино", description: "250 мл", price: 15900, available: true, modifiers: [{ id: "m-oat", name: "Овсяное молоко", price: 5000, available: true }, { id: "m-syrup", name: "Ванильный сироп", price: 3000, available: true }] },
        { id: "i-brownie", name: "Брауни", description: "Шоколадный, с орехами", price: 12900, available: false, modifiers: [] },
      ] },
    ],
  },
  { id: "22222222-2222-4222-8222-222222222222", name: "Лоток у вокзала", address: "Комсомольская пл., 3", city: "Москва", lat: 55.7763, lon: 37.6554, open: false, timezone: "Europe/Moscow", deliveryEnabled: false, deliveryRadius: 0,
    menu: [{ id: "c1", name: "Основное", items: [
      { id: "i-s", name: "Сэндвич", price: 15000, available: true, modifiers: [] },
      { id: "i-salad", name: "Салат «Цезарь»", description: "С курицей и пармезаном", price: 21900, available: true, modifiers: [] },
      { id: "i-soup", name: "Суп-лапша", description: "Куриный бульон, домашняя лапша", price: 16900, available: true, modifiers: [] },
    ] }] },
  { id: "33333333-3333-4333-8333-333333333333", name: "Лоток на Невском", address: "Невский пр., 28", city: "Санкт-Петербург", lat: 59.9343, lon: 30.3351, open: true, timezone: "Europe/Moscow", deliveryEnabled: true, deliveryRadius: 3000 },
];

export const CITY: Record<string, [number, number]> = { "москва": [55.7558, 37.6173], "санкт-петербург": [59.9343, 30.3351], "казань": [55.7887, 49.1221], "екатеринбург": [56.8389, 60.6057] };

export function haversine(a: [number, number], b: [number, number]): number {
  const R = 6371000, rad = (d: number) => (d * Math.PI) / 180;
  const dLat = rad(b[0] - a[0]), dLon = rad(b[1] - a[1]);
  const h = Math.sin(dLat / 2) ** 2 + Math.cos(rad(a[0])) * Math.cos(rad(b[0])) * Math.sin(dLon / 2) ** 2;
  return Math.round(2 * R * Math.asin(Math.sqrt(h)));
}

export interface MockUser { id: string; phone: string; password: string; roles: Principal["roles"]; restaurants?: string[] }
export interface MockCart { cart: Cart; restaurantId: string; owner: string | null; contact?: { phone?: string } }
export interface MockOrder {
  order: Order; polls: number; owner: string | null; paymentId?: string; attempt: number; authorizedAt?: number;
  /** Контакт клиента и сотрудник, оформивший заказ по факсу (StaffOrder). */
  contact?: { name?: string; phone?: string; email?: string }; createdBy?: string;
  /** Касса: когда «терминал подтвердит» оплату и каким способом; итог читается лениво (вебхук кассы в реальной системе). */
  pos?: { tender: "card" | "cash"; settleAt: number; outcome: "paid" | "unknown"; paidAt?: string };
  refund?: { requestedAt: string; refundedAt?: string };
}

/** Акция в мок-БД: PromotionPublic + служебные поля Promotion (loyalty.yaml). */
export interface MockPromotion {
  id: string; scope: "local" | "national"; restaurantId?: string; title: string; description?: string;
  discount: { type: "percent" | "fixed"; value: number }; currency: string;
  conditions: { fulfillment?: "pickup" | "delivery"; menuItemIds?: string[]; minSubtotal?: number; stackable: boolean };
  priority: number; startsAt: string; endsAt: string;
  status: "draft" | "active" | "completed"; version: number; createdAt: string; updatedAt: string; createdBy?: string;
}
export interface MockRevision { version: number; action: "created" | "updated" | "activated" | "completed"; actor?: string; at: string; snapshot: MockPromotion }

const R1 = RESTAURANTS[0]!.id;
const SEED_AT = "2026-10-01T00:00:00Z";
export const seedPromotions = (): [string, MockPromotion][] => [
  { id: "p-10", scope: "local" as const, restaurantId: R1, title: "−10% от 500 ₽", description: "На весь заказ при сумме от 500 ₽", discount: { type: "percent" as const, value: 1000 }, currency: "RUB", conditions: { minSubtotal: 50000, stackable: false }, priority: 10, startsAt: "2026-01-01T00:00:00Z", endsAt: "2027-12-31T00:00:00Z", status: "active" as const, version: 1, createdAt: SEED_AT, updatedAt: SEED_AT },
  { id: "p-fx", scope: "national" as const, title: "−100 ₽ на самовывоз", description: "Заберите заказ сами", discount: { type: "fixed" as const, value: 10000 }, currency: "RUB", conditions: { fulfillment: "pickup" as const, minSubtotal: 80000, stackable: false }, priority: 5, startsAt: "2026-01-01T00:00:00Z", endsAt: "2027-12-31T00:00:00Z", status: "active" as const, version: 1, createdAt: SEED_AT, updatedAt: SEED_AT },
].map((p) => [p.id, p]);

export function createDb() {
  const db = {
    users: new Map<string, MockUser>([
      ["+79990000001", { id: "u-1", phone: "+79990000001", password: "secret1", roles: ["CUSTOMER"] }],
      /* персонал: учётные записи в реальной системе заводятся только в БД (API администрирования нет) */
      ["+79990000010", { id: "u-staff", phone: "+79990000010", password: "secret1", roles: ["STAFF"], restaurants: [R1] }],
      ["+79990000011", { id: "u-owner", phone: "+79990000011", password: "secret1", roles: ["FRANCHISE_OWNER"], restaurants: [R1] }],
      ["+79990000012", { id: "u-hq", phone: "+79990000012", password: "secret1", roles: ["HQ_STAFF"] }],
    ]),
    access: new Map<string, { userId: string; exp: number }>(),
    refresh: new Map<string, string>(),
    carts: new Map<string, MockCart>(),
    orders: new Map<string, MockOrder>(),
    idem: new Map<string, { sig: string; status: number; body: unknown }>(),
    promotions: new Map<string, MockPromotion>(seedPromotions()),
    promotionHistory: new Map<string, MockRevision[]>(),
    devices: new Map<string, { id: string; owner: string; platform: string; permissionsGranted: boolean; registeredAt: string }>(),
    /** Каждые N опросов заказа статус продвигается (демо). 0 — не двигать (тесты). */
    advanceEvery: 2,
    /** Через сколько мс «терминал кассы» подтверждает оплату (вебхук кассы). 0 — сразу (тесты). */
    posDelayMs: 1200,
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
const MAPS = ["users", "access", "refresh", "carts", "orders", "idem", "devices", "promotions", "promotionHistory"] as const;

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
