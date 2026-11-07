import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { isApiError, type MenuItem, type PromotionPublic } from "@fp/api";
import { formatBasisPoints, formatMoney } from "@fp/shared";
import { useApi } from "../app/api-context";
import { Button, Empty, Notice, Price, Spinner, Surface } from "../components/ui";
import { Modal } from "../components/Modal";
import { useCart } from "../lib/cart";
import { errorText } from "../lib/errors";
import { scheduleRows, upcomingExceptions } from "../lib/schedule";

export function Restaurant() {
  const { restaurantId = "" } = useParams();
  const api = useApi();
  const cart = useCart();
  const nav = useNavigate();

  const rest = useQuery({ queryKey: ["restaurant", restaurantId], queryFn: () => api.restaurants.get(restaurantId), retry: false });
  const menu = useQuery({ queryKey: ["menu", restaurantId], queryFn: () => api.restaurants.menu(restaurantId), retry: false });
  const promos = useQuery({ queryKey: ["promotions", restaurantId], queryFn: () => api.promotions.list(restaurantId), retry: false });

  const [picked, setPicked] = useState<MenuItem | null>(null);
  const [switchTo, setSwitchTo] = useState<{ item: MenuItem; mods: string[]; qty: number } | null>(null);
  const [flash, setFlash] = useState<string | null>(null);

  const add = useMutation({
    mutationFn: async (v: { item: MenuItem; mods: string[]; qty: number }) =>
      cart.addItem(restaurantId, { menuItemId: v.item.id, quantity: v.qty, modifierIds: v.mods }),
    onSuccess: (_c, v) => { setPicked(null); setSwitchTo(null); setFlash(`«${v.item.name}» в корзине`); setTimeout(() => setFlash(null), 3000); },
  });

  /** UC-CRT-01: смена ресторана = новая корзина, сначала спросить. */
  const request = (item: MenuItem, mods: string[], qty: number) => {
    if (cart.cartId && cart.restaurantId !== restaurantId && cart.count > 0) { setPicked(null); setSwitchTo({ item, mods, qty }); return; }
    if (cart.cartId && cart.restaurantId !== restaurantId) cart.startNew();
    add.mutate({ item, mods, qty });
  };

  if (rest.isLoading) return <Spinner />;
  if (rest.isError) {
    return (
      <Empty title={isApiError(rest.error) && rest.error.status === 404 ? "Ресторан недоступен" : "Не удалось загрузить ресторан"}>
        <p>{errorText(rest.error)}</p>
        <Link to="/" className="mt-3 inline-block font-semibold underline">К списку ресторанов</Link>
      </Empty>
    );
  }
  const r = rest.data!;

  return (
    <div className="lt-rise">
      <Link to="/" className="mb-3 inline-flex min-h-[var(--lt-tap)] items-center font-semibold underline">← Рестораны</Link>
      <Surface className="mb-5">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h1 className="text-[length:var(--lt-fs-h2)] md:text-[length:var(--lt-fs-h1)]">{r.name}</h1>
            <p className="mt-1 text-muted">{r.city}, {r.address}</p>
          </div>
          <span className={`rounded-full px-3 py-1 text-sm font-semibold ${r.openNow ? "bg-mint" : "border-2 border-ink"}`}>{r.openNow ? "Открыто" : "Закрыто"}</span>
        </div>
        <p className="mt-3 text-sm">{r.deliveryEnabled ? `Доставка есть, радиус ${(r.deliveryRadiusMeters ?? 0) / 1000} км` : "Только самовывоз"} · пояс {r.timezone}</p>
        <div className="mt-4 flex flex-wrap gap-2">
          <a className="inline-flex min-h-[var(--lt-tap)] items-center rounded-full border-2 border-ink px-5 font-semibold" target="_blank" rel="noreferrer"
            href={`https://www.openstreetmap.org/directions?to=${r.location.lat}%2C${r.location.lon}`}>Построить маршрут</a>
        </div>
        <Hours r={r} />
      </Surface>

      {promos.data && promos.data.length > 0 ? <Promos list={promos.data} /> : null}

      <h2 className="mb-3 text-[length:var(--lt-fs-h2)]">Меню</h2>
      {menu.isLoading ? <Spinner label="Загружаем меню…" /> : null}
      {menu.isError ? <Notice>{isApiError(menu.error) && menu.error.code === "MENU_NOT_FOUND" ? "Меню временно недоступно." : errorText(menu.error)}</Notice> : null}
      {menu.data ? (
        <>
          <nav aria-label="Категории" className="sticky top-0 z-10 -mx-4 mb-4 flex gap-2 overflow-x-auto bg-paper px-4 py-2">
            {menu.data.categories.map((c) => (
              <a key={c.id} href={`#cat-${c.id}`} className="inline-flex min-h-[var(--lt-tap)] shrink-0 items-center rounded-full border-2 border-ink px-4 font-semibold">{c.name}</a>
            ))}
          </nav>
          {menu.data.categories.map((c) => (
            <section key={c.id} id={`cat-${c.id}`} className="mb-8 scroll-mt-16">
              <h3 className="mb-3 text-[length:var(--lt-fs-price)]">{c.name}</h3>
              <ul className="grid gap-4 sm:grid-cols-2">
                {c.items.map((it) => (
                  <li key={it.id} className="flex flex-col justify-between rounded-card bg-white p-5">
                    <div>
                      <h4 className={`font-display font-semibold ${it.available ? "" : "text-faint"}`}>{it.name}</h4>
                      {it.description ? <p className="mt-1 text-sm text-muted">{it.description}</p> : null}
                    </div>
                    <div className="mt-4 flex items-center justify-between gap-3">
                      <Price money={it.price} />
                      {it.available ? (
                        <Button variant="primary" aria-label={`Выбрать «${it.name}»`} onClick={() => setPicked(it)} className="!min-w-[var(--lt-tap)] !px-0">+</Button>
                      ) : (
                        <span className="rounded-full border-2 border-faint px-3 py-1 text-sm font-medium text-faint">Нет в наличии</span>
                      )}
                    </div>
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </>
      ) : null}

      {flash ? (
        <div role="status" className="fixed inset-x-4 bottom-4 z-40 mx-auto flex max-w-md items-center justify-between gap-3 rounded-full bg-ink px-5 py-3 text-white">
          <span>{flash}</span>
          <button className="rounded-full bg-lemon px-4 py-2 font-semibold text-ink" onClick={() => nav("/cart")}>Корзина</button>
        </div>
      ) : null}

      {picked ? <ItemDialog item={picked} busy={add.isPending} error={add.error} onClose={() => setPicked(null)} onAdd={(mods, qty) => request(picked, mods, qty)} /> : null}

      {switchTo ? (
        <Modal title="Начать новую корзину?" onClose={() => setSwitchTo(null)}>
          <p className="text-muted">В корзине блюда из другого ресторана. Чтобы заказать здесь, корзину придётся начать заново.</p>
          {add.error ? <Notice className="mt-3">{errorText(add.error)}</Notice> : null}
          <div className="mt-5 flex flex-wrap gap-2">
            <Button variant="accent" disabled={add.isPending} onClick={() => { cart.startNew(); add.mutate(switchTo); }}>Начать новую</Button>
            <Button variant="outline" onClick={() => setSwitchTo(null)}>Оставить прежнюю</Button>
          </div>
        </Modal>
      ) : null}
    </div>
  );
}

function ItemDialog({ item, onClose, onAdd, busy, error }: { item: MenuItem; onClose: () => void; onAdd: (mods: string[], qty: number) => void; busy: boolean; error: unknown }) {
  const [mods, setMods] = useState<Set<string>>(new Set());
  const [qty, setQty] = useState(1);
  // UC-MNU-02: предпросмотр на клиенте, истина — ответ сервера
  const unit = useMemo(() => item.price.amount + item.modifiers.filter((m) => mods.has(m.id)).reduce((s, m) => s + m.price.amount, 0), [item, mods]);
  const toggle = (id: string) => setMods((s) => { const n = new Set(s); if (n.has(id)) n.delete(id); else if (n.size < 20) n.add(id); return n; });
  return (
    <Modal title={item.name} onClose={onClose}>
      {item.description ? <p className="mb-3 text-muted">{item.description}</p> : null}
      {item.modifiers.length > 0 ? (
        <fieldset className="mb-4">
          <legend className="mb-2 font-semibold">Добавки</legend>
          <ul className="grid gap-2">
            {item.modifiers.map((m) => (
              <li key={m.id}>
                <label className={`flex min-h-[var(--lt-tap)] items-center gap-3 rounded-input border-2 border-ink px-4 ${m.available ? "" : "opacity-50"}`}>
                  <input type="checkbox" className="size-5 accent-[var(--lt-tomato)]" disabled={!m.available} checked={mods.has(m.id)} onChange={() => toggle(m.id)} />
                  <span className="flex-1">{m.name}{m.available ? "" : " — нет в наличии"}</span>
                  <span className="font-semibold">+{formatMoney(m.price.amount, m.price.currency)}</span>
                </label>
              </li>
            ))}
          </ul>
        </fieldset>
      ) : null}
      <div className="mb-4 flex items-center justify-between">
        <span className="font-semibold">Количество</span>
        <div className="flex items-center gap-2">
          <Button variant="outline" aria-label="Меньше" disabled={qty <= 1} onClick={() => setQty((q) => Math.max(1, q - 1))} className="!px-0 min-w-[var(--lt-tap)]">−</Button>
          <output aria-live="polite" className="w-8 text-center font-display font-semibold">{qty}</output>
          <Button variant="outline" aria-label="Больше" disabled={qty >= 99} onClick={() => setQty((q) => Math.min(99, q + 1))} className="!px-0 min-w-[var(--lt-tap)]">+</Button>
        </div>
      </div>
      {error ? <Notice className="mb-3">{errorText(error)}</Notice> : null}
      <Button variant="accent" cta className="w-full" disabled={busy} onClick={() => onAdd([...mods], qty)}>
        {busy ? "Добавляем…" : `В корзину · ${formatMoney(unit * qty, item.price.currency)}`}
      </Button>
    </Modal>
  );
}

function Hours({ r }: { r: { workingHours: import("@fp/api").WorkingHours[]; scheduleExceptions?: import("@fp/api").ScheduleException[] } }) {
  const rows = scheduleRows(r);
  const ex = upcomingExceptions(r);
  return (
    <details className="mt-4">
      <summary className="inline-flex min-h-[var(--lt-tap)] cursor-pointer items-center font-semibold underline">График работы</summary>
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
    <section aria-label="Акции" className="mb-6 grid gap-3 md:grid-cols-2">
      {list.map((p) => (
        <article key={p.id} className="rounded-card bg-tomato p-5 text-white">
          <p className="font-display text-xl font-semibold">
            {p.discount.type === "percent" ? `−${formatBasisPoints(p.discount.value)}` : `−${formatMoney(p.discount.value, p.currency ?? "RUB")}`}
          </p>
          <h3 className="mt-1 font-display font-semibold">{p.title}</h3>
          {p.description ? <p className="mt-1 text-sm text-white/90">{p.description}</p> : null}
          <p className="mt-2 text-sm text-white/90">
            {p.conditions?.minSubtotal ? `от ${formatMoney(p.conditions.minSubtotal, p.currency ?? "RUB")} · ` : ""}
            {p.conditions?.fulfillment === "pickup" ? "самовывоз · " : p.conditions?.fulfillment === "delivery" ? "доставка · " : ""}
            {fmt(p.startsAt)} – {fmt(p.endsAt)}
          </p>
        </article>
      ))}
    </section>
  );
}
