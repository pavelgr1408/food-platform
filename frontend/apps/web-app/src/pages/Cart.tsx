import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { isApiError, type ApiError, type Cart as CartT, type PaymentMethodType, type UnavailableItem } from "@fp/api";
import { formatMoney, isE164, normalizePhone } from "@fp/shared";
import { useApi } from "../app/api-context";
import { useAuth } from "../app/auth";
import { Button, Empty, Field, Notice, Price, Spinner, Surface } from "../components/ui";
import { Modal } from "../components/Modal";
import { MapPicker, type LatLon } from "../components/LazyMap";
import { toInputs, useCart } from "../lib/cart";
import { errorText } from "../lib/errors";
import { localContact, localOrders } from "../lib/local";

const PAY_LABEL: Record<PaymentMethodType, string> = { online_card: "Картой онлайн", pos: "На кассе", courier_card: "Картой курьеру" };
const PAY_BY: Record<"pickup" | "delivery", PaymentMethodType[]> = { pickup: ["online_card", "pos"], delivery: ["online_card", "courier_card"] };

/** Корзина, получение, контакты и оформление: UC-CRT-05…09, UC-FUL-01…04, UC-ORD-01…09. */
export function Cart() {
  const nav = useNavigate();
  const auth = useAuth();
  const c = useCart();
  const cart = c.cart;

  const [note, setNote] = useState<string | null>(null);

  const lines = useMutation({
    mutationFn: (next: ReturnType<typeof toInputs>) => c.setLines(c.cartId!, next),
    onError: (e) => { if (isApiError(e) && e.code === "ITEMS_UNAVAILABLE") setNote(`Убрали недоступных позиций: ${((e.extra.unavailableItems as unknown[]) ?? []).length}`); },
    onSuccess: () => setNote(null),
  });

  if (c.isLoading) return <Spinner label="Загружаем корзину…" />;
  if (!c.cartId || !cart) {
    return (
      <Empty title="Корзина пуста">
        <p>Выберите ресторан и добавьте блюда.</p>
        <Link to="/" className="mt-3 inline-block rounded-full bg-lemon px-6 py-3 font-semibold text-ink">К ресторанам</Link>
      </Empty>
    );
  }
  // UC-CRT-09: корзина уже оформлена — ведём к заказу
  if (cart.status === "ordered") {
    return (
      <Empty title="Эта корзина уже оформлена">
        {cart.orderId ? <Link to={`/orders/${cart.orderId}`} className="mt-1 inline-block rounded-full bg-lemon px-6 py-3 font-semibold text-ink">Открыть заказ</Link> : null}
        <div className="mt-3"><Button variant="outline" onClick={() => c.startNew()}>Начать новую корзину</Button></div>
      </Empty>
    );
  }
  if (cart.items.length === 0) {
    return (
      <Empty title="Корзина пуста">
        <Link to={`/r/${cart.restaurantId}`} className="mt-1 inline-block rounded-full bg-lemon px-6 py-3 font-semibold text-ink">Вернуться в меню</Link>
      </Empty>
    );
  }

  const change = (idx: number, delta: number) => {
    const next = toInputs(cart);
    const cur = next[idx]!;
    const q = cur.quantity + delta;
    if (q <= 0) next.splice(idx, 1); else next[idx] = { ...cur, quantity: Math.min(99, q) };
    lines.mutate(next);
  };
  const unavailable = cart.items.filter((i) => !i.available);

  return (
    <div className="lt-rise grid gap-5 lg:grid-cols-[1fr_380px]">
      <div className="grid content-start gap-5">
        <h1 className="text-[length:var(--lt-fs-h2)] md:text-[length:var(--lt-fs-h1)]">Корзина</h1>
        {note ? <Notice tone="info">{note}</Notice> : null}
        {lines.error && !(isApiError(lines.error) && lines.error.code === "ITEMS_UNAVAILABLE") ? <Notice>{errorText(lines.error)}</Notice> : null}

        <Surface>
          <ul className="divide-y divide-dashed divide-ink/30">
            {cart.items.map((it, idx) => (
              <li key={`${it.menuItemId}|${it.modifierIds.join(",")}`} className={`flex flex-wrap items-center justify-between gap-3 py-3 ${it.available ? "" : "opacity-60"}`}>
                <div className="min-w-0 flex-1">
                  <p className="font-semibold">{it.name}</p>
                  {it.modifierIds.length ? <p className="text-sm text-muted">Добавки: {it.modifierIds.length}</p> : null}
                  {!it.available ? <p className="text-sm font-semibold text-tomato">Нет в наличии</p> : null}
                  <p className="text-sm text-muted">{formatMoney(it.unitPrice.amount, it.unitPrice.currency)} × {it.quantity}</p>
                </div>
                <div className="flex items-center gap-2">
                  <Button variant="outline" aria-label={`Меньше: ${it.name}`} onClick={() => change(idx, -1)} disabled={lines.isPending} className="!px-0 min-w-[var(--lt-tap)]">−</Button>
                  <output className="w-7 text-center font-display font-semibold">{it.quantity}</output>
                  <Button variant="outline" aria-label={`Больше: ${it.name}`} onClick={() => change(idx, 1)} disabled={lines.isPending || it.quantity >= 99} className="!px-0 min-w-[var(--lt-tap)]">+</Button>
                </div>
                <Price money={it.lineTotal} />
              </li>
            ))}
          </ul>
          <div className="mt-3 flex flex-wrap gap-2">
            {unavailable.length > 0 ? (
              <Button variant="accent" disabled={lines.isPending} onClick={() => lines.mutate(toInputs({ ...cart, items: cart.items.filter((i) => i.available) }))}>Удалить недоступные</Button>
            ) : null}
            <Button variant="ghost" disabled={lines.isPending} onClick={() => lines.mutate([])}>Очистить</Button>
          </div>
        </Surface>

        <FulfillmentSection cart={cart} onSaved={(next) => c.refetch().then(() => next)} />
        <Checkout cart={cart} isAuthed={auth.status === "user"} defaultPhone={auth.principal?.phone} onUnavailable={(items) => lines.mutate(toInputs({ ...cart, items: cart.items.filter((i) => !items.some((u) => u.menuItemId === i.menuItemId)) }))}
          onDone={(order) => {
            localOrders.add({ id: order.id, number: order.number, restaurantId: order.restaurantId, createdAt: order.createdAt, totalAmount: order.total.amount, currency: order.total.currency });
            c.forget();
            nav(`/orders/${order.id}`);
          }} />
      </div>

      <aside className="lg:sticky lg:top-4 lg:self-start">
        <Surface>
          <h2 className="mb-3 text-[length:var(--lt-fs-price)]">Итого</h2>
          <dl className="grid gap-1 border-b border-dashed border-ink/30 pb-3">
            <Row k="Подытог" v={formatMoney(cart.subtotal.amount, cart.subtotal.currency)} />
            {cart.appliedPromotions.map((p) => (
              <Row key={p.promotionId ?? p.title} k={`Акция: ${p.title ?? ""}`} v={p.discount ? `−${formatMoney(p.discount.amount, p.discount.currency)}` : ""} />
            ))}
            {cart.discountTotal.amount > 0 ? <Row k="Скидка" v={`−${formatMoney(cart.discountTotal.amount, cart.discountTotal.currency)}`} /> : null}
          </dl>
          <p className="mt-3 flex items-center justify-between"><span className="font-semibold">К оплате</span><Price money={cart.total} big /></p>
          <p className="mt-2 text-xs text-muted">Цены и скидки пересчитывает сервер при каждом открытии корзины.</p>
        </Surface>
      </aside>
    </div>
  );
}

