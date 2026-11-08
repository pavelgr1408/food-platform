import { useState } from "react";
import { Link } from "react-router";
import { useInfiniteQuery } from "@tanstack/react-query";
import type { Promotion, PromotionScope, PromotionStatus } from "@fp/api";
import { Badge, Button, Empty, Notice, PageTitle, Skeleton, Surface, cx, errorText, useApi } from "@fp/ui";
import { useStaffAuth } from "../app/auth";
import { useWorkplace } from "../app/workplace";
import { ToneBadge, useNow } from "../components/bits";
import { PHASE_LABEL, PHASE_TONE, conditionsText, discountText, fmtPeriod, phaseOf } from "../lib/promo";

const FILTERS: { id: string; label: string; status?: PromotionStatus[] }[] = [
  { id: "all", label: "Все" },
  { id: "draft", label: "Черновики", status: ["draft"] },
  { id: "active", label: "Действующие", status: ["active"] },
  { id: "completed", label: "Завершённые", status: ["completed"] },
];
const PAGE = 20;

/** Управление акциями: список (UC-PRM-08) со всеми статусами — черновики и завершённые тоже. Владелец видит свои local, HQ — все. */
export function Promotions() {
  const api = useApi();
  const auth = useStaffAuth();
  const wp = useWorkplace();
  const now = useNow();
  const [filter, setFilter] = useState("all");
  const [scope, setScope] = useState<PromotionScope | "">("");
  const [restaurantId, setRestaurantId] = useState("");
  const f = FILTERS.find((x) => x.id === filter)!;

  const list = useInfiniteQuery({
    queryKey: ["promotions", filter, scope, restaurantId],
    queryFn: ({ pageParam }) => api.promotions.manage({ status: f.status, scope: scope || undefined, restaurantId: restaurantId || undefined, limit: PAGE, offset: pageParam }),
    initialPageParam: 0,
    getNextPageParam: (last) => (last.offset + last.items.length < last.total ? last.offset + last.items.length : undefined),
    retry: false,
  });
  const items = list.data?.pages.flatMap((p) => p.items) ?? [];
  const total = list.data?.pages[0]?.total ?? 0;
  const byId = new Map(wp.restaurants.map((r) => [r.id, r]));

  return (
    <div className="lt-rise grid gap-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <PageTitle sub={auth.isHq ? "Все акции сети: национальные и локальные." : "Акции ваших ресторанов."}>Акции</PageTitle>
        <Link to="/promotions/new" className="inline-flex min-h-[var(--lt-tap-cta)] items-center rounded-full bg-lemon px-6 text-lg font-semibold text-ink hover:brightness-95">Новая акция</Link>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-3">
        <div role="tablist" aria-label="Статус акции" className="flex flex-wrap gap-1 rounded-full bg-ink/5 p-1">
          {FILTERS.map((x) => (
            <button key={x.id} type="button" role="tab" aria-selected={filter === x.id} onClick={() => setFilter(x.id)}
              className={cx("min-h-11 rounded-full px-4 font-semibold", filter === x.id ? "bg-ink text-white" : "hover:bg-ink/5")}>{x.label}</button>
          ))}
        </div>
        <div className="flex flex-wrap items-center gap-3">
          {auth.isHq ? (
            <label className="flex items-center gap-2 text-sm font-semibold">Охват
              <select value={scope} onChange={(e) => setScope(e.target.value as PromotionScope | "")} className="min-h-11 rounded-input border-2 border-ink bg-white px-3 text-base font-normal">
                <option value="">Любой</option><option value="national">Национальные</option><option value="local">Локальные</option>
              </select>
            </label>
          ) : null}
          {wp.restaurants.length > 1 ? (
            <label className="flex items-center gap-2 text-sm font-semibold">Ресторан
              <select value={restaurantId} onChange={(e) => setRestaurantId(e.target.value)} className="min-h-11 max-w-56 rounded-input border-2 border-ink bg-white px-3 text-base font-normal">
                <option value="">Все</option>{wp.restaurants.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
              </select>
            </label>
          ) : null}
        </div>
      </div>

      <section role="tabpanel" aria-label={f.label} aria-busy={list.isFetching}>
        {list.isLoading ? <div role="status" aria-label="Загружаем акции" className="grid gap-3"><Skeleton className="h-24 rounded-card" /><Skeleton className="h-24 rounded-card" /></div>
          : list.isError ? <Notice>{errorText(list.error, "Не удалось загрузить акции.")} <button className="font-bold underline" onClick={() => void list.refetch()}>Повторить</button></Notice>
          : items.length === 0 ? <Empty title="Акций нет">Создайте первую: черновик можно проверить и активировать, когда будете готовы.</Empty>
          : (
            <>
              <ul className="grid gap-3">{items.map((p) => <li key={p.id}><Card p={p} restaurant={p.restaurantId ? byId.get(p.restaurantId)?.name : undefined} tz={(p.restaurantId ? byId.get(p.restaurantId) : wp.current)?.timezone} now={now} /></li>)}</ul>
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

function Card({ p, restaurant, tz, now }: { p: Promotion; restaurant?: string; tz?: string; now: number }) {
  const ph = phaseOf(p, now);
  return (
    <Link to={`/promotions/${p.id}`} className="block rounded-card focus-visible:outline focus-visible:outline-[3px] focus-visible:outline-offset-2 focus-visible:outline-ink">
      <Surface as="div" className="transition-shadow hover:shadow-[0_1px_0_var(--lt-border),0_14px_32px_-14px_rgba(18,19,26,0.4)]">
        <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
          <div className="min-w-0">
            <p className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <span className="text-lg font-semibold">{p.title}</span>
              <ToneBadge tone={PHASE_TONE[ph]}>{PHASE_LABEL[ph]}</ToneBadge>
              <Badge tone="neutral">{p.scope === "national" ? "Вся сеть" : restaurant ?? "Ресторан"}</Badge>
            </p>
            <p className="mt-1 text-sm text-muted">{fmtPeriod(p.startsAt, p.endsAt, tz)}</p>
            <p className="mt-1 text-sm text-muted">{conditionsText(p)} · приоритет {p.priority}</p>
          </div>
          <span className="inline-block whitespace-nowrap rounded-full bg-tomato px-4 py-1.5 font-display text-lg font-semibold text-white">{discountText(p.discount, p.currency)}</span>
        </div>
      </Surface>
    </Link>
  );
}
