import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { isApiError, type ApiError, type CancelReason, type PosTender, type StaffOrder } from "@fp/api";
import { formatMoney } from "@fp/shared";
import { Badge, Button, Empty, Modal, Notice, Price, Skeleton, Spinner, StatusPill, Surface, cx, errorText, fmtDateTime, fmtTime, reasonText, stepsFor, STATUS_LABEL, useApi } from "@fp/ui";
import { useWorkplace } from "../app/workplace";
import { KV, ToneBadge } from "../components/bits";
import { CHANNEL_LABEL, PAY_LABEL, TENDER_LABEL, canTakePosPayment, cancelVerdict, isSettled, issueVerdict, payBadge } from "../lib/order";
import { localPayments } from "../lib/local";

const CANCEL_REASONS: { value: CancelReason; label: string }[] = [
  { value: "customer_request", label: "По просьбе клиента" },
  { value: "out_of_stock", label: "Блюда закончились" },
  { value: "restaurant_closed", label: "Ресторан закрыт" },
  { value: "other", label: "Другая причина" },
];

/** Карточка заказа для кассы: UC-STF-01 (карточка с контактами), 06 (оплата), 07 (выдача), 08 (отмена), 09 (чек), 10 (производство). */
export function OrderPage() {
  const api = useApi();
  const qc = useQueryClient();
  const wp = useWorkplace();
  const { number = "" } = useParams();
  const n = decodeURIComponent(number).toUpperCase();

  const q = useQuery({
    queryKey: ["staff-order", n],
    queryFn: ({ signal }) => api.orders.search(n, signal),
    // опрашиваем, пока заказ меняется или деньги в пути; завершённый и сверенный — стоп
    refetchInterval: (query) => (query.state.data && isSettled(query.state.data) ? false : 3_000),
    retry: (c, e) => !(isApiError(e) && e.status >= 400 && e.status < 500) && c < 2,
  });
  const o = q.data;

  // ETA — справочно: сбой не должен ломать карточку (UC-STF-10)
  const eta = useQuery({
    queryKey: ["eta", o?.id],
    queryFn: () => api.orders.eta(o!.id),
    enabled: !!o && !isSettled(o) && !["closed", "cancelled", "handed_over", "delivered", "not_received"].includes(o.status),
    refetchInterval: 15_000,
    retry: false,
  });

  const [dialog, setDialog] = useState<null | "pay" | "issue" | "cancel">(null);
  const [flash, setFlash] = useState<{ text: string; untilPaid?: boolean } | null>(null);
  const apply = (next: StaffOrder | import("@fp/api").Order) => {
    // issue/cancel возвращают Order без контактов: не затираем StaffOrder, а сливаем
    qc.setQueryData<StaffOrder>(["staff-order", n], (prev) => (prev ? { ...prev, ...next } : (next as StaffOrder)));
    void qc.invalidateQueries({ queryKey: ["queue"] });
    void qc.invalidateQueries({ queryKey: ["queue-count"] });
  };

  if (q.isLoading) return <div role="status" aria-label="Загружаем заказ" className="grid gap-4"><Skeleton className="h-24 rounded-card" /><Skeleton className="h-64 rounded-card" /></div>;
  if (q.isError || !o) {
    const nf = isApiError(q.error) && q.error.code === "ORDER_NOT_FOUND";
    return (
      <Empty page title={nf ? "Заказ не найден" : "Не удалось загрузить заказ"}>
        <p>{nf ? "Проверьте номер: заказ мог быть оформлен в другом ресторане." : errorText(q.error)}</p>
        <Link to="/" className="mt-3 inline-block rounded-full bg-lemon px-6 py-3 font-semibold text-ink">К очереди</Link>
      </Empty>
    );
  }

  const tz = wp.restaurants.find((r) => r.id === o.restaurantId)?.timezone ?? wp.current?.timezone;
  const pay = payBadge(o);
  const issue = issueVerdict(o);
  const cancel = cancelVerdict(o);
  const takePay = canTakePosPayment(o);
  const paymentId = localPayments.get(o.id);
  const waitingPayment = o.paymentStatus === "unknown"; // pending на кассе — это «ещё не оплачено», а не ожидание
  const steps = stepsFor(o.fulfillment.type, o.paymentMethod === "online_card");
  const cur = steps.indexOf(o.status);
  const slot = o.pickupSlot;

  return (
    <div className="lt-rise grid gap-5 lg:grid-cols-[1fr_360px]">
      <div className="grid content-start gap-5">
        <header>
          <p className="mb-1 text-sm"><Link to="/" className="font-semibold underline">← Очередь</Link></p>
          <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
            <h1 className="lt-num !font-bold text-[length:var(--lt-fs-h2)] md:text-[length:var(--lt-fs-h1)]" aria-label={`Заказ ${o.number}`}>{o.number}</h1>
            <StatusPill status={o.status} />
            <Badge tone="neutral">{CHANNEL_LABEL[o.channel ?? "online"]}</Badge>
          </div>
          <p className="mt-2 text-muted">Оформлен {fmtDateTime(o.createdAt, tz)}{o.createdBy ? " сотрудником" : ""}{reasonText(o.statusReason) ? ` · ${reasonText(o.statusReason)}` : ""}</p>
        </header>

        {flash && !(flash.untilPaid && o.paymentStatus === "paid") ? <Notice tone="success">{flash.text}</Notice> : null}
        {o.refundStatus ? <RefundNotice status={o.refundStatus} /> : null}
        {o.status === "cancelled" && !o.refundStatus && o.paymentStatus === "paid" ? <Notice tone="info">Заказ оплачен — возврат запускается.</Notice> : null}
        {waitingPayment && o.paymentMethod === "pos" ? (
          <Notice tone="info"><span className="inline-flex items-center gap-2"><Spinner label="Проверяем платёж — не принимайте оплату повторно." /></span></Notice>
        ) : null}

        {o.status !== "cancelled" && cur >= 0 ? (
          <Surface>
            <ol aria-label="Ход заказа" className="flex flex-wrap gap-x-1 gap-y-2">
              {steps.map((s, i) => (
                <li key={s} aria-current={i === cur ? "step" : undefined}
                  className={cx("flex items-center gap-1 rounded-full px-3 py-1 text-sm font-semibold", i < cur ? "bg-mint text-ink" : i === cur ? "bg-ink text-white" : "bg-ink/5 text-faint")}>
                  <span aria-hidden>{i < cur ? "✓" : i + 1}</span>{STATUS_LABEL[s]}
                </li>
              ))}
            </ol>
            {eta.data && eta.data.readyAt && o.status !== "ready" ? (
              <p className="mt-3 text-sm text-muted">Будет готов к <strong>{fmtTime(eta.data.readyAt, tz)}</strong>{eta.data.estimateMinutes !== undefined ? ` · ${eta.data.approximate ? "около " : ""}${eta.data.estimateMinutes} мин` : ""}</p>
            ) : null}
            {o.delivery ? <DeliveryLine d={o.delivery} tz={tz} /> : null}
          </Surface>
        ) : null}

        <Surface>
          <h2 className="mb-3 text-[length:var(--lt-fs-price)]">Состав</h2>
          <ul className="divide-y divide-dashed divide-ink/20">
            {o.items.map((i) => (
              <li key={`${i.menuItemId}|${i.modifierIds.join(",")}`} className="flex items-start justify-between gap-3 py-2 first:pt-0 last:pb-0">
                <div>
                  <p className="font-semibold"><span className="lt-num">{i.quantity}×</span> {i.name}</p>
                  {i.modifiers?.length ? <p className="text-sm text-muted">{i.modifiers.map((m) => m.name).join(", ")}</p> : i.modifierIds.length ? <p className="text-sm text-muted">Добавок: {i.modifierIds.length}</p> : null}
                </div>
                <span className="whitespace-nowrap font-medium">{formatMoney(i.lineTotal.amount, i.lineTotal.currency)}</span>
              </li>
            ))}
          </ul>
          <dl className="mt-4 border-t border-dashed border-ink/30 pt-3">
            <KV k="Подытог">{formatMoney(o.subtotal.amount, o.subtotal.currency)}</KV>
            {(o.appliedPromotions ?? []).map((p) => <KV key={p.promotionId ?? p.title} k={`Акция: ${p.title ?? ""}`}>{p.discount ? `−${formatMoney(p.discount.amount, p.discount.currency)}` : ""}</KV>)}
            <div className="mt-2 flex items-center justify-between"><dt className="font-semibold">Итого</dt><dd><Price money={o.total} big /></dd></div>
          </dl>
        </Surface>
      </div>

      <aside className="grid content-start gap-5 lg:sticky lg:top-24 lg:self-start">
        <Surface>
          <h2 className="mb-2 text-[length:var(--lt-fs-price)]">Действия</h2>
          <div className="grid gap-2">
            {takePay ? <Button variant="accent" cta disabled={waitingPayment} onClick={() => setDialog("pay")}>{waitingPayment ? "Оплата обрабатывается…" : `Принять оплату · ${formatMoney(o.total.amount, o.total.currency)}`}</Button> : null}
            <Button variant={takePay ? "primary" : "accent"} cta disabled={!issue.ok} onClick={() => setDialog("issue")}>Выдать заказ</Button>
            {!issue.ok ? <p className="text-center text-sm text-muted" role="status">{issue.reason}</p> : null}
            <Button variant="outline" disabled={!cancel.ok} onClick={() => setDialog("cancel")}>Отменить заказ</Button>
            {!cancel.ok ? <p className="text-center text-sm text-muted">{cancel.reason}</p> : null}
          </div>
          {o.paymentMethod === "online_card" && o.paymentStatus !== "paid" && o.status === "pending_payment" ? (
            <p className="mt-3 text-sm text-muted">Клиент оплачивает онлайн: кассой этот заказ не оплатить. Если не оплатит за 15 минут, заказ отменится сам.</p>
          ) : null}
        </Surface>

        <Surface>
          <h2 className="mb-2 text-[length:var(--lt-fs-price)]">Клиент</h2>
          {o.contact?.phone || o.contact?.name || o.contact?.email ? (
            <dl>
              {o.contact.name ? <KV k="Имя">{o.contact.name}</KV> : null}
              {o.contact.phone ? <KV k="Телефон"><a className="font-semibold underline" href={`tel:${o.contact.phone}`}>{o.contact.phone}</a></KV> : null}
              {o.contact.email ? <KV k="Почта">{o.contact.email}</KV> : null}
            </dl>
          ) : <p className="text-muted">Контакты не указаны.</p>}
        </Surface>

        <Surface>
          <h2 className="mb-2 text-[length:var(--lt-fs-price)]">Получение и оплата</h2>
          <dl>
            <KV k="Получение">{o.fulfillment.type === "pickup" ? "Самовывоз" : "Доставка"}</KV>
            {o.fulfillment.type === "delivery" ? <KV k="Адрес">{o.fulfillment.address?.raw ?? "точка на карте"}</KV> : null}
            {slot ? <KV k="Время самовывоза">{fmtTime(slot.from, tz)}–{fmtTime(slot.to, tz)}</KV> : null}
            <KV k="Способ оплаты">{PAY_LABEL[o.paymentMethod]}</KV>
            <KV k="Оплата"><ToneBadge tone={pay.tone}>{pay.text}</ToneBadge></KV>
          </dl>
          {paymentId ? <PaymentDetails orderId={o.id} paymentId={paymentId} tz={tz} /> : o.paymentStatus === "paid" ? <p className="mt-2 text-xs text-muted">Чек и данные платежа видны для оплат, принятых на этом устройстве.</p> : null}
        </Surface>
      </aside>

      {dialog === "pay" ? <PayDialog o={o} onClose={() => setDialog(null)} onDone={(msg, untilPaid) => { setDialog(null); setFlash({ text: msg, untilPaid }); void qc.invalidateQueries({ queryKey: ["staff-order", n] }); }} /> : null}
      {dialog === "issue" ? <IssueDialog o={o} onClose={() => setDialog(null)} onPay={() => setDialog("pay")} onDone={(next) => { setDialog(null); setFlash({ text: `Заказ ${o.number} выдан и закрыт.` }); apply(next); }} /> : null}
      {dialog === "cancel" ? <CancelDialog o={o} onClose={() => setDialog(null)} onDone={(next) => { setDialog(null); setFlash({ text: `Заказ ${o.number} отменён.` }); apply(next); }} /> : null}
    </div>
  );
}

