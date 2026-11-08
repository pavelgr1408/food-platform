import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { useInfiniteQuery, useQueries, useQueryClient } from "@tanstack/react-query";
import type { FulfillmentType, OrderStatus, StaffOrder } from "@fp/api";
import { Badge, Button, Empty, Field, Notice, PageTitle, Price, Skeleton, StatusPill, Surface, cx, errorText, fmtTime, useApi } from "@fp/ui";
import { useWorkplace } from "../app/workplace";
import { ago, ToneBadge, useNow } from "../components/bits";
import { CHANNEL_LABEL, ORDER_NUMBER, itemsSummary, payBadge } from "../lib/order";

/** Вкладки очереди — статусы заказа (UC-STF-10). Сервер сам исключает завершённые, если `status` не задан, поэтому у «Истории» он явный. */
const TABS: { id: string; label: string; status: OrderStatus[]; sort: "oldest" | "newest"; empty: string }[] = [
  { id: "new", label: "Входящие", status: ["pending_payment", "accepted"], sort: "oldest", empty: "Новых заказов нет." },
  { id: "cooking", label: "Готовятся", status: ["cooking"], sort: "oldest", empty: "Сейчас ничего не готовится." },
  { id: "ready", label: "Готовы", status: ["ready"], sort: "oldest", empty: "Готовых заказов нет." },
  { id: "route", label: "В пути", status: ["on_delivery"], sort: "oldest", empty: "Курьеры никого не везут." },
  { id: "history", label: "История", status: ["handed_over", "delivered", "closed", "not_received", "cancelled"], sort: "newest", empty: "Завершённых заказов пока нет." },
];
const PAGE = 50;
const POLL_MS = 5_000;

