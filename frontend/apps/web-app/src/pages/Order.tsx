import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { isApiError, type ApiError, type Order as OrderT } from "@fp/api";
import { formatMoney } from "@fp/shared";
import { useApi } from "../app/api-context";
import { Button, Empty, Notice, Price, Skeleton, Spinner, StatusPill, Surface } from "../components/ui";
import { Thumb } from "../components/art";
import { MapPicker } from "../components/LazyMap";
import { errorText } from "../lib/errors";
import { agoSeconds, fmtDateTime, fmtTime } from "../lib/format";
import { PAYMENT_MODE } from "../lib/env";
import { localPayments } from "../lib/local";
import { isTerminal, REASON_LABEL, stepsFor, STATUS_LABEL } from "../lib/status";

/** Опрос: статус 3–5 с пока не терминальный, курьер 15–30 с (активных обновлений в API нет — пробел №8). */
const POLL_ORDER = 4000;

export function Order() {
  const { orderId = "" } = useParams();
  const api = useApi();
  const q = useQuery({
    queryKey: ["order", orderId],
    queryFn: ({ signal }) => api.orders.get(orderId, signal),
    retry: (n, e) => !(isApiError(e) && e.status === 404) && n < 2,
    refetchInterval: (query) => (query.state.data && isTerminal(query.state.data.status) && query.state.data.paymentStatus !== "pending" ? false : POLL_ORDER),
  });
  const rest = useQuery({ queryKey: ["restaurant", q.data?.restaurantId], queryFn: () => api.restaurants.get(q.data!.restaurantId), enabled: !!q.data, retry: false, staleTime: 5 * 60_000 });
  const tz = rest.data?.timezone;
  const menu = useQuery({ queryKey: ["menu", q.data?.restaurantId], queryFn: () => api.restaurants.menu(q.data!.restaurantId), enabled: !!q.data, retry: false, staleTime: 5 * 60_000 });
  const names = useMemo(() => {
    const mods = new Map<string, string>(), cats = new Map<string, string>();
    for (const c of menu.data?.categories ?? []) for (const it of c.items) { cats.set(it.id, c.name); for (const m of it.modifiers) mods.set(m.id, m.name); }
    return { mods, cats };
  }, [menu.data]);

  if (q.isLoading) return <div role="status" aria-label="Загружаем заказ" className="grid gap-4"><Skeleton className="h-32 rounded-card" /><Skeleton className="h-24 rounded-card" /><Skeleton className="h-40 rounded-card" /></div>;
  if (q.isError) {
    return (
      <Empty title={isApiError(q.error) && q.error.status === 404 ? "Заказ не найден" : "Не удалось загрузить заказ"}>
        <p>{errorText(q.error)}</p>
        <Link to="/orders" className="mt-3 inline-block font-semibold underline">Мои заказы</Link>
      </Empty>
    );
  }
  const o = q.data!;
  const delivery = o.fulfillment.type === "delivery";
  const steps = stepsFor(delivery ? "delivery" : "pickup", o.paymentMethod === "online_card");
  const idx = steps.indexOf(o.status === "closed" ? (steps[steps.length - 1] as typeof o.status) : o.status);
  const dead = o.status === "cancelled" || o.status === "not_received";

  return (
    <div className="lt-rise grid gap-5 lg:grid-cols-[1fr_380px]">
      <div className="grid content-start gap-5">
        <StatusHero o={o} dead={dead} />

        {dead ? <DeadNotice o={o} /> : <Stepper steps={steps} current={idx} />}
        {!dead ? <EtaBlock o={o} tz={tz} /> : null}

        {o.paymentMethod === "online_card" ? <OnlinePayment o={o} /> : null}
        {o.paymentMethod === "pos" ? (
          <Surface>
            <h2 className="text-[length:var(--lt-fs-price)]">Оплата на кассе</h2>
            <p className="mt-2 text-muted">Назовите сотруднику номер заказа:</p>
            <p className="mt-1 font-display text-3xl font-semibold" data-testid="pos-number">{o.number}</p>
            {o.paymentStatus === "paid" ? <p className="mt-2 font-semibold text-ink">Оплачено</p> : null}
          </Surface>
        ) : null}
        {o.paymentMethod === "courier_card" ? <Surface><h2 className="text-[length:var(--lt-fs-price)]">Оплата картой курьеру</h2><p className="mt-2 text-muted">Оплатите картой при получении. Ссылку на оплату получит курьер.</p></Surface> : null}

        {!dead && o.fulfillment.type === "pickup" && (o.status === "pending_payment" || o.status === "accepted") ? <PickupSlot o={o} tz={tz} /> : null}
        {delivery && o.status !== "cancelled" ? <DeliveryBlock o={o} /> : null}
      </div>

      <aside className="grid content-start gap-5 lg:sticky lg:top-20 lg:self-start">
        <Surface>
          <h2 className="mb-3 text-[length:var(--lt-fs-price)]">Состав</h2>
          <ul className="grid gap-3">
            {o.items.map((i) => (
              <li key={`${i.menuItemId}|${i.modifierIds.join(",")}`} className="flex items-center gap-3">
                <Thumb name={i.name} category={names.cats.get(i.menuItemId)} seed={i.menuItemId} className="size-14 shrink-0 rounded-input" />
                <div className="min-w-0 flex-1">
                  <p className="leading-snug">{i.name} × {i.quantity}</p>
                  {i.modifierIds.length ? <p className="text-sm text-muted">{i.modifierIds.map((id) => names.mods.get(id)).filter(Boolean).join(", ") || `Добавок: ${i.modifierIds.length}`}</p> : null}
                </div>
                <span className="font-medium">{formatMoney(i.lineTotal.amount, i.lineTotal.currency)}</span>
              </li>
            ))}
          </ul>
          <dl className="mt-3 grid gap-1 border-t border-dashed border-ink/30 pt-3 text-sm">
            <div className="flex justify-between"><dt>Подытог</dt><dd>{formatMoney(o.subtotal.amount, o.subtotal.currency)}</dd></div>
            {(o.appliedPromotions ?? []).map((p) => <div key={p.promotionId ?? p.title} className="flex justify-between"><dt>Акция: {p.title}</dt><dd>−{formatMoney(p.discount?.amount ?? 0, p.discount?.currency)}</dd></div>)}
          </dl>
          <p className="mt-3 flex items-center justify-between"><span className="font-semibold">Итого</span><Price money={o.total} big /></p>
          <p className="mt-3 text-xs text-muted">
            {o.fulfillment.type === "pickup" ? "Самовывоз" : `Доставка: ${o.fulfillment.address?.raw ?? ""}`} · {fmtDateTime(o.createdAt, tz)}
          </p>
          {rest.data ? <p className="mt-1 text-xs text-muted"><Link to={`/r/${rest.data.id}`} className="underline">{rest.data.name}</Link>, {rest.data.address}</p> : null}
        </Surface>
      </aside>
    </div>
  );
}