function RefundNotice({ status }: { status: NonNullable<StaffOrder["refundStatus"]> }) {
  if (status === "pending") return <Notice tone="info">Возврат денег клиенту выполняется.</Notice>;
  if (status === "refunded") return <Notice tone="success">Деньги клиенту возвращены.</Notice>;
  return <Notice>Возврат не прошёл. Обработайте вручную: обратитесь в поддержку и назовите номер заказа.</Notice>;
}

function DeliveryLine({ d, tz }: { d: NonNullable<StaffOrder["delivery"]>; tz?: string }) {
  const L: Record<string, string> = { searching: "ищем курьера", assigned: "курьер назначен", picked_up: "курьер забрал заказ", en_route: "курьер в пути", delivered: "доставлено", failed: "доставка не состоялась" };
  const noCourier = d.status === "failed" && d.failReason === "no_courier";
  return (
    <p className="mt-2 text-sm text-muted">
      Доставка: {L[d.status] ?? d.status}{d.etaAt && d.status !== "delivered" && d.status !== "failed" ? ` · к ${fmtTime(d.etaAt, tz)}` : ""}
      {noCourier ? " — курьера нет. Предложите клиенту забрать заказ в ресторане: выдать можно после оплаты." : ""}
    </p>
  );
}

/** UC-STF-09: чек и ход возврата — только для платежа, чей paymentId известен этому устройству. */
function PaymentDetails({ orderId, paymentId, tz }: { orderId: string; paymentId: string; tz?: string }) {
  const api = useApi();
  const p = useQuery({ queryKey: ["payment", orderId, paymentId], queryFn: () => api.payments.get(orderId, paymentId), refetchInterval: (q) => (q.state.data && (q.state.data.status === "pending" || q.state.data.status === "unknown") ? 3_000 : false), retry: false });
  if (p.isLoading) return <Spinner label="Загружаем чек…" />;
  if (!p.data) return null;
  const d = p.data;
  const fiscal = Object.entries(d.fiscalData ?? {});
  return (
    <div className="mt-3 rounded-tile bg-ink/5 p-3 text-sm">
      <p className="font-semibold">Платёж{d.tender ? ` · ${TENDER_LABEL[d.tender]}` : ""} · попытка {d.attempt}</p>
      {d.paidAt ? <p className="text-muted">Принят {fmtDateTime(d.paidAt, tz)}</p> : null}
      {d.declineReason ? <p className="text-muted">Причина отказа: {d.declineReason}</p> : null}
      {fiscal.length ? (
        <dl className="mt-2">
          {fiscal.map(([k, v]) => <KV key={k} k={k} className="py-0.5"><span className="lt-num">{String(v)}</span></KV>)}
        </dl>
      ) : null}
      {d.refund ? <p className="mt-2 font-semibold">Возврат: {{ pending: "выполняется", refunded: "выполнен", failed: "не прошёл — нужна ручная обработка" }[d.refund.status ?? "pending"]}</p> : null}
    </div>
  );
}