const Row = ({ k, v }: { k: string; v: string }) => <div className="flex justify-between gap-3"><dt>{k}</dt><dd className="font-medium">{v}</dd></div>;

/* ---------------- Получение: самовывоз / доставка ---------------- */
function FulfillmentSection({ cart, onSaved }: { cart: CartT; onSaved: (c: CartT) => void }) {
  const api = useApi();
  const cur = cart.fulfillment;
  // ничего не выбрано, пока сервер не знает способ получения: иначе повторный клик по «Самовывоз» не вызовет onChange
  const [type, setType] = useState<"pickup" | "delivery" | null>(cur?.type ?? null);
  const [raw, setRaw] = useState(cur?.address?.raw ?? "");
  const [pin, setPin] = useState<LatLon | null>(cur?.address?.location ?? null);
  // координаты вводятся строкой: иначе «56.» превращается в 56 и точку нельзя набрать
  const [latS, setLatS] = useState(pin ? String(pin.lat) : "");
  const [lonS, setLonS] = useState(pin ? String(pin.lon) : "");
  const setPinFromMap = (p: LatLon) => { setPin(p); setLatS(String(p.lat)); setLonS(String(p.lon)); };
  const typeCoord = (which: "lat" | "lon", v: string) => {
    const nl = which === "lat" ? v : latS, no = which === "lon" ? v : lonS;
    (which === "lat" ? setLatS : setLonS)(v);
    const a = Number(nl.replace(",", ".")), b = Number(no.replace(",", "."));
    if (nl.trim() && no.trim() && Number.isFinite(a) && Number.isFinite(b)) setPin({ lat: a, lon: b }); else setPin(null);
  };

  const save = useMutation({
    mutationFn: (body: Parameters<typeof api.carts.putFulfillment>[1]) => api.carts.putFulfillment(cart.id, body),
    onSuccess: (c) => onSaved(c),
  });
  const err = save.error as ApiError | null;
  const outOfZone = err?.code === "ADDRESS_OUT_OF_ZONE" || err?.code === "DELIVERY_NOT_AVAILABLE";

  const choosePickup = () => { setType("pickup"); save.mutate({ type: "pickup" }); };
  return (
    <Surface>
      <h2 className="mb-3 text-[length:var(--lt-fs-price)]">Получение</h2>
      <div role="radiogroup" aria-label="Способ получения" className="mb-3 flex flex-wrap gap-2">
        <Radio checked={type === "pickup"} onChange={choosePickup} label="Самовывоз" />
        <Radio checked={type === "delivery"} onChange={() => setType("delivery")} label="Доставка" />
      </div>
      {type === "delivery" ? (
        <div className="grid gap-3">
          <Field label="Адрес" value={raw} maxLength={300} onChange={(e) => setRaw(e.target.value)} placeholder="улица, дом, квартира, подъезд, этаж"
            hint="Подъезд, этаж, домофон и комментарий курьеру пока пишите в эту же строку." error={err?.code === "ADDRESS_REQUIRED" ? errorText(err) : null} />
          <MapPicker label="Карта доставки. Нажмите, чтобы поставить точку" value={pin} onChange={setPinFromMap} />
          <div className="grid grid-cols-2 gap-3">
            <Field label="Широта" inputMode="decimal" value={latS} onChange={(e) => typeCoord("lat", e.target.value)} />
            <Field label="Долгота" inputMode="decimal" value={lonS} onChange={(e) => typeCoord("lon", e.target.value)} />
          </div>
          {err?.code === "ADDRESS_LOCATION_REQUIRED" ? <Notice>{errorText(err)}</Notice> : null}
          {outOfZone ? (
            <Notice>
              {errorText(err)}{" "}
              {err?.extra.hint === "pickup" || err?.code === "DELIVERY_NOT_AVAILABLE" ? <button className="font-bold underline" onClick={choosePickup}>Заберу сам</button> : null}
            </Notice>
          ) : null}
          {err && !["ADDRESS_REQUIRED", "ADDRESS_LOCATION_REQUIRED", "ADDRESS_OUT_OF_ZONE", "DELIVERY_NOT_AVAILABLE"].includes(err.code) ? <Notice>{errorText(err)}</Notice> : null}
          <Button variant="accent" disabled={save.isPending} onClick={() => save.mutate({ type: "delivery", address: { raw: raw.trim() || undefined, location: pin ?? undefined } })}>
            {save.isPending ? "Проверяем…" : "Доставить сюда"}
          </Button>
        </div>
      ) : err ? <Notice>{errorText(err)}</Notice> : null}
      {cur ? <p className="mt-3 text-sm font-medium text-muted">Выбрано: {cur.type === "pickup" ? "самовывоз" : `доставка — ${cur.address?.raw ?? "точка на карте"}`}</p> : null}
    </Surface>
  );
}