function Stepper({ steps, current }: { steps: string[]; current: number }) {
  return (
    <ol aria-label="Этапы заказа" className="grid rounded-card bg-white p-4 shadow-[0_1px_0_var(--lt-border)]" style={{ gridTemplateColumns: `repeat(${steps.length}, minmax(0, 1fr))` }}>
      {steps.map((s, i) => {
        const done = i < current, now = i === current;
        return (
          <li key={s} aria-current={now ? "step" : undefined} className="relative flex flex-col items-center gap-1.5 text-center">
            {i > 0 ? <span aria-hidden className={`absolute right-1/2 top-3.5 h-0.5 w-full -translate-y-1/2 ${i <= current ? "bg-ink" : "bg-ink/15"}`} /> : null}
            <span aria-hidden className={`relative z-10 grid size-7 place-items-center rounded-full border-2 text-sm font-bold ${done ? "border-ink bg-ink text-white" : now ? "border-tomato bg-tomato text-white" : "border-ink/20 bg-white text-faint"}`}>{done ? "✓" : i + 1}</span>
            <span className={`text-[11px] leading-tight sm:text-sm ${now ? "font-semibold" : done ? "" : "text-faint"}`}>{STATUS_LABEL[s as keyof typeof STATUS_LABEL]}</span>
          </li>
        );
      })}
    </ol>
  );
}

const HERO: Partial<Record<OrderT["status"], { bg: string; text: string }>> = {
  pending_payment: { bg: "bg-lemon", text: "Осталось оплатить — иначе заказ отменится через 15 минут." },
  accepted: { bg: "bg-white", text: "Ресторан получил заказ." },
  cooking: { bg: "bg-white", text: "Повара уже готовят." },
  ready: { bg: "bg-mint", text: "Можно забирать." },
  on_delivery: { bg: "bg-tomato text-white", text: "Курьер уже едет к вам." },
  delivered: { bg: "bg-ink text-white", text: "Приятного аппетита!" },
  handed_over: { bg: "bg-ink text-white", text: "Приятного аппетита!" },
  closed: { bg: "bg-ink text-white", text: "Заказ завершён." },
};