/* ----------------------------- Оплата на кассе (UC-STF-06) ----------------------------- */
function PayDialog({ o, onClose, onDone }: { o: StaffOrder; onClose: () => void; onDone: (msg: string, untilPaid?: boolean) => void }) {
  const api = useApi();
  const [tender, setTender] = useState<PosTender>("card");
  // один ключ на одну попытку; после любого 4xx (кроме «в процессе») нужен новый (4xx сохраняются под ключом)
  const key = useRef<{ key: string; tender: PosTender } | null>(null);
  const busy = useRef(false);

  const pay = useMutation({
    onSettled: () => { busy.current = false; },
    mutationFn: async () => {
      if (!key.current || key.current.tender !== tender) key.current = { key: api.newIdempotencyKey(), tender };
      return api.payments.pos(o.id, tender, key.current.key);
    },
    onSuccess: ({ payment, status }) => {
      localPayments.set(o.id, payment.id);
      onDone(status === 202 ? "Касса пока не подтвердила платёж. Не принимайте оплату повторно — исход появится здесь." : "Оплата отправлена на терминал. Ждём подтверждения кассы.", true);
    },
    onError: (e) => {
      if (!isApiError(e)) return;
      if (e.status >= 400 && e.status < 500 && e.code !== "IDEMPOTENCY_IN_PROGRESS") key.current = null;
      const pid = e.extra.paymentId;
      if (typeof pid === "string") localPayments.set(o.id, pid); // PAYMENT_IN_PROGRESS / ALREADY_PAID / UNKNOWN: платёж уже есть
    },
  });
  const err = pay.error as ApiError | null;
  // «уже оплачен / в процессе» — не ошибка кассира: закрываем окно и показываем актуальное состояние
  useEffect(() => {
    if (err && ["ALREADY_PAID", "PAYMENT_IN_PROGRESS", "PAYMENT_STATUS_UNKNOWN"].includes(err.code)) {
      onDone(errorText(err));
    }
  }, [err]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <Modal title="Принять оплату" onClose={onClose}
      footer={<div className="flex flex-wrap gap-2">
        <Button variant="accent" cta className="flex-1" disabled={pay.isPending} onClick={() => { if (busy.current) return; busy.current = true; pay.mutate(); }}>
          {pay.isPending ? "Отправляем на терминал…" : `Принять ${formatMoney(o.total.amount, o.total.currency)}`}
        </Button>
        <Button variant="outline" onClick={onClose}>Отмена</Button>
      </div>}>
      <p className="mb-3 text-muted">Заказ <span className="lt-num font-semibold text-ink">{o.number}</span>. Сумма к оплате:</p>
      <p className="mb-4"><Price money={o.total} big /></p>
      <div role="radiogroup" aria-label="Способ оплаты на кассе" className="grid gap-2 sm:grid-cols-2">
        {(["card", "cash"] as const).map((t) => (
          <label key={t} className={cx("flex min-h-[var(--lt-tap)] cursor-pointer items-center gap-3 rounded-input border-2 px-4 py-2 has-[:focus-visible]:outline has-[:focus-visible]:outline-[3px] has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-ink", tender === t ? "border-ink bg-lemon/40" : "border-ink/30")}>
            <input type="radio" className="sr-only" name="tender" value={t} checked={tender === t} onChange={() => setTender(t)} />
            <span aria-hidden className={cx("grid size-6 shrink-0 place-items-center rounded-full border-2 border-ink", tender === t ? "bg-ink" : "bg-white")}>{tender === t ? <span className="size-2 rounded-full bg-lemon" /> : null}</span>
            <span><span className="block font-semibold">{TENDER_LABEL[t]}</span><span className="block text-sm text-muted">{t === "card" ? "Терминал считает карту" : "Проводится через терминал с чеком"}</span></span>
          </label>
        ))}
      </div>
      {err && !["ALREADY_PAID", "PAYMENT_IN_PROGRESS", "PAYMENT_STATUS_UNKNOWN"].includes(err.code) ? (
        <Notice className="mt-4">
          {errorText(err, undefined, { PAYMENT_DECLINED: "Касса отказала в операции. Попробуйте другой способ или отмените заказ." })}
          {err.code === "PAYMENT_DECLINED" ? <> Заказ остаётся неоплаченным.</> : null}
        </Notice>
      ) : null}
    </Modal>
  );
}