function Radio({ checked, onChange, label }: { checked: boolean; onChange: () => void; label: string }) {
  return (
    <label className={`inline-flex min-h-[var(--lt-tap)] cursor-pointer items-center gap-2 rounded-full border-2 border-ink px-5 font-semibold ${checked ? "bg-ink text-white" : ""}`}>
      <input type="radio" className="sr-only" checked={checked} onChange={onChange} />
      {label}
    </label>
  );
}

/* ---------------- Контакты, оплата, оформление ---------------- */
function Checkout({ cart, isAuthed, defaultPhone, onDone, onUnavailable }: {
  cart: CartT; isAuthed: boolean; defaultPhone?: string; onDone: (o: import("@fp/api").Order) => void; onUnavailable: (i: UnavailableItem[]) => void;
}) {
  const api = useApi();
  const qc = useQueryClient();
  const nav = useNavigate();
  const saved = useMemo(() => localContact.get(), []);
  const [phone, setPhone] = useState(defaultPhone ?? saved.phone ?? "");
  const [name, setName] = useState(saved.name ?? "");
  const [email, setEmail] = useState(saved.email ?? "");
  useEffect(() => { if (defaultPhone && !phone) setPhone(defaultPhone); }, [defaultPhone]); // eslint-disable-line react-hooks/exhaustive-deps

  const fType = cart.fulfillment?.type;
  const allowed = fType ? PAY_BY[fType] : [];
  const [method, setMethod] = useState<PaymentMethodType | null>(null);
  // UC-ORD-01: при смене способа получения выбор сбрасывается
  useEffect(() => { setMethod((m) => (m && allowed.includes(m) ? m : null)); }, [fType]); // eslint-disable-line react-hooks/exhaustive-deps

  const normPhone = normalizePhone(phone);
  const phoneErr = phone && !isE164(normPhone) ? "Формат: +79991234567" : !phone && !isAuthed ? null : null;
  const emailErr = email && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email) ? "Проверьте адрес почты" : null;

  /** Idempotency-Key живёт для одного и того же запроса; после любого 4xx или смены тела нужен новый (UC-ORD-08). */
  const keyRef = useRef<{ key: string; sig: string } | null>(null);
  const body = {
    cartId: cart.id, channel: "online" as const, paymentMethod: method!,
    contact: { phone: normPhone || undefined, name: name.trim() || undefined, email: email.trim() || undefined },
    expectedTotal: cart.total,
  };
  const sig = JSON.stringify(body);
  const [dialog, setDialog] = useState<null | { kind: "price" | "total" | "items"; was?: number; items?: UnavailableItem[] }>(null);

  const place = useMutation({
    mutationFn: async () => {
      if (!keyRef.current || keyRef.current.sig !== sig) keyRef.current = { key: api.newIdempotencyKey(), sig };
      return api.orders.create(body, keyRef.current.key);
    },
    onSuccess: ({ order }) => { localContact.set({ phone: normPhone, name, email }); onDone(order); },
    onError: async (e) => {
      if (!isApiError(e)) return;
      if (e.status >= 400 && e.status < 500 && e.code !== "IDEMPOTENCY_IN_PROGRESS") keyRef.current = null; // 4xx сохраняются под ключом
      if (e.code === "PRICE_CHANGED") setDialog({ kind: "price" });
      else if (e.code === "TOTAL_CHANGED") setDialog({ kind: "total", was: cart.total.amount });
      else if (e.code === "ITEMS_UNAVAILABLE") setDialog({ kind: "items", items: (e.extra.unavailableItems as UnavailableItem[]) ?? [] });
      if (["PRICE_CHANGED", "TOTAL_CHANGED", "CART_CHANGED"].includes(e.code)) await qc.invalidateQueries({ queryKey: ["cart", cart.id] });
    },
  });
  const error = place.error as ApiError | null;

  const ready = !!fType && !!method && (isAuthed || isE164(normPhone)) && !phoneErr && !emailErr && cart.items.every((i) => i.available);
  const missing = !fType ? "Выберите способ получения" : !method ? "Выберите способ оплаты" : !isAuthed && !isE164(normPhone) ? "Укажите телефон" : null;

  // CART_CLOSED: корзина уже оформлена — открываем заказ
  useEffect(() => {
    if (error?.code === "CART_CLOSED") api.carts.get(cart.id).then((c) => { if (c.orderId) nav(`/orders/${c.orderId}`); }).catch(() => undefined);
  }, [error?.code]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <>
      <Surface>
        <h2 className="mb-3 text-[length:var(--lt-fs-price)]">Контакты</h2>
        <div className="grid gap-3 md:grid-cols-2">
          <Field label={isAuthed ? "Телефон" : "Телефон (обязательно)"} type="tel" inputMode="tel" autoComplete="tel" value={phone} onChange={(e) => setPhone(e.target.value)}
            onBlur={() => setPhone(normalizePhone(phone))} error={phoneErr ?? (error?.code === "CONTACT_REQUIRED" ? errorText(error) : null)} placeholder="+7 999 123-45-67" />
          <Field label="Имя" autoComplete="name" maxLength={100} value={name} onChange={(e) => setName(e.target.value)} hint="Необязательно" />
          <Field label="Почта" type="email" autoComplete="email" maxLength={254} value={email} onChange={(e) => setEmail(e.target.value)} error={emailErr} hint="Необязательно. Письма пока не отправляем." className="md:col-span-2" />
        </div>
      </Surface>

      <Surface>
        <h2 className="mb-3 text-[length:var(--lt-fs-price)]">Оплата</h2>
        {!fType ? <p className="text-muted">Сначала выберите способ получения.</p> : (
          <div role="radiogroup" aria-label="Способ оплаты" className="flex flex-wrap gap-2">
            {allowed.map((m) => <Radio key={m} checked={method === m} onChange={() => setMethod(m)} label={PAY_LABEL[m]} />)}
          </div>
        )}
        <p className="mt-2 text-xs text-muted">Способ оплаты нельзя изменить после оформления. Наличных курьеру нет.</p>
      </Surface>

      {error && !dialog ? <Notice>{errorText(error)}{error.code === "IDEMPOTENCY_IN_PROGRESS" ? " Нажмите ещё раз через пару секунд." : ""}</Notice> : null}
      {missing ? <p className="text-sm text-muted" role="status">{missing}</p> : null}
      <Button variant="accent" cta className="w-full" disabled={!ready || place.isPending} onClick={() => place.mutate()}>
        {place.isPending ? "Оформляем…" : `Оформить заказ · ${formatMoney(cart.total.amount, cart.total.currency)}`}
      </Button>

      {dialog?.kind === "price" || dialog?.kind === "total" ? (
        <Modal title={dialog.kind === "price" ? "Цены обновились" : "Итог изменился"} onClose={() => setDialog(null)}>
          <p className="text-muted">
            {dialog.kind === "total" && dialog.was !== undefined ? `Было ${formatMoney(dialog.was, cart.total.currency)}, стало ${formatMoney(cart.total.amount, cart.total.currency)}. ` : ""}
            Новая сумма заказа — <strong>{formatMoney(cart.total.amount, cart.total.currency)}</strong>. Заказ ещё не создан.
          </p>
          <div className="mt-5 flex flex-wrap gap-2">
            <Button variant="accent" onClick={() => { setDialog(null); place.mutate(); }}>Подтвердить</Button>
            <Button variant="outline" onClick={() => setDialog(null)}>Вернуться</Button>
          </div>
        </Modal>
      ) : null}
      {dialog?.kind === "items" ? (
        <Modal title="Некоторые блюда закончились" onClose={() => setDialog(null)}>
          <ul className="mb-4 list-disc pl-5 text-muted">
            {(dialog.items ?? []).map((u) => <li key={u.menuItemId}>{cart.items.find((i) => i.menuItemId === u.menuItemId)?.name ?? u.menuItemId}: {u.reason ?? u.reasonCode}</li>)}
          </ul>
          <Button variant="accent" onClick={() => { onUnavailable(dialog.items ?? []); setDialog(null); }}>Убрать и продолжить</Button>
        </Modal>
      ) : null}
    </>
  );
}