/** Главный экран кассы: поиск по номеру и очередь заказов ресторана (UC-STF-01, UC-STF-10). */
export function Queue() {
  const api = useApi();
  const qc = useQueryClient();
  const nav = useNavigate();
  const wp = useWorkplace();
  const rid = wp.current!.id;
  const tz = wp.current!.timezone;
  const now = useNow();

  const [tab, setTab] = useState(TABS[0]!.id);
  const [fulfillment, setFulfillment] = useState<FulfillmentType | "">("");
  const active = TABS.find((t) => t.id === tab)!;
  const fl = fulfillment || undefined;

  /* ---- поиск по номеру ---- */
  const [number, setNumber] = useState("");
  const [searchErr, setSearchErr] = useState<string | null>(null);
  const [searching, setSearching] = useState(false);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const n = number.trim().toUpperCase();
    if (!ORDER_NUMBER.test(n)) { setSearchErr("Номер не по формату. Пример: R1-261108-0007."); return; }
    setSearchErr(null); setSearching(true);
    try {
      const o = await qc.fetchQuery({ queryKey: ["staff-order", n], queryFn: () => api.orders.search(n), staleTime: 0 });
      nav(`/o/${encodeURIComponent(o.number)}`);
    } catch (err) {
      setSearchErr(errorText(err, undefined, { ORDER_NOT_FOUND: "Заказ не найден. Проверьте номер: возможно, он из другого ресторана." }));
    } finally { setSearching(false); }
  };

  /* ---- очередь активной вкладки ---- */
  const list = useInfiniteQuery({
    queryKey: ["queue", rid, tab, fl],
    queryFn: ({ pageParam, signal }) => api.orders.queue({ restaurantId: rid, status: active.status, fulfillment: fl, sort: active.sort, limit: PAGE, offset: pageParam }, signal),
    initialPageParam: 0,
    getNextPageParam: (last) => (last.offset + last.items.length < last.total ? last.offset + last.items.length : undefined),
    refetchInterval: POLL_MS,
    // держим прежний список только при смене фильтра внутри вкладки; при переходе на другую вкладку — скелет, а не чужое пустое состояние
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[2] === tab ? prev : undefined),
  });
  const items: StaffOrder[] = list.data?.pages.flatMap((p) => p.items) ?? [];
  const total = list.data?.pages[0]?.total ?? 0;

  /* ---- счётчики остальных вкладок: легкий запрос на одну запись, берём только total ---- */
  const counts = useQueries({
    queries: TABS.map((t) => ({
      queryKey: ["queue-count", rid, t.id, fl],
      queryFn: ({ signal }: { signal: AbortSignal }) => api.orders.queue({ restaurantId: rid, status: t.status, fulfillment: fl, limit: 1 }, signal),
      refetchInterval: POLL_MS * 2,
      // «История» растёт бесконечно — счётчик на ней только путает
      enabled: t.id !== "history",
    })),
  });

  return (
    <div className="lt-rise grid gap-5">
      <PageTitle sub={`${wp.current!.name} · очередь обновляется сама`}>Касса</PageTitle>

      <Surface>
        <form onSubmit={submit} className="flex flex-wrap items-end gap-3" role="search" aria-label="Поиск заказа по номеру">
          <Field label="Номер заказа" className="min-w-64 flex-1" value={number} autoComplete="off" autoCapitalize="characters" spellCheck={false}
            placeholder="R1-261108-0007" onChange={(e) => { setNumber(e.target.value); setSearchErr(null); }} error={searchErr}
            hint="Назовёт клиент или покажет в приложении. Регистр не важен." />
          <Button type="submit" variant="accent" disabled={searching || !number.trim()} className="mb-[1.6rem]">{searching ? "Ищем…" : "Найти заказ"}</Button>
        </form>
      </Surface>

      <div className="flex flex-wrap items-center justify-between gap-3">
        <div role="tablist" aria-label="Статус заказов" className="flex flex-wrap gap-1 rounded-full bg-ink/5 p-1">
          {TABS.map((t, i) => {
            const n = counts[i]?.data?.total;
            return (
              <button key={t.id} type="button" role="tab" aria-selected={tab === t.id} onClick={() => setTab(t.id)}
                className={cx("inline-flex min-h-11 items-center gap-2 rounded-full px-4 font-semibold", tab === t.id ? "bg-ink text-white" : "hover:bg-ink/5")}>
                {t.label}
                {n !== undefined && t.id !== "history" ? <span className={cx("min-w-6 rounded-full px-1.5 text-center text-sm", n > 0 ? (tab === t.id ? "bg-lemon text-ink" : "bg-tomato text-white") : "bg-ink/10")} aria-label={`заказов: ${n}`}>{n}</span> : null}
              </button>
            );
          })}
        </div>
        <label className="flex items-center gap-2 text-sm font-semibold">
          Получение
          <select value={fulfillment} onChange={(e) => setFulfillment(e.target.value as FulfillmentType | "")} className="min-h-11 rounded-input border-2 border-ink bg-white px-3 text-base font-normal">
            <option value="">Любое</option>
            <option value="pickup">Самовывоз</option>
            <option value="delivery">Доставка</option>
          </select>
        </label>
      </div>

      <section role="tabpanel" aria-label={active.label} aria-busy={list.isFetching}>
        {list.isLoading ? (
          <div role="status" aria-label="Загружаем очередь" className="grid gap-3"><Skeleton className="h-24 rounded-card" /><Skeleton className="h-24 rounded-card" /></div>
        ) : list.isError ? (
          <Notice>{errorText(list.error, "Не удалось загрузить очередь.")} <button className="font-bold underline" onClick={() => void list.refetch()}>Повторить</button></Notice>
        ) : items.length === 0 ? (
          <Empty title={active.empty} />
        ) : (
          <>
            <ul className="grid gap-3">
              {items.map((o) => <li key={o.id}><Row o={o} tz={tz} now={now} /></li>)}
            </ul>
            <div className="mt-4 flex flex-wrap items-center justify-between gap-3 text-sm text-muted">
              <span>Показано {items.length} из {total}</span>
              {list.hasNextPage ? <Button variant="outline" disabled={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>{list.isFetchingNextPage ? "Загружаем…" : "Показать ещё"}</Button> : null}
            </div>
          </>
        )}
      </section>
    </div>
  );
}

function Row({ o, tz, now }: { o: StaffOrder; tz: string; now: number }) {
  const pay = payBadge(o);
  const who = [o.contact?.name, o.contact?.phone].filter(Boolean).join(" · ");
  return (
    <Link to={`/o/${encodeURIComponent(o.number)}`} className="block rounded-card bg-white p-4 shadow-[0_1px_0_var(--lt-border),0_10px_28px_-16px_rgba(18,19,26,0.28)] transition-shadow hover:shadow-[0_1px_0_var(--lt-border),0_14px_32px_-14px_rgba(18,19,26,0.4)] focus-visible:outline focus-visible:outline-[3px] focus-visible:outline-offset-2 focus-visible:outline-ink">
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
        <div className="min-w-0">
          <p className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span className="lt-num text-xl font-bold">{o.number}</span>
            <StatusPill status={o.status} />
            {o.channel === "staff_fax" ? <Badge tone="neutral">{CHANNEL_LABEL.staff_fax}</Badge> : null}
          </p>
          <p className="mt-1 text-sm text-muted">
            {fmtTime(o.createdAt, tz)} · {ago(o.createdAt, now)} · {o.fulfillment.type === "pickup" ? "самовывоз" : "доставка"}{who ? ` · ${who}` : ""}
          </p>
          <p className="mt-2">{itemsSummary(o)}</p>
        </div>
        <div className="flex flex-col items-end gap-2">
          <Price money={o.total} />
          <ToneBadge tone={pay.tone}>{pay.text}</ToneBadge>
        </div>
      </div>
    </Link>
  );
}
