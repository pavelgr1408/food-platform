import { Link } from "react-router";
import { useQueries } from "@tanstack/react-query";
import { formatMoney } from "@fp/shared";
import { useApi } from "../app/api-context";
import { Empty, PageTitle, StatusPill, Surface } from "../components/ui";
import { fmtDateTime } from "../lib/format";
import { localOrders } from "../lib/local";

/** UC-TRK-09: API списка заказов нет (пробел №2) — показываем историю этого устройства. */
export function Orders() {
  const api = useApi();
  const list = localOrders.list();
  const results = useQueries({
    queries: list.map((o) => ({ queryKey: ["order", o.id], queryFn: () => api.orders.get(o.id), retry: false, staleTime: 30_000 })),
  });
  return (
    <div className="lt-rise">
      <PageTitle sub="Список хранится на этом устройстве: на другом браузере заказы не появятся.">Мои заказы</PageTitle>
      {list.length === 0 ? (
        <Empty title="Заказов пока нет"><Link to="/" className="font-semibold underline">Выбрать ресторан</Link></Empty>
      ) : (
        <ul className="grid gap-3">
          {list.map((o, i) => {
            const r = results[i]?.data;
            return (
              <li key={o.id}>
                <Link to={`/orders/${o.id}`}>
                  <Surface as="div" className="flex flex-wrap items-center justify-between gap-3 transition-transform hover:-translate-y-0.5 motion-reduce:transition-none">
                    <div>
                      <p className="font-display font-semibold">{o.number}</p>
                      <p className="text-sm text-muted">{fmtDateTime(o.createdAt)}</p>
                    </div>
                    <div className="flex items-center gap-3">
                      {r ? <StatusPill status={r.status} /> : null}
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
