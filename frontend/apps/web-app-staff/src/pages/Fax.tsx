import { useId, useRef, useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { isApiError, type ApiError, type Cart, type CartItemInput, type MenuItem, type Order, type PaymentMethodType, type UnavailableItem } from "@fp/api";
import { formatMoney, isE164, normalizePhone } from "@fp/shared";
import { Badge, Button, Field, Modal, Notice, PageTitle, Price, QtyStepper, Skeleton, Surface, cx, errorText, useApi } from "@fp/ui";
import { MapPicker, type LatLon } from "@fp/ui/map";
import { useWorkplace } from "../app/workplace";
import { lineKey, useFaxCart } from "../lib/faxCart";
import { PAY_LABEL } from "../lib/order";

/** Оплата определяется способом получения: онлайн-оплата для заказа по факсу запрещена (UC-STF-04). */
const PAY_FOR: Record<"pickup" | "delivery", PaymentMethodType> = { pickup: "pos", delivery: "courier_card" };

/** Заказ по факсу: сотрудник вводит заказ клиента (UC-STF-02…05). Заказ без владельца, push клиент не получит. */
export function Fax() {
  const api = useApi();
  const wp = useWorkplace();
  const rest = wp.current!;
  const fc = useFaxCart(rest.id);
  const cart = fc.cart;

  const menu = useQuery({ queryKey: ["menu", rest.id], queryFn: () => api.restaurants.menu(rest.id), staleTime: 60_000, retry: false });
  const [category, setCategory] = useState<string | null>(null);
  const [picking, setPicking] = useState<MenuItem | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [created, setCreated] = useState<Order | null>(null);

  const onItemsError = (e: unknown) => { if (isApiError(e) && e.code === "ITEMS_UNAVAILABLE") setNote(`Убрали недоступных позиций: ${((e.extra.unavailableItems as unknown[]) ?? []).length}`); };
  const edit = useMutation({ mutationFn: (fn: (cur: CartItemInput[]) => CartItemInput[]) => fc.update(fn), onSuccess: () => setNote(null), onError: onItemsError });
  const add = useMutation({ mutationFn: (l: CartItemInput) => fc.add(l), onSuccess: () => setNote(null), onError: onItemsError });
  const err = (add.error ?? edit.error) as ApiError | null;

  if (created) return <Done order={created} onNew={() => { setCreated(null); }} />;

  const cats = menu.data?.categories ?? [];
  const activeCat = cats.find((c) => c.id === category) ?? cats[0];
  const lines = cart?.items ?? [];
  const bump = (key: string, d: number) => edit.mutate((cur) => cur.flatMap((l) => (lineKey(l) !== key ? [l] : Math.min(99, l.quantity + d) > 0 ? [{ ...l, quantity: Math.min(99, l.quantity + d) }] : [])));

  return (
    <div className="lt-rise grid gap-5 lg:grid-cols-[1fr_420px]">
      <div className="grid content-start gap-4">
        <PageTitle sub={`${rest.name} · заказ диктует клиент по телефону или присылает по факсу`}>Заказ по факсу</PageTitle>
        {note ? <Notice tone="info">{note}</Notice> : null}
        {err && !(err.code === "ITEMS_UNAVAILABLE") ? <Notice>{errorText(err)}</Notice> : null}

        {menu.isLoading ? <div role="status" aria-label="Загружаем меню" className="grid gap-3"><Skeleton className="h-12 rounded-full" /><Skeleton className="h-24 rounded-card" /></div>
          : menu.isError ? <Notice>{errorText(menu.error, "Не удалось загрузить меню.")} <button className="font-bold underline" onClick={() => void menu.refetch()}>Повторить</button></Notice>
          : (
            <>
              <div role="tablist" aria-label="Категории меню" className="flex flex-wrap gap-2">
                {cats.map((c) => (
                  <button key={c.id} type="button" role="tab" aria-selected={activeCat?.id === c.id} onClick={() => setCategory(c.id)}
                    className={cx("min-h-11 rounded-full border-2 px-4 font-semibold", activeCat?.id === c.id ? "border-ink bg-ink text-white" : "border-ink bg-white hover:bg-ink/5")}>{c.name}</button>
                ))}
              </div>
              <ul className="grid gap-3 sm:grid-cols-2" aria-label={activeCat?.name}>
                {(activeCat?.items ?? []).map((it) => {
                  const mods = it.modifiers.filter((m) => m.available);
                  return (
                    <li key={it.id} className={cx("flex flex-col justify-between gap-3 rounded-card bg-white p-4 shadow-[0_1px_0_var(--lt-border)]", !it.available && "opacity-60")}>
                      <div>
                        <p className="font-semibold leading-snug">{it.name}</p>
                        {it.description ? <p className="mt-1 text-sm text-muted">{it.description}</p> : null}
                        {!it.available ? <p className="mt-1 text-sm font-semibold text-tomato">Нет в наличии</p> : null}
                      </div>
                      <div className="flex items-center justify-between gap-2">
                        <Price money={it.price} />
                        {it.available ? (
                          mods.length ? (
                            <Button variant="outline" aria-label={`Добавить «${it.name}» с добавками`} onClick={() => setPicking(it)}>Добавки…</Button>
                          ) : (
                            <Button variant="accent" aria-label={`Добавить «${it.name}»`} disabled={add.isPending} onClick={() => add.mutate({ menuItemId: it.id, quantity: 1, modifierIds: [] })}>+ Добавить</Button>
                          )
                        ) : null}
                      </div>
                    </li>
                  );
                })}
              </ul>
            </>
          )}
      </div>

      <aside className="grid content-start gap-4 lg:sticky lg:top-24 lg:self-start">
        <Surface>
          <h2 className="mb-3 text-[length:var(--lt-fs-price)]">Заказ</h2>
          {fc.isLoading ? <Skeleton className="h-20 rounded-tile" /> : lines.length === 0 ? <p className="text-muted">Пока пусто. Выберите блюда в меню.</p> : (
            <>
              <ul className="divide-y divide-dashed divide-ink/20">
                {lines.map((it) => {
                  const mods = it.modifiers?.map((m) => m.name).join(", ");
                  return (
                    <li key={lineKey(it)} className={cx("py-2 first:pt-0 last:pb-0", !it.available && "opacity-60")}>
                      <div className="flex items-start justify-between gap-2">
                        <div className="min-w-0">
                          <p className="font-semibold leading-snug">{it.name}</p>
                          {mods ? <p className="text-sm text-muted">{mods}</p> : null}
                          {!it.available ? <p className="text-sm font-semibold text-tomato">Нет в наличии</p> : null}
                        </div>
                        <span className="whitespace-nowrap font-medium">{formatMoney(it.lineTotal.amount, it.lineTotal.currency)}</span>
                      </div>
                      <div className="mt-1 flex items-center justify-between">
                        <QtyStepper size="sm" label={it.name} value={it.quantity} busy={edit.isPending} onDec={() => bump(lineKey(it), -1)} onInc={() => bump(lineKey(it), 1)} />
                        <button type="button" aria-label={`Убрать «${it.name}»`} className="min-h-11 rounded-full px-3 text-sm font-semibold text-muted underline hover:text-ink" onClick={() => bump(lineKey(it), -it.quantity)}>Убрать</button>
                      </div>
                    </li>
                  );
                })}
              </ul>
              {lines.some((i) => !i.available) ? (
                <Button className="mt-3" variant="accent" disabled={edit.isPending} onClick={() => edit.mutate((cur) => cur.filter((l) => lines.find((x) => lineKey(x) === lineKey(l))?.available))}>Удалить недоступные</Button>
              ) : null}
              <Totals cart={cart!} />
            </>
          )}
        </Surface>

        {cart && lines.length > 0 ? (
          <Checkout cart={cart} restaurantDeliveryEnabled={rest.deliveryEnabled} restaurantRadius={rest.deliveryRadiusMeters}
            onUnavailable={(items) => edit.mutate((cur) => cur.filter((l) => !items.some((u) => u.menuItemId === l.menuItemId)))}
            onRefetch={() => void fc.refetch()}
            onDone={(o) => { fc.forget(); setCreated(o); }} onClosed={fc.forget} />
        ) : null}
      </aside>

      {picking ? <ModifierDialog item={picking} onClose={() => setPicking(null)} onAdd={(l) => { setPicking(null); add.mutate(l); }} /> : null}
    </div>
  );
}

function Totals({ cart }: { cart: Cart }) {
  return (
    <div className="mt-3 border-t border-dashed border-ink/30 pt-3">
      <dl className="grid gap-1">
        <div className="flex justify-between"><dt>Подытог</dt><dd className="font-medium">{formatMoney(cart.subtotal.amount, cart.subtotal.currency)}</dd></div>
        {cart.appliedPromotions.map((p) => (
          <div key={p.promotionId ?? p.title} className="flex justify-between"><dt>Акция: {p.title}</dt><dd className="font-medium">{p.discount ? `−${formatMoney(p.discount.amount, p.discount.currency)}` : ""}</dd></div>
        ))}
      </dl>
      <p className="mt-2 flex items-center justify-between"><span className="font-semibold">К оплате</span><Price money={cart.total} big /></p>
    </div>
  );
}

/* ----------------------- Добавки (UC-MNU-02): чекбоксы, аддитивные ----------------------- */
function ModifierDialog({ item, onClose, onAdd }: { item: MenuItem; onClose: () => void; onAdd: (l: CartItemInput) => void }) {
  const [picked, setPicked] = useState<string[]>([]);
  const [qty, setQty] = useState(1);
  const sum = item.price.amount + item.modifiers.filter((m) => picked.includes(m.id)).reduce((s, m) => s + m.price.amount, 0);
  const toggle = (id: string) => setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]));
  return (
    <Modal title={item.name} onClose={onClose}
      footer={<Button variant="accent" cta className="w-full" onClick={() => onAdd({ menuItemId: item.id, quantity: qty, modifierIds: picked })}>Добавить · {formatMoney(sum * qty, item.price.currency)}</Button>}>
      {item.description ? <p className="mb-3 text-muted">{item.description}</p> : null}
      <fieldset className="mb-4 grid gap-2">
        <legend className="mb-1 text-sm font-semibold">Добавки</legend>
        {item.modifiers.map((m) => (
          <label key={m.id} className={cx("flex min-h-[var(--lt-tap)] items-center gap-3 rounded-input border-2 border-ink/30 px-4 py-2", m.available ? "cursor-pointer" : "cursor-not-allowed opacity-50")}>
            <input type="checkbox" className="size-5 accent-[var(--lt-tomato)]" disabled={!m.available} checked={picked.includes(m.id)} onChange={() => toggle(m.id)} />
            <span className="flex-1 font-medium">{m.name}{!m.available ? " · нет в наличии" : ""}</span>
            <span className="text-muted">+{formatMoney(m.price.amount, m.price.currency)}</span>
          </label>
        ))}
      </fieldset>
      <QtyStepper label={item.name} value={qty} onDec={() => setQty((q) => Math.max(1, q - 1))} onInc={() => setQty((q) => Math.min(99, q + 1))} />
    </Modal>
  );
}