/* ----------------------------- Выдача (UC-STF-07) ----------------------------- */
function IssueDialog({ o, onClose, onDone, onPay }: { o: StaffOrder; onClose: () => void; onDone: (o: import("@fp/api").Order) => void; onPay: () => void }) {
  const api = useApi();
  const busy = useRef(false);
  const run = useMutation({ onSettled: () => { busy.current = false; }, mutationFn: () => api.orders.issue(o.id), onSuccess: onDone });
  const err = run.error as ApiError | null;
  return (
    <Modal title="Выдать заказ?" onClose={onClose}
      footer={<div className="flex flex-wrap gap-2">
        <Button variant="accent" cta className="flex-1" disabled={run.isPending} onClick={() => { if (busy.current) return; busy.current = true; run.mutate(); }}>{run.isPending ? "Выдаём…" : "Выдать и закрыть"}</Button>
        <Button variant="outline" onClick={onClose}>Не сейчас</Button>
      </div>}>
      <p className="text-muted">Заказ <span className="lt-num font-semibold text-ink">{o.number}</span> будет отмечен как выданный и закрыт. Сверьте имя или телефон клиента{o.contact?.phone ? `: ${o.contact.phone}` : ""}.</p>
      {err ? (
        <Notice className="mt-4">
          {errorText(err)}
          {err.code === "ORDER_NOT_PAID" ? <> <button className="font-bold underline" onClick={onPay}>Принять оплату</button></> : null}
        </Notice>
      ) : null}
    </Modal>
  );
}