function StatusHero({ o, dead }: { o: OrderT; dead: boolean }) {
  const [copied, setCopied] = useState(false);
  const h = dead ? { bg: "bg-coral/40", text: "" } : HERO[o.status] ?? { bg: "bg-white", text: "" };
  const copy = async () => {
    try { await navigator.clipboard.writeText(o.number); setCopied(true); setTimeout(() => setCopied(false), 2000); } catch { /* буфер недоступен */ }
  };
  return (
    <header className={`rounded-card p-5 shadow-[0_1px_0_var(--lt-border)] ${h.bg}`}>
      <p className="text-sm font-semibold opacity-80">Заказ</p>
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="font-display text-[length:var(--lt-fs-h2)] font-semibold md:text-[length:var(--lt-fs-h1)]" aria-label={`Номер заказа ${o.number}`}>{o.number}</h1>
        <button type="button" onClick={copy} className="min-h-11 rounded-full border-2 border-current px-4 text-sm font-semibold">{copied ? "Скопировано" : "Копировать"}</button>
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <StatusPill status={o.status} />
        {o.statusReason ? <span className="font-medium">{REASON_LABEL[o.statusReason] ?? o.statusReason}</span> : null}
      </div>
      {h.text ? <p className="mt-2 font-medium">{h.text}</p> : null}
    </header>
  );
}

function DeadNotice({ o }: { o: OrderT }) {
  return (
    <Surface className="bg-coral/30">
      <h2 className="text-[length:var(--lt-fs-price)]">{o.status === "cancelled" ? "Заказ отменён" : "Заказ не получен"}</h2>
      <p className="mt-1">{o.statusReason ? REASON_LABEL[o.statusReason] ?? o.statusReason : null}</p>
      {o.refundStatus === "pending" ? <p className="mt-2 font-medium">Возврат денег выполняется.</p> : null}
      {o.refundStatus === "refunded" ? <p className="mt-2 font-medium">Деньги возвращены.</p> : null}
      {o.refundStatus === "failed" ? <p className="mt-2 font-semibold">Возврат не прошёл автоматически. Обратитесь в поддержку ресторана.</p> : null}
    </Surface>
  );
}

function EtaBlock({ o, tz }: { o: OrderT; tz?: string }) {
  const api = useApi();
  const eta = useQuery({
    queryKey: ["eta", o.id, o.status], queryFn: () => api.orders.eta(o.id), retry: false,
    refetchInterval: isTerminal(o.status) ? false : 15_000,
  });
  const e = eta.data;
  if (!e || o.status === "pending_payment") return null;
  const mins = e.estimateMinutes;
  const at = o.fulfillment.type === "delivery" ? e.deliveryAt ?? e.readyAt : e.readyAt;
  return (
    <Surface>
      <h2 className="text-[length:var(--lt-fs-price)]">{o.status === "ready" ? "Готов — можно забирать" : "Когда будет готово"}</h2>
      {o.status !== "ready" && mins !== undefined ? <p className="mt-1 text-lg font-semibold">{e.approximate ? "около " : ""}{mins} мин{at ? ` · к ${fmtTime(at, tz)}` : ""}</p> : null}
      {o.fulfillment.type === "delivery" && !e.deliveryAt && o.delivery?.status === "failed" ? <p className="mt-1 text-muted">Курьер не найден — заберите заказ в ресторане.</p> : null}
    </Surface>
  );
}

