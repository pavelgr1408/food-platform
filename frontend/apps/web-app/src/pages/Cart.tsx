import { useEffect, useId, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { isApiError, type ApiError, type Cart as CartT, type PaymentMethodType, type UnavailableItem } from "@fp/api";
import { formatMoney, isE164, normalizePhone } from "@fp/shared";
import { useApi } from "../app/api-context";
import { useAuth } from "../app/auth";
import { Button, Empty, Field, Notice, Price, QtyStepper, Skeleton, Surface } from "../components/ui";
import { Thumb } from "../components/art";
import { Modal } from "../components/Modal";
import { MapPicker, type LatLon } from "../components/LazyMap";
import { lineKey, toInputs, useCart } from "../lib/cart";
import { errorText } from "../lib/errors";
import { localContact, localOrders } from "../lib/local";

const PAY_LABEL: Record<PaymentMethodType, string> = { online_card: "Картой онлайн", pos: "На кассе", courier_card: "Картой курьеру" };
const PAY_DESC: Record<PaymentMethodType, string> = { online_card: "На странице банка", pos: "Оплатите при получении", courier_card: "Курьер возьмёт терминал" };
const PAY_BY: Record<"pickup" | "delivery", PaymentMethodType[]> = { pickup: ["online_card", "pos"], delivery: ["online_card", "courier_card"] };

/** Корзина, получение, контакты и оформление: UC-CRT-05…09, UC-FUL-01…04, UC-ORD-01…09. */
export function Cart() {
  const api = useApi();
  const nav = useNavigate();
  const auth = useAuth();
  const c = useCart();
  const cart = c.cart;

  const [note, setNote] = useState<string | null>(null);
  const [confirmClear, setConfirmClear] = useState(false);

  // названия добавок и категории для картинок: корзина хранит только modifierIds
  const menu = useQuery({ queryKey: ["menu", c.restaurantId], queryFn: () => api.restaurants.menu(c.restaurantId!), enabled: !!c.restaurantId, retry: false, staleTime: 5 * 60_000 });
  const rest = useQuery({ queryKey: ["restaurant", c.restaurantId], queryFn: () => api.restaurants.get(c.restaurantId!), enabled: !!c.restaurantId, retry: false, staleTime: 5 * 60_000 });
  const lookup = useMemo(() => {
    const mods = new Map<string, string>(), cats = new Map<string, string>();
    for (const cat of menu.data?.categories ?? []) for (const it of cat.items) { cats.set(it.id, cat.name); for (const m of it.modifiers) mods.set(m.id, m.name); }
    return { mods, cats };
  }, [menu.data]);

  const lines = useMutation({
    mutationFn: (fn: (cur: ReturnType<typeof toInputs>) => ReturnType<typeof toInputs>) => c.update(c.restaurantId!, fn),
    onError: (e) => { if (isApiError(e) && e.code === "ITEMS_UNAVAILABLE") setNote(`Убрали недоступных позиций: ${((e.extra.unavailableItems as unknown[]) ?? []).length}`); },
    onSuccess: () => setNote(null),
  });

  if (c.isLoading) return <div role="status" aria-label="Загружаем корзину" className="grid gap-4"><Skeleton className="h-40 rounded-card" /><Skeleton className="h-48 rounded-card" /></div>;
  if (!c.cartId || !cart) {
    return (
      <Empty page title="Корзина пуста">
        <p>Выберите ресторан и добавьте блюда.</p>
        <Link to="/" className="mt-3 inline-block rounded-full bg-lemon px-6 py-3 font-semibold text-ink">К ресторанам</Link>
      </Empty>
    );
  }
  // UC-CRT-09: корзина уже оформлена — ведём к заказу
  if (cart.status === "ordered") {
    return (
      <Empty page title="Эта корзина уже оформлена">
        {cart.orderId ? <Link to={`/orders/${cart.orderId}`} className="mt-1 inline-block rounded-full bg-lemon px-6 py-3 font-semibold text-ink">Открыть заказ</Link> : null}
        <div className="mt-3"><Button variant="outline" onClick={() => c.startNew()}>Начать новую корзину</Button></div>
      </Empty>
    );
  }
  if (cart.items.length === 0) {
    return (
      <Empty page title="Корзина пуста">
        <Link to={`/r/${cart.restaurantId}`} className="mt-1 inline-block rounded-full bg-lemon px-6 py-3 font-semibold text-ink">Вернуться в меню</Link>
      </Empty>
    );
  }

  /** Изменения идут по ключу позиции, а не по индексу: пока летит прошлый запрос, индексы уже устарели. */
  const change = (it: CartT["items"][number], delta: number) => lines.mutate((cur) => {
    const key = lineKey(it);
    return cur.flatMap((l) => {
      if (lineKey(l) !== key) return [l];
      const q = Math.min(99, l.quantity + delta);
      return q > 0 ? [{ ...l, quantity: q }] : [];
    });
  });
  const unavailable = cart.items.filter((i) => !i.available);

  return (
    <div className="lt-rise grid gap-5 lg:grid-cols-[1fr_380px]">
      <div className="grid content-start gap-5">
        <header>
          <h1 className="text-[length:var(--lt-fs-h2)] md:text-[length:var(--lt-fs-h1)]">Корзина</h1>
          {rest.data ? <p className="mt-1 text-muted">Заказ из <Link to={`/r/${rest.data.id}`} className="font-semibold underline">{rest.data.name}</Link></p> : null}
        </header>
        {note ? <Notice tone="info">{note}</Notice> : null}
        {lines.error && !(isApiError(lines.error) && lines.error.code === "ITEMS_UNAVAILABLE") ? <Notice>{errorText(lines.error)}</Notice> : null}

        <Surface>
          <ul className="divide-y divide-dashed divide-ink/20">
            {cart.items.map((it) => (
              <li key={lineKey(it)} className={`flex gap-3 py-3 first:pt-0 last:pb-0 ${it.available ? "" : "opacity-60"}`}>
                <Thumb name={it.name} category={lookup.cats.get(it.menuItemId)} seed={it.menuItemId} className="size-20 shrink-0 rounded-tile" />
                <div className="flex min-w-0 flex-1 flex-col gap-2">
                  <div className="flex items-start justify-between gap-2">
                    <div className="min-w-0">
                      <p className="font-semibold leading-snug">{it.name}</p>
                      {it.modifierIds.length ? (
                        <p className="text-sm text-muted">{it.modifierIds.map((id) => lookup.mods.get(id)).filter(Boolean).join(", ") || `Добавок: ${it.modifierIds.length}`}</p>
                      ) : null}
                      {!it.available ? <p className="text-sm font-semibold text-tomato">Нет в наличии</p> : null}
                    </div>
                    <Price money={it.lineTotal} />
                  </div>
                  <div className="flex items-center justify-between gap-2">
                    <QtyStepper size="sm" label={it.name} value={it.quantity} onDec={() => change(it, -1)} onInc={() => change(it, 1)} />
                    <button type="button" onClick={() => change(it, -it.quantity)} aria-label={`Убрать «${it.name}»`} className="min-h-11 rounded-full px-3 text-sm font-semibold text-muted underline hover:text-ink">Убрать</button>
                  </div>
                </div>
              </li>
            ))}
          </ul>
          <div className="mt-4 flex flex-wrap gap-2 border-t border-dashed border-ink/20 pt-3">
            {unavailable.length > 0 ? (
              <Button variant="accent" disabled={lines.isPending} onClick={() => lines.mutate((cur) => cur.filter((l) => !unavailable.some((u) => u.menuItemId === l.menuItemId)))}>Удалить недоступные</Button>
            ) : null}
            <Link to={`/r/${cart.restaurantId}`} className="inline-flex min-h-[var(--lt-tap)] items-center rounded-full border-2 border-ink px-5 font-semibold hover:bg-ink/5">Добавить ещё</Link>
            <Button variant="ghost" disabled={lines.isPending} onClick={() => setConfirmClear(true)}>Очистить</Button>
          </div>
        </Surface>

        {confirmClear ? (
          <Modal title="Очистить корзину?" onClose={() => setConfirmClear(false)}>
            <p className="text-muted">Все блюда будут удалены из корзины.</p>
            <div className="mt-5 flex flex-wrap gap-2">
              <Button variant="primary" onClick={() => { setConfirmClear(false); lines.mutate(() => []); }}>Очистить</Button>
              <Button variant="outline" onClick={() => setConfirmClear(false)}>Оставить</Button>
            </div>
          </Modal>
        ) : null}

        <FulfillmentSection cart={cart} onSaved={(next) => c.refetch().then(() => next)} />
        <Checkout cart={cart} isAuthed={auth.status === "user"} defaultPhone={auth.principal?.phone} onUnavailable={(items) => lines.mutate((cur) => cur.filter((l) => !items.some((u) => u.menuItemId === l.menuItemId)))}
          onDone={(order) => {
            localOrders.add({ id: order.id, number: order.number, restaurantId: order.restaurantId, createdAt: order.createdAt, totalAmount: order.total.amount, currency: order.total.currency });
            c.forget();
            nav(`/orders/${order.id}`);
          }} />
      </div>

      <aside className="lg:sticky lg:top-20 lg:self-start">
        <Surface>
          <h2 className="mb-3 text-[length:var(--lt-fs-price)]">Итого</h2>
          <dl className="grid gap-1 border-b border-dashed border-ink/30 pb-3">
            <Row k="Подытог" v={formatMoney(cart.subtotal.amount, cart.subtotal.currency)} />
            {cart.appliedPromotions.map((p) => (
              <Row key={p.promotionId ?? p.title} k={`Акция: ${p.title ?? ""}`} v={p.discount ? `−${formatMoney(p.discount.amount, p.discount.currency)}` : ""} />
            ))}
            {cart.discountTotal.amount > 0 && cart.appliedPromotions.length === 0 ? <Row k="Скидка" v={`−${formatMoney(cart.discountTotal.amount, cart.discountTotal.currency)}`} /> : null}
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
  const [locating, setLocating] = useState(false);
  const [geoErr, setGeoErr] = useState<string | null>(null);
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
      <StepTitle n={1}>Получение</StepTitle>
      <div role="radiogroup" aria-label="Способ получения" className="mb-3 flex flex-col gap-2 sm:flex-row">
        <Radio checked={type === "pickup"} onChange={choosePickup} label="Самовывоз" desc="Заберёте сами в ресторане" />
        <Radio checked={type === "delivery"} onChange={() => setType("delivery")} label="Доставка" desc="Привезёт курьер" />
      </div>
      {type === "delivery" ? (
        <div className="grid gap-3">
          <Field label="Адрес" value={raw} maxLength={300} onChange={(e) => setRaw(e.target.value)} placeholder="улица, дом, квартира, подъезд, этаж"
            hint="Подъезд, этаж, домофон и комментарий курьеру пока пишите в эту же строку." error={err?.code === "ADDRESS_REQUIRED" ? errorText(err) : null} />
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" disabled={locating} onClick={() => {
              if (!navigator.geolocation) return setGeoErr("Браузер не поддерживает геолокацию.");
              setGeoErr(null); setLocating(true);
              navigator.geolocation.getCurrentPosition(
                (p) => { setLocating(false); setPinFromMap({ lat: +p.coords.latitude.toFixed(6), lon: +p.coords.longitude.toFixed(6) }); },
                () => { setLocating(false); setGeoErr("Не удалось определить местоположение. Поставьте точку на карте."); },
                { timeout: 8000 },
              );
            }}>{locating ? "Определяем…" : "Моё местоположение"}</Button>
            <span className="text-sm text-muted">или нажмите на карту</span>
          </div>
          {geoErr ? <Notice tone="info">{geoErr}</Notice> : null}
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

function StepTitle({ n, children }: { n: number; children: React.ReactNode }) {
  return (
    <h2 className="mb-3 flex items-center gap-3 text-[length:var(--lt-fs-price)]">
      <span aria-hidden className="grid size-8 place-items-center rounded-full bg-ink text-sm text-white">{n}</span>{children}
    </h2>
  );
}

function Radio({ checked, onChange, label, desc }: { checked: boolean; onChange: () => void; label: string; desc?: string }) {
  const id = useId();
  return (
    <label className={`flex min-h-[var(--lt-tap)] flex-1 cursor-pointer items-center gap-3 rounded-input border-2 px-4 py-2 has-[:focus-visible]:outline has-[:focus-visible]:outline-[3px] has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-ink ${checked ? "border-ink bg-lemon/30" : "border-ink/25 hover:border-ink"}`}>
      <input type="radio" className="sr-only" checked={checked} onChange={onChange} aria-labelledby={`${id}-t`} aria-describedby={desc ? `${id}-d` : undefined} />
      <span aria-hidden className={`grid size-6 shrink-0 place-items-center rounded-full border-2 border-ink ${checked ? "bg-ink" : "bg-white"}`}>{checked ? <span className="size-2 rounded-full bg-lemon" /> : null}</span>
      <span>
        <span id={`${id}-t`} className="block font-semibold">{label}</span>
        {desc ? <span id={`${id}-d`} className="block text-sm text-muted">{desc}</span> : null}
      </span>
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
  const phoneErr = phone && !isE164(normPhone) ? "Формат: +79991234567" : null;
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
        <StepTitle n={2}>Контакты</StepTitle>
        <div className="grid gap-3 md:grid-cols-2">
          <Field label={isAuthed ? "Телефон" : "Телефон (обязательно)"} type="tel" inputMode="tel" autoComplete="tel" value={phone} onChange={(e) => setPhone(e.target.value)}
            onBlur={() => setPhone(normalizePhone(phone))} error={phoneErr ?? (error?.code === "CONTACT_REQUIRED" ? errorText(error) : null)} placeholder="+7 999 123-45-67" />
          <Field label="Имя" autoComplete="name" maxLength={100} value={name} onChange={(e) => setName(e.target.value)} hint="Необязательно" />
          <Field label="Почта" type="email" autoComplete="email" maxLength={254} value={email} onChange={(e) => setEmail(e.target.value)} error={emailErr} hint="Необязательно. Письма пока не отправляем." className="md:col-span-2" />
        </div>
      </Surface>

      <Surface>
        <StepTitle n={3}>Оплата</StepTitle>
        {!fType ? <p className="text-muted">Сначала выберите способ получения.</p> : (
          <div role="radiogroup" aria-label="Способ оплаты" className="flex flex-col gap-2 sm:flex-row">
            {allowed.map((m) => <Radio key={m} checked={method === m} onChange={() => setMethod(m)} label={PAY_LABEL[m]} desc={PAY_DESC[m]} />)}
          </div>
        )}
        <p className="mt-2 text-xs text-muted">Способ оплаты нельзя изменить после оформления. Наличных курьеру нет.</p>
      </Surface>

      {error && !dialog ? <Notice>{errorText(error)}{error.code === "IDEMPOTENCY_IN_PROGRESS" ? " Нажмите ещё раз через пару секунд." : ""}</Notice> : null}
      <div className="sticky bottom-[68px] z-20 -mx-4 grid gap-1 bg-paper/95 px-4 py-3 backdrop-blur md:static md:mx-0 md:bg-transparent md:p-0 md:backdrop-blur-none">
        {missing ? <p className="text-center text-sm text-muted" role="status">{missing}</p> : null}
        <Button variant="accent" cta className="w-full" disabled={!ready || place.isPending} onClick={() => place.mutate()}>
          {place.isPending ? "Оформляем…" : `Оформить заказ · ${formatMoney(cart.total.amount, cart.total.currency)}`}
        </Button>
      </div>

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
