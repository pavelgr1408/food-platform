import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { isApiError, type MenuItem, type PromotionPublic, type RestaurantDetails } from "@fp/api";
import { formatBasisPoints, formatMoney } from "@fp/shared";
import { fullAddress } from "../lib/format";
import { useApi } from "../app/api-context";
import { Badge, Button, Empty, Notice, Price, QtyStepper, Skeleton, Surface } from "../components/ui";
import { Cover, Thumb, imageOf } from "../components/art";
import { Modal } from "../components/Modal";
import { useCart } from "../lib/cart";
import { errorText } from "../lib/errors";
import { scheduleRows, upcomingExceptions } from "../lib/schedule";

type Pending = { item: MenuItem; mods: string[]; qty: number };

export function Restaurant() {
  const { restaurantId = "" } = useParams();
  const api = useApi();
  const cart = useCart();
  const nav = useNavigate();

  const rest = useQuery({ queryKey: ["restaurant", restaurantId], queryFn: () => api.restaurants.get(restaurantId), retry: false });
  const menu = useQuery({ queryKey: ["menu", restaurantId], queryFn: () => api.restaurants.menu(restaurantId), retry: false });
  const promos = useQuery({ queryKey: ["promotions", restaurantId], queryFn: () => api.promotions.list(restaurantId), retry: false });

  const [picked, setPicked] = useState<{ item: MenuItem; category: string } | null>(null);
  const [switchTo, setSwitchTo] = useState<Pending | null>(null);
  const [flash, setFlash] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [active, setActive] = useState<string | null>(null);
  const flashTimer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(flashTimer.current), []);
  const say = (text: string) => { setFlash(text); clearTimeout(flashTimer.current); flashTimer.current = setTimeout(() => setFlash(null), 3000); };

  const add = useMutation({
    mutationFn: (v: Pending) => cart.addItem(restaurantId, { menuItemId: v.item.id, quantity: v.qty, modifierIds: v.mods }),
    onSuccess: (_c, v) => { setPicked(null); setSwitchTo(null); say(`«${v.item.name}» в корзине`); },
  });
  const bump = useMutation({
    mutationFn: (v: { item: MenuItem; delta: number }) => cart.bump(restaurantId, v.item.id, v.delta),
    onSuccess: (_c, v) => { if (v.delta > 0) say(`«${v.item.name}» в корзине`); },
    onError: (e) => say(errorText(e)),
  });

  const otherRestaurant = !!cart.cartId && cart.restaurantId !== restaurantId;
  /** UC-CRT-01: смена ресторана = новая корзина, сначала спросить. */
  const request = (p: Pending) => {
    if (otherRestaurant && cart.count > 0) { setPicked(null); setSwitchTo(p); return; }
    if (otherRestaurant) cart.startNew();
    add.mutate(p);
  };
  const quick = (item: MenuItem, delta: number) => {
    if (otherRestaurant && cart.count > 0) { setSwitchTo({ item, mods: [], qty: 1 }); return; }
    if (otherRestaurant) cart.startNew();
    bump.mutate({ item, delta });
  };

  const inCart = (id: string) => (cart.restaurantId === restaurantId ? (cart.cart?.items ?? []).filter((i) => i.menuItemId === id).reduce((n, i) => n + i.quantity, 0) : 0);
  const mineCount = cart.restaurantId === restaurantId ? cart.count : 0;

  const q = search.trim().toLowerCase();
  const categories = useMemo(() => {
    const cats = menu.data?.categories ?? [];
    if (!q) return cats;
    return cats.map((c) => ({ ...c, items: c.items.filter((i) => `${i.name} ${i.description ?? ""}`.toLowerCase().includes(q)) })).filter((c) => c.items.length > 0);
  }, [menu.data, q]);

  // подсветка текущей категории при прокрутке
  useEffect(() => {
    if (typeof IntersectionObserver === "undefined" || categories.length === 0) return;
    const els = categories.map((c) => document.getElementById(`cat-${c.id}`)).filter((e): e is HTMLElement => !!e);
    const io = new IntersectionObserver((entries) => {
      const top = entries.filter((e) => e.isIntersecting).sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)[0];
      if (top) setActive(top.target.id.replace("cat-", ""));
    }, { rootMargin: "-130px 0px -65% 0px" });
    els.forEach((e) => io.observe(e));
    return () => io.disconnect();
  }, [categories]);
  const tabsRef = useRef<HTMLElement>(null);
  useEffect(() => {
    tabsRef.current?.querySelector<HTMLElement>('[aria-current="true"]')?.scrollIntoView?.({ block: "nearest", inline: "center" });
  }, [active]);
  const jump = (id: string) => { setActive(id); document.getElementById(`cat-${id}`)?.scrollIntoView({ behavior: "smooth", block: "start" }); };

  if (rest.isLoading) return <RestaurantSkeleton />;
  if (rest.isError) {
    return (
      <Empty page title={isApiError(rest.error) && rest.error.status === 404 ? "Ресторан недоступен" : "Не удалось загрузить ресторан"}>
        <p>{errorText(rest.error)}</p>
        <Link to="/" className="mt-3 inline-block font-semibold underline">К списку ресторанов</Link>
      </Empty>
    );
  }
  const r = rest.data!;
  const photo = imageOf(r);

  return (
    <div className="lt-rise">
      <div className="relative -mx-4 -mt-5 mb-4 h-40 overflow-hidden md:mx-0 md:mt-0 md:h-52 md:rounded-card">
        {photo ? <img src={photo} alt="" className="size-full object-cover" /> : <Cover name={r.name} seed={r.id} className={`h-full w-full ${r.openNow ? "" : "grayscale"}`} />}
        <Link to="/" aria-label="К списку ресторанов" className="absolute left-3 top-3 grid size-11 place-items-center rounded-full bg-white text-xl font-bold shadow">←</Link>
      </div>

      <Surface className="mb-5">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h1 className="text-[length:var(--lt-fs-h2)] leading-tight md:text-[length:var(--lt-fs-h1)]">{r.name}</h1>
            <p className="mt-1 text-muted">{fullAddress(r)}</p>
          </div>
          <Badge tone={r.openNow ? "ok" : "neutral"} className={r.openNow ? "" : "border-2 border-ink"}>{r.openNow ? "Открыто" : "Закрыто"}</Badge>
        </div>
        <div className="mt-3 flex flex-wrap gap-2">
          <Badge tone={r.deliveryEnabled ? "warn" : "neutral"}>{r.deliveryEnabled ? `Доставка до ${(r.deliveryRadiusMeters ?? 0) / 1000} км` : "Только самовывоз"}</Badge>
          <a className="inline-flex min-h-11 items-center rounded-full border-2 border-ink px-4 text-sm font-semibold hover:bg-ink/5" target="_blank" rel="noreferrer"
            href={`https://www.openstreetmap.org/directions?to=${r.location.lat}%2C${r.location.lon}`}>Построить маршрут</a>
        </div>
        {!r.openNow ? <Notice tone="info" className="mt-3">Сейчас ресторан закрыт. Меню можно посмотреть, а оформить заказ получится в рабочее время.</Notice> : null}
        <Hours r={r} />
      </Surface>

      {promos.data && promos.data.length > 0 ? <Promos list={promos.data} /> : null}

      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-[length:var(--lt-fs-h2)]">Меню</h2>
        {menu.data ? (
          <div className="w-full sm:w-72">
            <label htmlFor="menu-search" className="sr-only">Поиск по меню</label>
            <input id="menu-search" type="search" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Найти блюдо"
              className="min-h-[var(--lt-tap)] w-full rounded-full border-2 border-ink/25 bg-white px-5 placeholder:text-faint focus:border-ink" />
          </div>
        ) : null}
      </div>

      {menu.isLoading ? <MenuSkeleton /> : null}
      {menu.isError ? <Notice>{isApiError(menu.error) && menu.error.code === "MENU_NOT_FOUND" ? "Меню временно недоступно." : errorText(menu.error)}</Notice> : null}
      {menu.data ? (
        <>
          {categories.length > 1 ? (
            <nav ref={tabsRef} aria-label="Категории" className="sticky top-[60px] z-20 -mx-4 mb-4 flex gap-2 overflow-x-auto bg-paper/95 px-4 py-2 backdrop-blur [scrollbar-width:none]">
              {categories.map((c) => (
                <button key={c.id} type="button" onClick={() => jump(c.id)} aria-current={active === c.id ? "true" : undefined}
                  className="min-h-11 shrink-0 rounded-full border-2 border-ink/25 bg-white px-4 font-semibold transition-colors aria-[current=true]:border-ink aria-[current=true]:bg-ink aria-[current=true]:text-white">{c.name}</button>
              ))}
            </nav>
          ) : null}
          {q && categories.length === 0 ? <Empty title="Такого блюда нет">Попробуйте другое слово или <button className="font-semibold underline" onClick={() => setSearch("")}>покажите всё меню</button>.</Empty> : null}
          {categories.map((c) => (
            <section key={c.id} id={`cat-${c.id}`} className="mb-8 scroll-mt-32">
              <h3 className="mb-3 text-[length:var(--lt-fs-price)]">{c.name}</h3>
              <ul className="grid gap-3 sm:grid-cols-2">
                {c.items.map((it) => {
                  const qty = inCart(it.id);
                  const hasMods = it.modifiers.length > 0;
                  return (
                    <li key={it.id} onClick={() => setPicked({ item: it, category: c.name })}
                      className={`flex cursor-pointer gap-3 rounded-card bg-white p-3 shadow-[0_1px_0_var(--lt-border)] transition-shadow hover:shadow-[0_8px_24px_-14px_rgba(18,19,26,0.4)] ${it.available ? "" : "cursor-default"}`}>
                      <div className="flex min-w-0 flex-1 flex-col">
                        <h4 className={`font-display font-semibold leading-snug ${it.available ? "" : "text-faint"}`}>{it.name}</h4>
                        {it.description ? <p className="mt-1 line-clamp-2 text-sm text-muted">{it.description}</p> : null}
                        <div className="mt-auto flex items-center justify-between gap-2 pt-3">
                          {it.available ? <Price money={it.price} /> : <span className="rounded-full border-2 border-faint px-3 py-1 text-sm font-medium text-faint">Нет в наличии</span>}
                        </div>
                      </div>
                      <div className="relative shrink-0 self-start">
                        <Thumb name={it.name} category={c.name} seed={it.id} imageUrl={imageOf(it)} className={`size-28 rounded-tile ${it.available ? "" : "opacity-50 grayscale"}`} />
                        {it.available ? (
                          <div className="absolute inset-x-0 -bottom-3 flex justify-center" onClick={(e) => e.stopPropagation()}>
                            {!hasMods && qty > 0 ? (
                              <QtyStepper size="sm" label={it.name} value={qty} onDec={() => quick(it, -1)} onInc={() => quick(it, 1)} />
                            ) : (
                              <button type="button" aria-label={hasMods ? `Выбрать «${it.name}»` : `Добавить «${it.name}»`}
                                onClick={() => (hasMods ? setPicked({ item: it, category: c.name }) : quick(it, 1))}
                                className="relative grid size-11 place-items-center rounded-full bg-ink text-2xl font-semibold leading-none text-white shadow-lg hover:bg-black">
                                +
                                {hasMods && qty > 0 ? <span className="absolute -right-1 -top-1 grid min-w-5 place-items-center rounded-full bg-tomato px-1 text-[11px] font-bold">{qty}</span> : null}
                              </button>
                            )}
                          </div>
                        ) : null}
                      </div>
                    </li>
                  );
                })}
              </ul>
            </section>
          ))}
        </>
      ) : null}

      {mineCount > 0 || flash ? (
        <div role="status" className="fixed inset-x-4 bottom-20 z-40 mx-auto flex max-w-md items-center justify-between gap-3 rounded-full bg-ink py-2 pl-5 pr-2 text-white shadow-xl md:bottom-6">
          <span className="min-w-0 truncate">{flash ?? `${mineCount} в корзине${cart.cart ? ` · ${formatMoney(cart.cart.total.amount, cart.cart.total.currency)}` : ""}`}</span>
          <button className="min-h-11 shrink-0 rounded-full bg-lemon px-5 font-semibold text-ink" onClick={() => nav("/cart")}>Корзина</button>
        </div>
      ) : null}

      {picked ? (
        <ItemDialog item={picked.item} category={picked.category} busy={add.isPending} error={add.error} onClose={() => setPicked(null)} onAdd={(mods, qty) => request({ item: picked.item, mods, qty })} />
      ) : null}

      {switchTo ? (
        <Modal title="Начать новую корзину?" onClose={() => setSwitchTo(null)}>
          <p className="text-muted">В корзине блюда из другого ресторана. Чтобы заказать здесь, корзину придётся начать заново.</p>
          {add.error || bump.error ? <Notice className="mt-3">{errorText(add.error ?? bump.error)}</Notice> : null}
          <div className="mt-5 flex flex-wrap gap-2">
            <Button variant="accent" disabled={add.isPending || bump.isPending} onClick={() => { cart.startNew(); const p = switchTo; setSwitchTo(null); add.mutate(p); }}>Начать новую</Button>
            <Button variant="outline" onClick={() => setSwitchTo(null)}>Оставить прежнюю</Button>
          </div>
        </Modal>
      ) : null}
    </div>
  );
}