/* ----------------------- Клиент, получение, оформление (UC-STF-03…05) ----------------------- */
function Checkout({ cart, restaurantDeliveryEnabled, restaurantRadius, onDone, onUnavailable, onRefetch, onClosed }: {
  cart: Cart; restaurantDeliveryEnabled?: boolean; restaurantRadius?: number;
  onDone: (o: Order) => void; onUnavailable: (i: UnavailableItem[]) => void; onRefetch: () => void; onClosed: () => void;
}) {
  const api = useApi();
  const qc = useQueryClient();
  const [phone, setPhone] = useState("");
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const normPhone = normalizePhone(phone);
  const phoneErr = phone && !isE164(normPhone) ? "Формат: +79991234567" : null;
  const emailErr = email && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email) ? "Проверьте адрес почты" : null;

  const fType = cart.fulfillment?.type;
  const method = fType ? PAY_FOR[fType] : null;

  const key = useRef<{ key: string; sig: string } | null>(null);
  const body = {
    cartId: cart.id, channel: "staff_fax" as const, paymentMethod: method!,
    contact: { phone: normPhone || undefined, name: name.trim() || undefined, email: email.trim() || undefined },
    expectedTotal: cart.total,
  };
  const sig = JSON.stringify(body);
  const [dialog, setDialog] = useState<null | { kind: "price" | "total" | "items"; was?: number; items?: UnavailableItem[] }>(null);
  const busy = useRef(false);

  const place = useMutation({
    onSettled: () => { busy.current = false; },
    mutationFn: async () => {
      if (!key.current || key.current.sig !== sig) key.current = { key: api.newIdempotencyKey(), sig };
      return api.orders.create(body, key.current.key);
    },
    onSuccess: ({ order }) => { void qc.invalidateQueries({ queryKey: ["queue"] }); void qc.invalidateQueries({ queryKey: ["queue-count"] }); onDone(order); },
    onError: async (e) => {
      if (!isApiError(e)) return;
      if (e.status >= 400 && e.status < 500 && e.code !== "IDEMPOTENCY_IN_PROGRESS") key.current = null; // 4xx сохраняются под ключом
      if (e.code === "PRICE_CHANGED") setDialog({ kind: "price" });
      else if (e.code === "TOTAL_CHANGED") setDialog({ kind: "total", was: cart.total.amount });
      else if (e.code === "ITEMS_UNAVAILABLE") setDialog({ kind: "items", items: (e.extra.unavailableItems as UnavailableItem[]) ?? [] });
      if (["PRICE_CHANGED", "TOTAL_CHANGED", "CART_CHANGED"].includes(e.code)) { await qc.invalidateQueries({ queryKey: ["fax-cart", cart.id] }); onRefetch(); }
      if (e.code === "CART_CLOSED") onClosed();
    },
  });
  const error = place.error as ApiError | null;

  const ready = !!fType && isE164(normPhone) && !emailErr && cart.items.every((i) => i.available) && cart.items.length > 0;
  const missing = !fType ? "Выберите способ получения" : !isE164(normPhone) ? "Укажите телефон клиента" : null;
  const go = () => { if (busy.current) return; busy.current = true; place.mutate(); };

  return (
    <>
      <Fulfillment cart={cart} deliveryEnabled={restaurantDeliveryEnabled} radius={restaurantRadius} onSaved={onRefetch} />

      <Surface>
        <h2 className="mb-3 text-[length:var(--lt-fs-price)]">Клиент</h2>
        <div className="grid gap-3">
          <Field label="Телефон (обязательно)" type="tel" inputMode="tel" autoComplete="off" aria-required placeholder="+7 999 123-45-67" value={phone} onChange={(e) => setPhone(e.target.value)}
            onBlur={() => setPhone(normalizePhone(phone))} error={phoneErr ?? (error?.code === "CONTACT_REQUIRED" ? errorText(error) : null)} />
          <Field label="Имя" autoComplete="off" maxLength={100} value={name} onChange={(e) => setName(e.target.value)} hint="Необязательно" />
          <Field label="Почта" type="email" autoComplete="off" maxLength={254} value={email} onChange={(e) => setEmail(e.target.value)} error={emailErr} hint="Необязательно. Письма пока не отправляются." />
        </div>
        <p className="mt-3 text-sm text-muted">Оплата: <strong>{method ? PAY_LABEL[method] : "определится способом получения"}</strong>. Онлайн-оплата для заказа по факсу недоступна. Клиент не получит push — сообщите ему номер заказа.</p>
      </Surface>

      {error && !dialog ? <Notice>{errorText(error, undefined, { FORBIDDEN: "Нет доступа к этому ресторану." })}{error.code === "IDEMPOTENCY_IN_PROGRESS" ? " Нажмите ещё раз через пару секунд." : ""}</Notice> : null}
      <div className="grid gap-1">
        {missing ? <p className="text-center text-sm text-muted" role="status">{missing}</p> : null}
        <Button variant="accent" cta className="w-full" disabled={!ready || place.isPending} onClick={go}>
          {place.isPending ? "Оформляем…" : `Оформить заказ · ${formatMoney(cart.total.amount, cart.total.currency)}`}
        </Button>
      </div>

      {dialog?.kind === "price" || dialog?.kind === "total" ? (
        <Modal title={dialog.kind === "price" ? "Цены обновились" : "Итог изменился"} onClose={() => setDialog(null)}>
          <p className="text-muted">
            {dialog.kind === "total" && dialog.was !== undefined ? `Было ${formatMoney(dialog.was, cart.total.currency)}, стало ${formatMoney(cart.total.amount, cart.total.currency)}. ` : ""}
            Новая сумма — <strong>{formatMoney(cart.total.amount, cart.total.currency)}</strong>. Назовите её клиенту. Заказ ещё не создан.
          </p>
          <div className="mt-5 flex flex-wrap gap-2">
            <Button variant="accent" onClick={() => { setDialog(null); go(); }}>Клиент согласен</Button>
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

/* ----------------------- Получение: самовывоз / доставка (UC-STF-04) ----------------------- */
function Fulfillment({ cart, deliveryEnabled, radius, onSaved }: { cart: Cart; deliveryEnabled?: boolean; radius?: number; onSaved: () => void }) {
  const api = useApi();
  const cur = cart.fulfillment;
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

  const save = useMutation({ mutationFn: (b: Parameters<typeof api.carts.putFulfillment>[1]) => api.carts.putFulfillment(cart.id, b), onSuccess: onSaved });
  const err = save.error as ApiError | null;
  const choosePickup = () => { setType("pickup"); save.mutate({ type: "pickup" }); };
  const hid = useId();
  const outOfZone = err?.code === "ADDRESS_OUT_OF_ZONE" || err?.code === "DELIVERY_NOT_AVAILABLE";

  return (
    <Surface>
      <h2 id={hid} className="mb-3 text-[length:var(--lt-fs-price)]">Получение</h2>
      <div role="radiogroup" aria-labelledby={hid} className="mb-3 grid gap-2 sm:grid-cols-2">
        <Choice checked={type === "pickup"} onChange={choosePickup} label="Самовывоз" desc="Оплата на кассе" />
        <Choice checked={type === "delivery"} onChange={() => setType("delivery")} disabled={deliveryEnabled === false} label="Доставка" desc={deliveryEnabled === false ? "Ресторан не доставляет" : "Оплата картой курьеру"} />
      </div>
      {type === "delivery" ? (
        <div className="grid gap-3">
          <Field label="Адрес" value={raw} maxLength={300} onChange={(e) => setRaw(e.target.value)} placeholder="улица, дом, квартира, подъезд, этаж" error={err?.code === "ADDRESS_REQUIRED" ? errorText(err) : null} />
          <MapPicker label="Карта доставки. Нажмите, чтобы поставить точку" value={pin} onChange={setPinFromMap} radiusMeters={radius} />
          <div className="grid grid-cols-2 gap-3">
            <Field label="Широта" inputMode="decimal" value={latS} onChange={(e) => typeCoord("lat", e.target.value)} />
            <Field label="Долгота" inputMode="decimal" value={lonS} onChange={(e) => typeCoord("lon", e.target.value)} />
          </div>
          {err?.code === "ADDRESS_LOCATION_REQUIRED" ? <Notice>{errorText(err)}</Notice> : null}
          {outOfZone ? <Notice>{errorText(err)} <button className="font-bold underline" onClick={choosePickup}>Предложить самовывоз</button></Notice> : null}
          {err && !["ADDRESS_REQUIRED", "ADDRESS_LOCATION_REQUIRED", "ADDRESS_OUT_OF_ZONE", "DELIVERY_NOT_AVAILABLE"].includes(err.code) ? <Notice>{errorText(err)}</Notice> : null}
          <Button variant="accent" disabled={save.isPending} onClick={() => save.mutate({ type: "delivery", address: { raw: raw.trim() || undefined, location: pin ?? undefined } })}>{save.isPending ? "Проверяем зону…" : "Проверить зону и сохранить"}</Button>
        </div>
      ) : err ? <Notice>{errorText(err)}</Notice> : null}
      {cur ? <p className="mt-3 text-sm font-medium text-muted">Выбрано: {cur.type === "pickup" ? "самовывоз" : `доставка — ${cur.address?.raw ?? "точка на карте"}`}</p> : null}
    </Surface>
  );
}

function Choice({ checked, onChange, label, desc, disabled }: { checked: boolean; onChange: () => void; label: string; desc?: string; disabled?: boolean }) {
  return (
    <label className={cx("flex min-h-[var(--lt-tap)] items-center gap-3 rounded-input border-2 px-4 py-2 has-[:focus-visible]:outline has-[:focus-visible]:outline-[3px] has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-ink",
      disabled ? "cursor-not-allowed opacity-50" : "cursor-pointer", checked ? "border-ink bg-lemon/40" : "border-ink/30")}>
      <input type="radio" className="sr-only" name="fax-fulfillment" disabled={disabled} checked={checked} onChange={onChange} />
      <span aria-hidden className={cx("grid size-6 shrink-0 place-items-center rounded-full border-2 border-ink", checked ? "bg-ink" : "bg-white")}>{checked ? <span className="size-2 rounded-full bg-lemon" /> : null}</span>
      <span><span className="block font-semibold">{label}</span>{desc ? <span className="block text-sm text-muted">{desc}</span> : null}</span>
    </label>
  );
}

/* ----------------------- Заказ принят (UC-STF-05): номер крупно — его диктуют клиенту ----------------------- */
function Done({ order, onNew }: { order: Order; onNew: () => void }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => { try { await navigator.clipboard.writeText(order.number); setCopied(true); } catch { /* буфер недоступен — номер виден на экране */ } };
  const pos = order.paymentMethod === "pos";
  return (
    <div className="lt-rise mx-auto grid max-w-xl gap-5 text-center">
      <Surface>
        <Badge tone="ok">Заказ принят</Badge>
        <p className="mt-4 text-muted">Назовите клиенту номер заказа:</p>
        <p className="lt-num my-3 break-all text-4xl font-bold md:text-5xl" aria-label={`Номер заказа ${order.number}`}>{order.number}</p>
        <p className="mb-4"><Price money={order.total} big /></p>
        <p className="text-muted">{pos ? "Оплата на кассе при получении: примите её в карточке заказа." : "Оплата картой курьеру при получении."} Клиент не получит push — только этот номер.</p>
        <div className="mt-5 flex flex-wrap justify-center gap-2">
          <Link to={`/o/${encodeURIComponent(order.number)}`} className="inline-flex min-h-[var(--lt-tap)] items-center rounded-full bg-lemon px-6 font-semibold text-ink">Открыть заказ</Link>
          <Button variant="outline" onClick={() => void copy()}>{copied ? "Скопировано" : "Скопировать номер"}</Button>
          <Button variant="primary" onClick={onNew}>Новый заказ</Button>
        </div>
      </Surface>
    </div>
  );
}
