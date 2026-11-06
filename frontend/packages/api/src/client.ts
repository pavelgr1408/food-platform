import { ApiError, problemFromResponse } from "./problem";
import { createHttp, newIdempotencyKey, type Http } from "./http";
import { Session, type KeyValueStorage } from "./session";
import type * as T from "./types";

export interface ApiConfig {
  /** '' — относительные пути (dev-proxy / тот же origin); иначе URL gateway, напр. http://localhost:8080 */
  baseUrl: string;
  storage?: KeyValueStorage;
  fetchImpl?: typeof fetch;
}

export function createApi(cfg: ApiConfig) {
  const f: typeof fetch = (...a) => (cfg.fetchImpl ?? fetch)(...a);

  // refresh идёт мимо обычного http: без токена и без рекурсивного refresh на 401
  const session = new Session(async (refreshToken) => {
    let res: Response;
    try {
      res = await f(`${cfg.baseUrl}/auth/refresh`, {
        method: "POST",
        headers: { "Content-Type": "application/json", Accept: "application/json" },
        body: JSON.stringify({ refreshToken }),
      });
    } catch {
      throw new ApiError({ status: 0, network: true });
    }
    if (!res.ok) throw await problemFromResponse(res);
    return (await res.json()) as T.TokenPair;
  }, cfg.storage);

  const http: Http = createHttp({ baseUrl: cfg.baseUrl, session, fetchImpl: cfg.fetchImpl });
  const get = <R>(path: string, query?: Record<string, never> | object, auth: "none" | "optional" | "required" = "optional", signal?: AbortSignal) =>
    http.request<R>("GET", path, { query: query as Record<string, string>, auth, signal }).then((r) => r.data);

  const auth = {
    /** UC-ACC-03 */
    register: (phone: string, password: string) =>
      http.request<T.RegisteredUser>("POST", "/auth/register", { body: { phone, password }, auth: "none" }).then((r) => r.data),
    /** UC-ACC-04: сохраняет пару в сессию. */
    async login(phone: string, password: string) {
      const { data } = await http.request<T.TokenPair>("POST", "/auth/login", { body: { phone, password }, auth: "none" });
      session.setTokens(data);
      return data;
    },
    /** UC-ACC-06: повторный logout (401) игнорируем, сессию чистим всегда. */
    async logout() {
      const rt = session.refreshToken;
      try {
        if (rt) await http.request("POST", "/auth/logout", { body: { refreshToken: rt }, auth: "none" });
      } catch (e) {
        if (!(e instanceof ApiError) || (e.status !== 401 && !e.network)) throw e;
      } finally {
        session.clear();
      }
    },
    /** UC-ACC-07 */
    me: () => get<T.Principal>("/auth/me", undefined, "required"),
  };

  const restaurants = {
    search: (p: T.RestaurantSearchParams, signal?: AbortSignal) => get<T.RestaurantPage>("/restaurants", p, "optional", signal),
    get: (id: string) => get<T.RestaurantDetails>(`/restaurants/${id}`),
    menu: (id: string) => get<T.MenuData>(`/restaurants/${id}/menu`),
    validateMenu: (id: string, items: T.CartItemInput[]) =>
      http.request<T.MenuValidationResult>("POST", `/restaurants/${id}/menu/validation`, { body: { items }, auth: "optional" }).then((r) => r.data),
  };

  const promotions = {
    /** UC-MNU-04: без restaurantId — только национальные. */
    list: (restaurantId?: string) => get<T.PromotionPublic[]>("/promotions", { restaurantId } as object),
  };

  const carts = {
    create: (restaurantId: string, opts: { guest: boolean; contact?: T.ContactInfo }) =>
      http.request<T.Cart>("POST", "/carts", { body: { restaurantId, guest: opts.guest || undefined, contact: opts.contact } }).then((r) => r.data),
    get: (id: string) => get<T.Cart>(`/carts/${id}`),
    /** Полный состав (замена, не дельта); [] очищает. */
    putItems: (id: string, items: T.CartItemInput[]) =>
      http.request<T.Cart>("PUT", `/carts/${id}/items`, { body: { items } }).then((r) => r.data),
    putFulfillment: (id: string, body: T.FulfillmentInput) =>
      http.request<T.Cart>("PUT", `/carts/${id}/fulfillment`, { body }).then((r) => r.data),
  };

  const orders = {
    /** Ключ генерирует вызывающий при открытии экрана подтверждения (UC-ORD-08). */
    create: (body: T.OrderCreateRequest, idempotencyKey: string) =>
      http.request<T.Order>("POST", "/orders", { body, idempotencyKey }).then((r) => ({ order: r.data, replay: r.headers.get("Idempotent-Replay") === "true" })),
    get: (id: string, signal?: AbortSignal) => get<T.Order>(`/orders/${id}`, undefined, "optional", signal),
    eta: (id: string) => get<T.Eta>(`/orders/${id}/eta`),
    setPickupSlot: (id: string, slot: { from: string; to: string }) =>
      http.request<T.Eta>("PUT", `/orders/${id}/pickup-slot`, { body: slot }).then((r) => r.data),
  };

  const payments = {
    methods: (orderId: string) => get<T.PaymentMethod[]>(`/orders/${orderId}/payment-methods`),
    initiate: (orderId: string, returnUrl: string | undefined, idempotencyKey: string) =>
      http.request<T.Payment_>("POST", `/orders/${orderId}/payments`, { body: { method: "online_card", returnUrl }, idempotencyKey }).then((r) => ({ payment: r.data, status: r.status })),
    get: (orderId: string, paymentId: string) => get<T.Payment_>(`/orders/${orderId}/payments/${paymentId}`),
  };

  const delivery = {
    get: (orderId: string) => get<T.DeliveryInfo>(`/orders/${orderId}/delivery`),
    courierLocation: (orderId: string) => get<T.CourierLocation>(`/orders/${orderId}/delivery/courier-location`),
  };

  const notifications = {
    register: (b: T.DeviceTokenRegistration, locale: "ru" | "en") =>
      http.request<T.DeviceToken>("POST", "/notifications/device-tokens", { body: b, auth: "required", headers: { "Accept-Language": locale } }).then((r) => r.data),
    list: () => get<T.DeviceToken[]>("/notifications/device-tokens", undefined, "required"),
    remove: (id: string) => http.request("DELETE", `/notifications/device-tokens/${id}`, { auth: "required" }).then(() => undefined),
  };

  return { session, http, auth, restaurants, promotions, carts, orders, payments, delivery, notifications, newIdempotencyKey };
}

export type Api = ReturnType<typeof createApi>;