function ItemDialog({ item, category, onClose, onAdd, busy, error }: { item: MenuItem; category: string; onClose: () => void; onAdd: (mods: string[], qty: number) => void; busy: boolean; error: unknown }) {
  const [mods, setMods] = useState<Set<string>>(new Set());
  const [qty, setQty] = useState(1);
  // UC-MNU-02: предпросмотр на клиенте, истина — ответ сервера
  const unit = useMemo(() => item.price.amount + item.modifiers.filter((m) => mods.has(m.id)).reduce((s, m) => s + m.price.amount, 0), [item, mods]);
  const toggle = (id: string) => setMods((s) => { const n = new Set(s); if (n.has(id)) n.delete(id); else if (n.size < 20) n.add(id); return n; });
  return (
    <Modal title={item.name} onClose={onClose}
      hero={<Thumb name={item.name} category={category} seed={item.id} imageUrl={imageOf(item)} className={`h-44 w-full [@media(max-height:700px)]:h-28 ${item.available ? "" : "grayscale"}`} />}
      footer={item.available ? (
        <>
          <div className="mb-3 flex items-center justify-between">
            <span className="font-semibold">Количество</span>
            <QtyStepper label={item.name} value={qty} onDec={() => setQty((q) => Math.max(1, q - 1))} onInc={() => setQty((q) => Math.min(99, q + 1))} />
          </div>
          {error ? <Notice className="mb-3">{errorText(error)}</Notice> : null}
          <Button variant="accent" cta className="w-full" disabled={busy} onClick={() => onAdd([...mods], qty)}>
            {busy ? "Добавляем…" : `В корзину · ${formatMoney(unit * qty, item.price.currency)}`}
          </Button>
        </>
      ) : <Button variant="outline" className="w-full" onClick={onClose}>Закрыть</Button>}>
      {item.description ? <p className="mb-3 text-muted">{item.description}</p> : null}
      {!item.available ? <Notice className="mb-3" tone="info">Сейчас этого блюда нет в наличии.</Notice> : null}
      {item.available && item.modifiers.length > 0 ? (
        <fieldset className="mb-4">
          <legend className="mb-2 font-semibold">Добавки</legend>
          <ul className="grid gap-2">
            {item.modifiers.map((m) => (
              <li key={m.id}>
                <label className={`flex min-h-[var(--lt-tap)] items-center gap-3 rounded-input border-2 px-4 ${mods.has(m.id) ? "border-ink bg-lemon/30" : "border-ink/25"} ${m.available ? "cursor-pointer" : "opacity-50"}`}>
                  <input type="checkbox" className="size-5 accent-[var(--lt-tomato)]" disabled={!m.available} checked={mods.has(m.id)} onChange={() => toggle(m.id)} />
                  <span className="flex-1">{m.name}{m.available ? "" : " — нет в наличии"}</span>
                  <span className="font-semibold">+{formatMoney(m.price.amount, m.price.currency)}</span>
                </label>
              </li>
            ))}
          </ul>
        </fieldset>
      ) : null}
    </Modal>
  );
}

