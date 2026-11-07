import { Link } from "react-router";
import { useQueries } from "@tanstack/react-query";
import { isApiError } from "@fp/api";
import { formatMoney } from "@fp/shared";
import { useApi } from "../app/api-context";
import { Badge, Empty, PageTitle, Skeleton, StatusPill, Surface } from "../components/ui";
import { fmtDateTime } from "../lib/format";
import { localOrders } from "../lib/local";

/** UC-TRK-09: API списка заказов нет (пробел №2) — показываем историю этого устройства. */
export function Orders() {
  const api = useApi();
  const list = localOrders.list();
  const results = useQueries({
    queries: list.map((o) => ({ queryKey: ["order", o.id], queryFn: () => api.orders.get(o.id), retry: false, staleTime: 30_000 })),
  });
  const rests = useQueries({
    queries: [...new Set(list.map((o) => o.restaurantId))].map((id) => ({ queryKey: ["restaurant", id], queryFn: () => api.restaurants.get(id), retry: false, staleTime: 5 * 60_000 })),
  });
  const restName = (id: string) => rests.find((r) => r.data?.id === id)?.data?.name;

  return (
    <div className="lt-rise">
      <PageTitle sub="Список хранится на этом устройстве: на другом браузере заказы не появятся.">Мои заказы</PageTitle>
      {list.length === 0 ? (
        <Empty title="Заказов пока нет"><Link to="/" className="font-semibold underline">Выбрать ресторан</Link></Empty>
      ) : (
        <ul className="grid gap-3">
          {list.map((o, i) => {
            const res = results[i];
            const r = res?.data;
            const gone = !!res?.error && isApiError(res.error) && res.error.status === 404;
            const summary = r ? r.items.map((it) => `${it.name} × ${it.quantity}`).join(", ") : null;
            return (
              <li key={o.id}>
                <Link to={`/orders/${o.id}`} className="block rounded-card focus-visible:outline-offset-4">
                  <Surface as="div" className="grid gap-2 transition-transform hover:-translate-y-0.5 motion-reduce:transition-none">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <p className="font-display font-semibold">{o.number}</p>
                      {r ? <StatusPill status={r.status} /> : gone ? <Badge>Не найден</Badge> : res?.isLoading ? <Skeleton className="h-7 w-24 rounded-full" /> : null}
                    </div>
                    {restName(o.restaurantId) ? <p className="font-medium">{restName(o.restaurantId)}</p> : null}
                    {summary ? <p className="line-clamp-2 text-sm text-muted">{summary}</p> : null}
                    <div className="flex items-center justify-between gap-3">
                      <p className="text-sm text-muted">{fmtDateTime(o.createdAt)}{r ? ` · ${r.fulfillment.type === "delivery" ? "доставка" : "самовывоз"}` : ""}</p>
                      <span className="font-semibold">{formatMoney(o.totalAmount, o.currency)}</span>
                    </div>
                  </Surface>
                </Link>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