/* ---------------- Онлайн-оплата: UC-PAY-01…05 ---------------- */
function OnlinePayment({ o }: { o: OrderT }) {
  const api = useApi();
  const qc = useQueryClient();
  const payable = o.status === "pending_payment";
  const methods = useQuery({
    queryKey: ["pay-methods", o.id], enabled: payable,
    queryFn: () => api.payments.methods(o.id),
    // заказ ещё не дошёл до payment по Kafka: 404 + Retry-After → повторяем (UC-PAY-01)
    retry: (n, e) => isApiError(e) && e.status === 404 && n < 5, retryDelay: (_n, e) => ((e as ApiError).retryAfter ?? 1) * 1000,
  });
  const online = methods.data?.find((m) => m.type === "online_card");
  const paymentId = localPayments.get(o.id);
  const pay = useQuery({ queryKey: ["payment", o.id, paymentId], enabled: !!paymentId && o.paymentStatus === "declined", queryFn: () => api.payments.get(o.id, paymentId!), retry: false });

  const keyRef = useRef<string | null>(null);
  const start = useMutation({
    mutationFn: () => { keyRef.current ??= api.newIdempotencyKey(); return api.payments.initiate(o.id, `${window.location.origin}/orders/${o.id}`, keyRef.current); },
    onSuccess: ({ payment }) => {
      keyRef.current = null;
      localPayments.set(o.id, payment.id);
      qc.invalidateQueries({ queryKey: ["order", o.id] });
      if (PAYMENT_MODE === "redirect" && payment.status === "pending" && payment.paymentUrl) window.location.assign(payment.paymentUrl);
    },
    onError: (e) => {
      if (isApiError(e) && e.status >= 400 && e.status < 500) keyRef.current = null;
      if (isApiError(e) && e.code === "PAYMENT_IN_PROGRESS" && typeof e.extra.paymentId === "string") localPayments.set(o.id, e.extra.paymentId);
      qc.invalidateQueries({ queryKey: ["order", o.id] });
    },
  });
  const err = start.error as ApiError | null;

  // нет «Оплатить» после перезахода: paymentUrl в Order недоступен (пробел №9)
  const [waited, setWaited] = useState(0);
  useEffect(() => { if (o.paymentStatus !== "pending") return; const t = setInterval(() => setWaited((w) => w + 1), 1000); return () => clearInterval(t); }, [o.paymentStatus]);

  if (o.paymentStatus === "paid") return <Surface><h2 className="text-[length:var(--lt-fs-price)]">Оплачено</h2><p className="mt-1 text-muted">Спасибо! Заказ передан на кухню.</p></Surface>;
  if (!payable && o.paymentStatus !== "unknown") return null;

  return (
    <Surface>
      <h2 className="text-[length:var(--lt-fs-price)]">Оплата картой онлайн</h2>
      {o.paymentStatus === "unknown" ? <Notice tone="info" className="mt-3">Проверяем платёж. Не платите повторно — статус обновится сам.</Notice> : null}
      {o.paymentStatus === "pending" && paymentId ? (
        <Notice tone="info" className="mt-3">
          Проверяем оплату{waited > 20 ? " — банк отвечает дольше обычного, оплата обрабатывается" : "…"}
        </Notice>
      ) : null}
      {o.paymentStatus === "declined" ? (
        <Notice className="mt-3">Банк отказал в оплате{pay.data?.declineReason ? `: ${pay.data.declineReason}` : ""}{pay.data ? ` (попытка ${pay.data.attempt})` : ""}. Попробуйте ещё раз или другую карту.</Notice>
      ) : null}
      {methods.isLoading ? <Spinner label="Готовим оплату…" /> : null}
      {methods.isError ? <Notice className="mt-3">{errorText(methods.error)}</Notice> : null}
      {err ? <Notice className="mt-3">{err.code === "PAYMENT_IN_PROGRESS" ? "Платёж уже обрабатывается. Подождите результата." : errorText(err)}</Notice> : null}
      <div className="mt-4">
        <Button variant="accent" cta className="w-full sm:w-auto" disabled={!online?.enabled || start.isPending || o.paymentStatus === "unknown" || (o.paymentStatus === "pending" && !!paymentId)} onClick={() => start.mutate()}>
          {start.isPending ? "Переходим к оплате…" : `Оплатить ${formatMoney(o.total.amount, o.total.currency)}`}
        </Button>
        {online && !online.enabled ? <p className="mt-2 text-sm text-muted">Сейчас оплата недоступна.</p> : null}
        <p className="mt-2 text-xs text-muted">Данные карты вводятся на странице банка, к нам они не попадают. Неоплаченный заказ отменяется автоматически через 15 минут.</p>
      </div>
    </Surface>
  );
}