function Hours({ r }: { r: Pick<RestaurantDetails, "workingHours" | "scheduleExceptions" | "timezone"> }) {
  const rows = scheduleRows(r);
  const ex = upcomingExceptions(r);
  const local = typeof Intl !== "undefined" ? Intl.DateTimeFormat().resolvedOptions().timeZone : r.timezone;
  return (
    <details className="mt-3">
      <summary className="inline-flex min-h-[var(--lt-tap)] cursor-pointer items-center font-semibold underline">График работы</summary>
      {local !== r.timezone ? <p className="text-sm text-muted">Время в часовом поясе ресторана: {r.timezone}</p> : null}
      <table className="mt-2 w-full max-w-sm text-sm">
        <tbody>
          {rows.map((d) => (
            <tr key={d.day} className="border-b border-ink/10"><th scope="row" className="py-1 pr-4 text-left font-medium">{d.name}</th><td>{d.intervals.length ? d.intervals.join(", ") : "выходной"}</td></tr>
          ))}
        </tbody>
      </table>
      {ex.length ? (
        <div className="mt-3 text-sm">
          <p className="font-semibold">Особые дни</p>
          <ul>{ex.map((e) => <li key={e.date}>{e.date}: {e.text}{e.note ? ` — ${e.note}` : ""}</li>)}</ul>
        </div>
      ) : null}
    </details>
  );
}

