import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, Navigate, Outlet, RouterProvider, useLocation, type RouteObject } from "react-router";
import { isApiError, type Api } from "@fp/api";
import { ApiProvider, Button, Empty, Spinner } from "@fp/ui";
import { useStaffAuth } from "./auth";
import { WorkplaceProvider, useWorkplace } from "./workplace";
import { Shell } from "../components/Shell";
import { Login } from "../pages/Login";
import { Queue } from "../pages/Queue";
import { OrderPage } from "../pages/OrderPage";
import { Fax } from "../pages/Fax";
import { Promotions } from "../pages/Promotions";
import { PromotionEditor } from "../pages/PromotionEditor";
import { NotFound } from "../pages/NotFound";

/** Охранник маршрутов: гость → вход; не персонал → отказ; персонал без ресторанов → отказ (UC-ACC-08). */
function RequireStaff() {
  const auth = useStaffAuth();
  const loc = useLocation();
  if (auth.status === "loading") return <div className="mx-auto max-w-md p-6"><Spinner label="Проверяем доступ…" /></div>;
  if (auth.status === "guest") return <Navigate to={`/login?next=${encodeURIComponent(loc.pathname + loc.search)}`} replace />;
  if (!auth.isStaff) return <Denied title="Нет доступа" text="Эта часть сервиса — для сотрудников. Войдите под учётной записью персонала." />;
  return (
    <WorkplaceProvider>
      <HasRestaurants />
    </WorkplaceProvider>
  );
}

function HasRestaurants() {
  const wp = useWorkplace();
  if (wp.loading) return <div className="mx-auto max-w-md p-6"><Spinner label="Загружаем рестораны…" /></div>;
  if (!wp.current) return <Denied title="Нет доступа к ресторанам" text="Учётная запись не привязана ни к одному ресторану. Обратитесь к владельцу франшизы или в HQ." />;
  return <Outlet />;
}

function Denied({ title, text }: { title: string; text: string }) {
  const auth = useStaffAuth();
  return (
    <div className="mx-auto max-w-lg p-6">
      <Empty page title={title}>
        <p>{text}</p>
        <div className="mt-4"><Button variant="outline" onClick={() => void auth.logout()}>Выйти</Button></div>
      </Empty>
    </div>
  );
}

/** Маршрут акций доступен владельцу и HQ; кассиру показываем отказ, а не 404 (иначе непонятно, почему нет раздела). */
function RequirePromotions() {
  const auth = useStaffAuth();
  if (!auth.canPromotions) return <Empty page title="Раздел недоступен">Управлять акциями могут владелец франшизы и HQ.</Empty>;
  return <Outlet />;
}

export const routes: RouteObject[] = [
  { path: "/login", element: <Login /> },
  {
    element: <RequireStaff />,
    children: [{
      element: <Shell />,
      children: [
        { path: "/", element: <Queue /> },
        { path: "/o/:number", element: <OrderPage /> },
        { path: "/fax", element: <Fax /> },
        { element: <RequirePromotions />, children: [
          { path: "/promotions", element: <Promotions /> },
          { path: "/promotions/new", element: <PromotionEditor /> },
          { path: "/promotions/:promotionId", element: <PromotionEditor /> },
        ] },
        { path: "*", element: <NotFound /> },
      ],
    }],
  },
];

export function makeQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 5_000,
        refetchOnWindowFocus: false,
        // 4xx не лечится повтором; сеть/5xx — пару раз
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
