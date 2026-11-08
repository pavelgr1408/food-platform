import { render } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router";
import { setupServer } from "msw/node";
import { createApi, type Api } from "@fp/api";
import { ApiProvider } from "@fp/ui";
import { RESTAURANTS, createDb, createHandlers, setActiveDb } from "@fp/mocks";
import { makeQueryClient, routes } from "../app/App";

export const R1 = RESTAURANTS[0]!.id;
export const R2 = RESTAURANTS[1]!.id;
export const PHONES = { customer: "+79990000001", staff: "+79990000010", owner: "+79990000011", hq: "+79990000012" } as const;

export const db = createDb();
export const server = setupServer(...createHandlers(db));

/** Каждый тест — чистый мир; ожидания в UI не зависят от таймеров мок-бэкенда (advanceEvery=0, касса отвечает сразу). */
export function resetWorld() {
  window.localStorage.clear();
  Object.assign(db, createDb(), { advanceEvery: 0, posDelayMs: 0 });
  setActiveDb(db);
}

export function newApi(): Api {
  const mem = new Map<string, string>();
  return createApi({ baseUrl: "http://test.local/api", storage: { getItem: (k) => mem.get(k) ?? null, setItem: (k, v) => void mem.set(k, v), removeItem: (k) => void mem.delete(k) } });
}

/** as — телефон для предварительного входа; без него открывается как гость. */
export async function renderApp(path: string, as?: string) {
  const api = newApi();
  if (as) await api.auth.login(as, "secret1");
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  const qc = makeQueryClient();
  qc.setDefaultOptions({ queries: { retry: false, staleTime: 0, refetchOnWindowFocus: false } });
  const utils = render(<ApiProvider api={api}><QueryClientProvider client={qc}><RouterProvider router={router} /></QueryClientProvider></ApiProvider>);
  return { ...utils, api, router };
}

/** Заказ по факсу через API (как это сделал бы сотрудник на экране). */
export async function faxOrder(o: { item?: string; qty?: number; method?: "pos" | "courier_card"; total?: number } = {}) {
  const api = newApi();
  await api.auth.login(PHONES.staff, "secret1");
  const cart = await api.carts.create(R1, { guest: true });
  const c = await api.carts.putItems(cart.id, [{ menuItemId: o.item ?? "i-chicken", quantity: o.qty ?? 1, modifierIds: [] }]);
  await api.carts.putFulfillment(cart.id, { type: "pickup" });
  const { order } = await api.orders.create({ cartId: cart.id, channel: "staff_fax", paymentMethod: o.method ?? "pos", contact: { phone: "+79995550101", name: "Анна" }, expectedTotal: c.total }, api.newIdempotencyKey());
  if (o.total) db.orders.get(order.id)!.order.total = { amount: o.total, currency: "RUB" };
  return order;
}