function Promos({ list }: { list: PromotionPublic[] }) {
  const fmt = (d: string) => new Date(d).toLocaleDateString("ru-RU", { day: "numeric", month: "long" });
  return (
    <section aria-label="Акции" className="mb-6 flex snap-x gap-3 overflow-x-auto pb-1 md:grid md:grid-cols-2 md:overflow-visible">
      {list.map((p) => (
        <article key={p.id} className="relative min-w-[85%] snap-start overflow-hidden rounded-card bg-tomato p-5 text-white md:min-w-0">
          <div aria-hidden className="absolute -right-6 -top-6 size-24 rounded-full bg-lemon/40" />
          <p className="relative font-display text-2xl font-semibold">
            {p.discount.type === "percent" ? `−${formatBasisPoints(p.discount.value)}` : `−${formatMoney(p.discount.value, p.currency ?? "RUB")}`}
          </p>
          <h3 className="relative mt-1 font-display font-semibold">{p.title}</h3>
          {p.description ? <p className="relative mt-1 text-sm text-white/90">{p.description}</p> : null}
          <p className="relative mt-2 text-sm text-white/90">
            {p.conditions?.minSubtotal ? `от ${formatMoney(p.conditions.minSubtotal, p.currency ?? "RUB")} · ` : ""}
            {p.conditions?.fulfillment === "pickup" ? "самовывоз · " : p.conditions?.fulfillment === "delivery" ? "доставка · " : ""}
            {fmt(p.startsAt)} – {fmt(p.endsAt)}
          </p>
        </article>
      ))}
    </section>
  );
}

function RestaurantSkeleton() {
  return <div aria-busy="true" role="status" aria-label="Загружаем ресторан" className="grid gap-4"><Skeleton className="h-40 rounded-card" /><Skeleton className="h-40 rounded-card" /><Skeleton className="h-32 rounded-card" /></div>;
}
function MenuSkeleton() {
  return <ul aria-busy="true" role="status" aria-label="Загружаем меню" className="grid gap-3 sm:grid-cols-2">{[0, 1, 2, 3].map((i) => <li key={i}><Skeleton className="h-36 rounded-card" /></li>)}</ul>;
}
