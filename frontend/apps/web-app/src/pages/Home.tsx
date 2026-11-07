import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useInfiniteQuery } from "@tanstack/react-query";
import { isApiError, type RestaurantPage, type RestaurantSearchParams } from "@fp/api";
import { useApi } from "../app/api-context";
import { Button, Empty, Field, Notice, Spinner, Surface } from "../components/ui";
import { MapPicker, type LatLon } from "../components/LazyMap";
import { errorText } from "../lib/errors";

const PAGE = 20;

/** UC-CAT-01…07: центр поиска (геолокация/пин/город), фильтры, радиус, список и пустая выдача по hint. */
export function Home() {
  const api = useApi();
  const [sp, setSp] = useSearchParams();
  const lat = sp.get("lat"), lon = sp.get("lon");
  const center: LatLon | null = lat && lon ? { lat: +lat, lon: +lon } : null;
  const address = sp.get("address") ?? "";
  const deliveryOnly = sp.get("deliveryOnly") === "1";
  const openNow = sp.get("openNow") === "1";
  const radius = Number(sp.get("radius") ?? 5000);

  const [addrDraft, setAddrDraft] = useState(address);
  const [radiusDraft, setRadiusDraft] = useState(radius);
  const [geoErr, setGeoErr] = useState<string | null>(null);
  const [showMap, setShowMap] = useState(!!center);

  const patch = (changes: Record<string, string | null>) => {
    const next = new URLSearchParams(sp);
    for (const [k, v] of Object.entries(changes)) {
      if (v === null || v === "") next.delete(k); else next.set(k, v);
    }
    setSp(next, { replace: true });
  };

  const params: RestaurantSearchParams = useMemo(() => ({
    lat: center?.lat, lon: center?.lon,
    address: center ? undefined : address || undefined,
    deliveryOnly: deliveryOnly || undefined, openNow: openNow || undefined,
    radiusMeters: center ? radius : undefined, limit: PAGE,
  }), [center?.lat, center?.lon, address, deliveryOnly, openNow, radius]); // eslint-disable-line react-hooks/exhaustive-deps

  const q = useInfiniteQuery({
    queryKey: ["restaurants", params],
    queryFn: ({ pageParam, signal }) => api.restaurants.search({ ...params, offset: pageParam }, signal),
    initialPageParam: 0,
    getNextPageParam: (last: RestaurantPage) => (last.offset + last.items.length < last.total ? last.offset + last.limit : undefined),
  });
  const items = q.data?.pages.flatMap((p) => p.items) ?? [];
  const hint = q.data?.pages[0]?.hint;

  const locate = () => {
    setGeoErr(null);
    if (!navigator.geolocation) return setGeoErr("Браузер не поддерживает геолокацию. Поставьте точку на карте или введите город.");
    navigator.geolocation.getCurrentPosition(
      (p) => { setShowMap(true); patch({ lat: p.coords.latitude.toFixed(6), lon: p.coords.longitude.toFixed(6), address: null }); },
      () => setGeoErr("Не удалось определить местоположение. Поставьте точку на карте или введите город."),
      { timeout: 8000 },
    );
  };

  return (
    <div className="lt-rise">
      <section className="mb-6 rounded-card bg-tomato p-6 text-white md:p-10">
        <h1 className="max-w-xl text-[length:var(--lt-fs-h2)] leading-tight md:text-[length:var(--lt-fs-hero)]">Закажи еду быстро</h1>
        <p className="mt-3 max-w-md text-white/90">Выберите ресторан рядом и заберите заказ сами или попросите курьера.</p>
      </section>

      <Surface className="mb-6 grid gap-4">
        <h2 className="text-[length:var(--lt-fs-price)]">Где искать</h2>
        <form className="grid gap-3 md:grid-cols-[1fr_auto_auto]" onSubmit={(e) => { e.preventDefault(); patch({ address: addrDraft.trim() || null, lat: null, lon: null }); }}>
          <Field label="Город или адрес" value={addrDraft} onChange={(e) => setAddrDraft(e.target.value)} placeholder="Москва" hint="Знаем Москву, Санкт-Петербург, Казань и Екатеринбург. Точный адрес — через карту." />
          <Button type="submit" variant="primary" className="self-end">Найти</Button>
          <Button variant="outline" className="self-end" onClick={locate}>Где я</Button>
        </form>
        {geoErr ? <Notice tone="info">{geoErr}</Notice> : null}

        <div className="flex flex-wrap gap-2" role="group" aria-label="Фильтры">
          <Chip on={deliveryOnly} onClick={() => patch({ deliveryOnly: deliveryOnly ? null : "1" })}>Только с доставкой</Chip>
          <Chip on={openNow} onClick={() => patch({ openNow: openNow ? null : "1" })}>Открыто сейчас</Chip>
          <Chip on={showMap} onClick={() => setShowMap((v) => !v)}>Точка на карте</Chip>
        </div>

        {showMap ? (
          <div className="grid gap-3">
            <MapPicker label="Карта поиска. Нажмите, чтобы поставить точку" value={center} radiusMeters={center ? radius : undefined}
              markers={items.map((r) => ({ id: r.id, name: r.name, at: r.location }))}
              onChange={(p) => patch({ lat: String(p.lat), lon: String(p.lon), address: null })} />
            <div className="grid grid-cols-2 gap-3">
              <Field label="Широта" inputMode="decimal" value={lat ?? ""} onChange={(e) => patch({ lat: e.target.value })} error={lat && !lon ? "Укажите и долготу" : null} />
              <Field label="Долгота" inputMode="decimal" value={lon ?? ""} onChange={(e) => patch({ lon: e.target.value })} error={lon && !lat ? "Укажите и широту" : null} />
            </div>
            <div>
              <label htmlFor="radius" className="mb-1 block text-sm font-semibold">Радиус: {radiusDraft >= 1000 ? `${radiusDraft / 1000} км` : `${radiusDraft} м`}</label>
              <input id="radius" type="range" min={100} max={50000} step={100} value={radiusDraft} disabled={!center} className="h-12 w-full accent-[var(--lt-tomato)]"
                onChange={(e) => setRadiusDraft(+e.target.value)} onPointerUp={() => patch({ radius: String(radiusDraft) })} onKeyUp={() => patch({ radius: String(radiusDraft) })} />
            </div>
          </div>
        ) : null}
      </Surface>

      {q.isLoading ? <Spinner label="Ищем рестораны…" /> : null}
      {q.isError ? (
        <Notice>
          {isApiError(q.error) && q.error.code === "COORDINATES_INCOMPLETE" ? errorText(q.error) : errorText(q.error)}{" "}
          <button className="font-semibold underline" onClick={() => q.refetch()}>Повторить</button>
        </Notice>
      ) : null}

      {q.isSuccess && items.length === 0 ? (
        <Empty title="Ничего не нашли">
          {hint === "pickup" ? <p>Доставка сюда не доходит, но рядом есть рестораны для самовывоза.</p> : null}
          {hint === "expand_radius" ? <p>Увеличьте радиус поиска.</p> : null}
          {hint === "change_address" || !hint ? <p>Уточните адрес или поставьте точку на карте.</p> : null}
          <div className="mt-4 flex flex-wrap justify-center gap-2">
            {hint === "pickup" ? <Button variant="accent" onClick={() => patch({ deliveryOnly: null })}>Снять фильтр доставки</Button> : null}
            {hint === "expand_radius" ? <Button variant="accent" onClick={() => { setRadiusDraft(Math.min(50000, radius * 2)); patch({ radius: String(Math.min(50000, radius * 2)) }); }}>Увеличить радиус</Button> : null}
            {hint === "change_address" ? <Button variant="outline" onClick={() => document.getElementById("radius")?.focus()}>Изменить адрес</Button> : null}
          </div>
        </Empty>
      ) : null}

      <ul className="grid gap-4 md:grid-cols-2" aria-label="Рестораны">
        {items.map((r) => (
          <li key={r.id}>
            <Link to={`/r/${r.id}`} className="block rounded-card bg-white p-5 transition-transform hover:-translate-y-0.5 motion-reduce:transition-none">
              <div className="flex items-start justify-between gap-3">
                <h3 className="text-[length:var(--lt-fs-price)]">{r.name}</h3>
                <span className={`shrink-0 rounded-full px-3 py-1 text-sm font-semibold ${r.openNow ? "bg-mint text-ink" : "border-2 border-ink text-ink"}`}>{r.openNow ? "Открыто" : "Закрыто"}</span>
              </div>
              <p className="mt-1 text-muted">{r.city}, {r.address}</p>
              <p className="mt-3 flex flex-wrap gap-2 text-sm">
                {r.distanceMeters !== undefined ? <span className="rounded-full bg-paper px-3 py-1 font-medium">{r.distanceMeters >= 1000 ? `${(r.distanceMeters / 1000).toFixed(1)} км` : `${r.distanceMeters} м`}</span> : null}
                <span className="rounded-full bg-paper px-3 py-1 font-medium">{r.deliveryAvailable ? "Есть доставка" : "Самовывоз"}</span>
              </p>
            </Link>
          </li>
        ))}
      </ul>
      {q.hasNextPage ? (
        <div className="mt-6 text-center">
          <Button variant="outline" onClick={() => q.fetchNextPage()} disabled={q.isFetchingNextPage}>{q.isFetchingNextPage ? "Загружаем…" : "Показать ещё"}</Button>
        </div>
      ) : null}
    </div>
  );
}

function Chip({ on, onClick, children }: { on: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button type="button" aria-pressed={on} onClick={onClick}
      className={`min-h-[var(--lt-tap)] rounded-full border-2 border-ink px-4 font-semibold ${on ? "bg-tomato text-white border-tomato" : "bg-transparent"}`}>
      {children}
    </button>
  );
}