/* ----------------------------- Отмена (UC-STF-08) ----------------------------- */
function CancelDialog({ o, onClose, onDone }: { o: StaffOrder; onClose: () => void; onDone: (o: import("@fp/api").Order) => void }) {
  const api = useApi();
  const [reason, setReason] = useState<CancelReason>("customer_request");
  const busy = useRef(false);
  const run = useMutation({ onSettled: () => { busy.current = false; }, mutationFn: () => api.orders.cancel(o.id, reason), onSuccess: onDone });
  const err = run.error as ApiError | null;
  return (
    <Modal title="Отменить заказ?" onClose={onClose}
      footer={<div className="flex flex-wrap gap-2">
        <Button variant="primary" cta className="flex-1" disabled={run.isPending} onClick={() => { if (busy.current) return; busy.current = true; run.mutate(); }}>{run.isPending ? "Отменяем…" : "Отменить заказ"}</Button>
        <Button variant="outline" onClick={onClose}>Оставить</Button>
      </div>}>
      <p className="mb-3 text-muted">Заказ <span className="lt-num font-semibold text-ink">{o.number}</span> будет отменён. Это нельзя вернуть.</p>
      {o.paymentStatus === "paid" ? <Notice tone="info" className="mb-3">Заказ оплачен: деньги вернутся клиенту автоматически, ход возврата виден на карточке заказа.</Notice> : null}
      <label className="block">
        <span className="mb-1 block text-sm font-semibold">Причина</span>
        <select value={reason} onChange={(e) => setReason(e.target.value as CancelReason)} className="min-h-[var(--lt-tap)] w-full rounded-input border-2 border-ink bg-white px-3 text-base">
          {CANCEL_REASONS.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
        </select>
      </label>
      {err ? <Notice className="mt-4">{errorText(err, undefined, { ORDER_STATE_CONFLICT: "Заказ уже готовится — отменить нельзя. Обновите карточку." })}</Notice> : null}
    </Modal>
  );
}
