import { render } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router";
import { setupServer } from "msw/node";
import { createApi } from "@fp/api";
import { ApiProvider } from "../app/api-context";
import { makeQueryClient, routes } from "../app/App";
import { createDb, setActiveDb, RESTAURANTS } from "../mocks/db";
import { createHandlers } from "../mocks/handlers";
import { resetCartStore } from "../lib/cart";

export const R1 = RESTAURANTS[0]!.id;
export const R2 = RESTAURANTS[1]!.id;
export const R3 = RESTAURANTS[2]!.id;

export const db = createDb();
export const server = setupServer(...createHandlers(db));

export function resetWorld() {
  window.localStorage.clear();
  Object.assign(db, createDb(), { advanceEvery: 0 });
  setActiveDb(db);
  resetCartStore();
}

export function renderApp(path: string) {
  const mem = new Map<string, string>();
  const api = createApi({ baseUrl: "http://test.local/api", storage: { getItem: (k) => mem.get(k) ?? null, setItem: (k, v) => void mem.set(k, v), removeItem: (k) => void mem.delete(k) } });
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  const qc = makeQueryClient();
  qc.setDefaultOptions({ queries: { retry: false, staleTime: 0, refetchOnWindowFocus: false } });
  const utils = render(
    <ApiProvider api={api}><QueryClientProvider client={qc}><RouterProvider router={router} /></QueryClientProvider></ApiProvider>,
  );
  return { ...utils, api, router };
}
