import { useEffect, useState } from "react";
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
  const [list, setList] = useState(() => localOrders.list());
  const [pruned, setPruned] = useState(0);
  const results = useQueries({
    queries: list.map((o) => ({ queryKey: ["order", o.id], queryFn: () => api.orders.get(o.id), retry: false, staleTime: 30_000 })),
  });
  const rests = useQueries({
    queries: [...new Set(list.map((o) => o.restaurantId))].map((id) => ({ queryKey: ["restaurant", id], queryFn: () => api.restaurants.get(id), retry: false, staleTime: 5 * 60_000 })),
  });
  /* Сервер сказал 404 — заказа нет (БД пересоздана или запись из другого стенда): убираем из истории устройства. */
  const goneIds = list.filter((_, i) => { const e = results[i]?.error; return !!e && isApiError(e) && e.status === 404; }).map((o) => o.id);
  const goneKey = goneIds.join(",");
  useEffect(() => {
    if (!goneKey) return;
    const ids = goneKey.split(",");
    localOrders.remove(ids);
    setList(localOrders.list());
    setPruned((n) => n + ids.length);
  }, [goneKey]);
  const restName = (id: string) => rests.find((r) => r.data?.id === id)?.data?.name;

  return (
    <div className="lt-rise">
      <PageTitle sub="Список хранится на этом устройстве: на другом браузере заказы не появятся.">Мои заказы</PageTitle>
      {pruned > 0 ? <p role="status" className="mb-3 rounded-card bg-lemon/40 px-4 py-2 text-sm">Убрано из истории заказов, которых нет на сервере: {pruned}.</p> : null}
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