/* ---------------- Слот самовывоза: UC-TRK-03 ---------------- */
function PickupSlot({ o, tz }: { o: OrderT; tz?: string }) {
  const api = useApi();
  const qc = useQueryClient();
  // по умолчанию — через полчаса, округлено до 5 минут, в локальном формате datetime-local
  const [from, setFrom] = useState(() => {
    const d = new Date(Date.now() + 30 * 60_000);
    d.setMinutes(Math.ceil(d.getMinutes() / 5) * 5, 0, 0);
    d.setMinutes(d.getMinutes() - d.getTimezoneOffset());
    return d.toISOString().slice(0, 16);
  });
  const [mins, setMins] = useState(30);
  const save = useMutation({
    mutationFn: () => {
      const f = new Date(from);
      return api.orders.setPickupSlot(o.id, { from: f.toISOString(), to: new Date(f.getTime() + mins * 60_000).toISOString() });
    },
    onSuccess: () => { qc.invalidateQueries({ queryKey: ["order", o.id] }); qc.invalidateQueries({ queryKey: ["eta", o.id] }); },
  });
  return (
    <Surface>
      <h2 className="text-[length:var(--lt-fs-price)]">Время самовывоза</h2>
      {o.pickupSlot ? <p className="mt-1 font-semibold">Заберёте с {fmtTime(o.pickupSlot.from, tz)} до {fmtTime(o.pickupSlot.to, tz)}</p> : <p className="mt-1 text-muted">По умолчанию — как только будет готово. Можно выбрать удобный интервал.</p>}
      <form className="mt-3 grid gap-3 sm:grid-cols-[1fr_auto_auto]" onSubmit={(e) => { e.preventDefault(); if (from) save.mutate(); }}>
        <div>
          <label htmlFor="slot-from" className="mb-1 block text-sm font-semibold">С какого времени{tz ? ` (${tz})` : ""}</label>
          <input id="slot-from" type="datetime-local" required value={from} onChange={(e) => setFrom(e.target.value)} className="min-h-[var(--lt-tap)] w-full rounded-input border-2 border-ink bg-white px-3" />
        </div>
        <div>
          <label htmlFor="slot-len" className="mb-1 block text-sm font-semibold">Длительность</label>
          <select id="slot-len" value={mins} onChange={(e) => setMins(+e.target.value)} className="min-h-[var(--lt-tap)] w-full rounded-input border-2 border-ink bg-white px-3">
            {[15, 30, 60, 120, 240].map((m) => <option key={m} value={m}>{m < 60 ? `${m} мин` : `${m / 60} ч`}</option>)}
          </select>
        </div>
        <Button type="submit" variant="primary" className="self-end" disabled={save.isPending || !from}>Сохранить</Button>
      </form>
      {save.error ? <Notice className="mt-3">{errorText(save.error)}</Notice> : null}
    </Surface>
  );
}

/* ---------------- Доставка: UC-TRK-04…06 ---------------- */
function DeliveryBlock({ o }: { o: OrderT }) {
  const api = useApi();
  const d = useQuery({
    queryKey: ["delivery", o.id], queryFn: () => api.delivery.get(o.id),
    retry: (n, e) => isApiError(e) && e.status === 404 && n < 5, retryDelay: (_n, e) => ((e as ApiError).retryAfter ?? 1) * 1000,
    refetchInterval: (qq) => (qq.state.data && ["delivered", "failed"].includes(qq.state.data.status) ? false : 5000),
  });
  const moving = d.data?.status === "picked_up" || d.data?.status === "en_route";
  const loc = useQuery({ queryKey: ["courier", o.id], queryFn: () => api.delivery.courierLocation(o.id), enabled: moving, retry: false, refetchInterval: 20_000 });

  if (d.isLoading || (d.isError && isApiError(d.error) && d.error.status === 404)) {
    return <Surface><h2 className="text-[length:var(--lt-fs-price)]">Доставка</h2><p className="mt-1 text-muted">{o.status === "pending_payment" ? "Начнём искать курьера после оплаты." : "Ищем курьера…"}</p></Surface>;
  }
  if (!d.data) return null;
  const del = d.data;
  const label: Record<string, string> = { searching: "Ищем курьера", assigned: "Курьер назначен", picked_up: "Курьер забрал заказ", en_route: "Курьер в пути", delivered: "Доставлен", failed: "Не доставлен" };
  return (
    <Surface>
      <h2 className="text-[length:var(--lt-fs-price)]">Доставка</h2>
      <p className="mt-1 text-lg font-semibold">{label[del.status]}</p>
      {del.etaAt && !["delivered", "failed"].includes(del.status) ? <p className="text-muted">Ожидаем к {fmtTime(del.etaAt)}</p> : null}
      {del.courier?.name && ["assigned", "picked_up", "en_route"].includes(del.status) ? (
        <p className="mt-2">Курьер: <strong>{del.courier.name}</strong>{del.courier.phone ? <> · <a className="font-semibold underline" href={`tel:${del.courier.phone}`}>Позвонить</a></> : null}</p>
      ) : null}
      {del.payOnDelivery ? <p className="mt-2 text-sm text-muted">Оплата картой курьеру при получении.</p> : null}
      {del.status === "failed" && del.failReason === "no_courier" ? (
        <Notice tone="info" className="mt-3">Курьер не найден — заберите заказ в ресторане. Он будет ждать вас.</Notice>
      ) : null}
      {moving && loc.data ? (
        <div className="mt-3">
          <MapPicker label="Положение курьера" value={loc.data.location} />
          <p className="mt-1 text-xs text-muted">Обновлено {agoSeconds(loc.data.updatedAt)} с назад</p>
        </div>
      ) : null}
    </Surface>
  );
}
