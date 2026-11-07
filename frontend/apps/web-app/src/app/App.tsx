import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider, type RouteObject } from "react-router";
import { isApiError, type Api } from "@fp/api";
import { ApiProvider } from "./api-context";
import { Layout } from "../components/Layout";
import { Home } from "../pages/Home";
import { Restaurant } from "../pages/Restaurant";
import { Cart } from "../pages/Cart";
import { Order } from "../pages/Order";
import { Orders } from "../pages/Orders";
import { Login, Register } from "../pages/AuthPages";
import { Profile } from "../pages/Profile";
import { NotFound } from "../pages/NotFound";
import { API_MODE } from "../lib/env";
import { lazy, Suspense, useState } from "react";

const MockGateway = lazy(() => import("../pages/MockGateway").then((m) => ({ default: m.MockGateway })));

export const routes: RouteObject[] = [
  {
    element: <Layout />,
    children: [
      { path: "/", element: <Home /> },
      { path: "/r/:restaurantId", element: <Restaurant /> },
      { path: "/cart", element: <Cart /> },
      { path: "/orders", element: <Orders /> },
      { path: "/orders/:orderId", element: <Order /> },
      { path: "/login", element: <Login /> },
      { path: "/register", element: <Register /> },
      { path: "/profile", element: <Profile /> },
      ...(API_MODE === "mock" ? [{ path: "/mock-gateway", element: <Suspense fallback={null}><MockGateway /></Suspense> }] : []),
      { path: "*", element: <NotFound /> },
    ],
  },
];

export function makeQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 15_000,
        refetchOnWindowFocus: false,
        // 4xx — не лечится повтором; сеть/5xx — пару раз
        retry: (n, e) => !(isApiError(e) && e.status >= 400 && e.status < 500) && n < 2,
      },
    },
  });
}

export function App({ api, queryClient }: { api: Api; queryClient?: QueryClient }) {
  const [qc] = useState(() => queryClient ?? makeQueryClient());
  const [router] = useState(() => createBrowserRouter(routes));
  return (
    <ApiProvider api={api}>
      <QueryClientProvider client={qc}><RouterProvider router={router} /></QueryClientProvider>
    </ApiProvider>
  );
}
