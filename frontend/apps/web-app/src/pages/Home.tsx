import { useMemo, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useInfiniteQuery } from "@tanstack/react-query";
import { isApiError, type RestaurantPage, type RestaurantSearchParams, type RestaurantSummary } from "@fp/api";
import { useApi } from "../app/api-context";
import { Badge, Button, Empty, Field, Notice, Skeleton, Surface } from "../components/ui";
import { Cover, imageOf } from "../components/art";
import { MapPicker, type LatLon } from "../components/LazyMap";
import { errorText } from "../lib/errors";

const PAGE = 20;
const CITIES = ["Москва", "Санкт-Петербург", "Казань", "Екатеринбург"];
const km = (m: number) => (m >= 1000 ? `${(m / 1000).toFixed(1).replace(".0", "")} км` : `${m} м`);
const inRange = (v: string, lo: number, hi: number) => v.trim() !== "" && Number.isFinite(+v.replace(",", ".")) && +v.replace(",", ".") >= lo && +v.replace(",", ".") <= hi;

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
  const [latDraft, setLatDraft] = useState(lat ?? "");
  const [lonDraft, setLonDraft] = useState(lon ?? "");
  const [geoErr, setGeoErr] = useState<string | null>(null);
  const [locating, setLocating] = useState(false);
  const [showMap, setShowMap] = useState(!!center);
  const addrRef = useRef<HTMLInputElement>(null);

  const patch = (changes: Record<string, string | null>) => {
    const next = new URLSearchParams(sp);
    for (const [k, v] of Object.entries(changes)) {
      if (v === null || v === "") next.delete(k); else next.set(k, v);
    }
    setSp(next, { replace: true });
  };
  const setCenter = (p: LatLon) => {
    setLatDraft(String(p.lat)); setLonDraft(String(p.lon));
    patch({ lat: String(p.lat), lon: String(p.lon), address: null });
  };
  // Координаты применяются целиком и только когда оба значения верны: пока человек печатает «5», запрос не уходит.
  const applyCoords = () => {
    if (inRange(latDraft, -90, 90) && inRange(lonDraft, -180, 180)) patch({ lat: latDraft.replace(",", "."), lon: lonDraft.replace(",", "."), address: null });
  };
  const coordErr = (v: string, lo: number, hi: number, other: string) =>
    v && !inRange(v, lo, hi) ? `От ${lo} до ${hi}` : !v && other ? "Укажите и это значение" : null;

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
  const total = q.data?.pages[0]?.total;
  const hint = q.data?.pages[0]?.hint;

  const locate = () => {
    setGeoErr(null);
    if (!navigator.geolocation) return setGeoErr("Браузер не поддерживает геолокацию. Выберите город или поставьте точку на карте.");
    setLocating(true);
    navigator.geolocation.getCurrentPosition(
      (p) => { setLocating(false); setShowMap(true); setCenter({ lat: +p.coords.latitude.toFixed(6), lon: +p.coords.longitude.toFixed(6) }); },
      () => { setLocating(false); setGeoErr("Не удалось определить местоположение. Выберите город или поставьте точку на карте."); },
      { timeout: 8000 },
    );
  };
  const searchAddress = (v: string) => { setAddrDraft(v); patch({ address: v.trim() || null, lat: null, lon: null }); setLatDraft(""); setLonDraft(""); };
  const reset = () => { setAddrDraft(""); setLatDraft(""); setLonDraft(""); setSp(new URLSearchParams(), { replace: true }); };
  const filtered = !!(center || address || deliveryOnly || openNow);

  return (
    <div className="lt-rise">
      <section className="relative mb-5 overflow-hidden rounded-card bg-tomato p-5 text-white md:p-8">
        <div aria-hidden className="pointer-events-none absolute -right-10 -top-10 size-48 rounded-full bg-lemon/30" />
        <div aria-hidden className="pointer-events-none absolute -bottom-16 right-24 size-40 rounded-full bg-white/10" />
        <h1 className="relative max-w-xl text-[length:var(--lt-fs-h2)] leading-tight md:text-[length:var(--lt-fs-hero)]">Закажи еду быстро</h1>
        <p className="relative mt-2 max-w-md text-white/90">Рестораны рядом: заберите заказ сами или попросите курьера.</p>
        <form className="relative mt-4 flex max-w-xl flex-col gap-2 sm:flex-row" role="search" onSubmit={(e) => { e.preventDefault(); searchAddress(addrDraft); }}>
          <Field ref={addrRef} label="Город или адрес" value={addrDraft} onChange={(e) => setAddrDraft(e.target.value)} placeholder="Москва" className="flex-1 [&_label]:text-white [&_input]:border-white [&_input]:text-ink" />
          <div className="flex gap-2 sm:self-end">
            <Button type="submit" variant="accent" className="flex-1 sm:flex-none">Найти</Button>
            <Button variant="outline" className="flex-1 !border-white !text-white hover:!bg-white/10 sm:flex-none" onClick={locate} disabled={locating}>{locating ? "Определяем…" : "Где я"}</Button>
          </div>
        </form>
        <div className="relative mt-3 flex flex-wrap gap-2" role="group" aria-label="Быстрый выбор города">
          {CITIES.map((c) => (
            <button key={c} type="button" onClick={() => searchAddress(c)} aria-pressed={address === c}
              className={`min-h-11 rounded-full border-2 border-white/70 px-4 text-sm font-semibold ${address === c ? "bg-white text-tomato" : "text-white hover:bg-white/10"}`}>{c}</button>
          ))}
        </div>
        {geoErr ? <Notice tone="info" className="relative mt-3 max-w-xl">{geoErr}</Notice> : null}
      </section>

      <div className="mb-4 flex flex-wrap items-center gap-2" role="group" aria-label="Фильтры">
        <Chip on={deliveryOnly} onClick={() => patch({ deliveryOnly: deliveryOnly ? null : "1" })}>С доставкой</Chip>
        <Chip on={openNow} onClick={() => patch({ openNow: openNow ? null : "1" })}>Открыто сейчас</Chip>
        <Chip on={showMap} onClick={() => setShowMap((v) => !v)}>На карте</Chip>
        {filtered ? <button type="button" onClick={reset} className="min-h-11 px-3 text-sm font-semibold underline">Сбросить</button> : null}
        {total !== undefined && q.isSuccess ? <span className="ml-auto text-sm text-muted" aria-live="polite">Найдено: {total}</span> : null}
      </div>

      {showMap ? (
        <Surface className="mb-5 grid gap-3">
          <MapPicker label="Карта поиска. Нажмите, чтобы поставить точку" value={center} radiusMeters={center ? radius : undefined}
            markers={items.map((r) => ({ id: r.id, name: r.name, at: r.location }))} onChange={setCenter} />
          <div className="grid grid-cols-2 gap-3">
            <Field label="Широта" inputMode="decimal" value={latDraft} onChange={(e) => setLatDraft(e.target.value)} onBlur={applyCoords} onKeyDown={(e) => e.key === "Enter" && applyCoords()} error={coordErr(latDraft, -90, 90, lonDraft)} />
            <Field label="Долгота" inputMode="decimal" value={lonDraft} onChange={(e) => setLonDraft(e.target.value)} onBlur={applyCoords} onKeyDown={(e) => e.key === "Enter" && applyCoords()} error={coordErr(lonDraft, -180, 180, latDraft)} />
          </div>
          <div>
            <label htmlFor="radius" className="mb-1 block text-sm font-semibold">Радиус поиска: {km(radiusDraft)}</label>
            <input id="radius" type="range" min={100} max={50000} step={100} value={radiusDraft} disabled={!center} className="h-12 w-full accent-[var(--lt-tomato)] disabled:opacity-40"
              onChange={(e) => setRadiusDraft(+e.target.value)} onPointerUp={() => patch({ radius: String(radiusDraft) })} onKeyUp={() => patch({ radius: String(radiusDraft) })} />
            {!center ? <p className="text-sm text-muted">Поставьте точку на карте — тогда можно задать радиус.</p> : null}
          </div>
        </Surface>
      ) : null}

      {q.isLoading ? (
        <ul className="grid gap-4 md:grid-cols-2" aria-label="Загружаем рестораны" aria-busy="true">
          {[0, 1, 2, 3].map((i) => <li key={i}><Skeleton className="h-64 rounded-card" /></li>)}
        </ul>
      ) : null}
      {q.isError ? (
        <Notice>{errorText(q.error)} <button className="font-semibold underline" onClick={() => q.refetch()}>Повторить</button></Notice>
      ) : null}
      {q.isError && isApiError(q.error) && q.error.code === "COORDINATES_INCOMPLETE" ? <p className="mt-2 text-sm text-muted">Заполните и широту, и долготу.</p> : null}

      {q.isSuccess && items.length === 0 ? (
        <Empty title="Ничего не нашли">
          {hint === "pickup" ? <p>Доставка сюда не доходит, но рядом есть рестораны для самовывоза.</p> : null}
          {hint === "expand_radius" ? <p>Увеличьте радиус поиска.</p> : null}
          {hint === "change_address" || !hint ? <p>Уточните адрес или поставьте точку на карте.</p> : null}
          <div className="mt-4 flex flex-wrap justify-center gap-2">
            {hint === "pickup" ? <Button variant="accent" onClick={() => patch({ deliveryOnly: null })}>Снять фильтр доставки</Button> : null}
            {hint === "expand_radius" ? <Button variant="accent" onClick={() => { const r = Math.min(50000, radius * 2); setRadiusDraft(r); patch({ radius: String(r) }); }}>Увеличить радиус</Button> : null}
            {hint === "change_address" ? <Button variant="outline" onClick={() => { addrRef.current?.focus(); addrRef.current?.scrollIntoView({ block: "center", behavior: "smooth" }); }}>Изменить адрес</Button> : null}
          </div>
        </Empty>
      ) : null}

      <ul className="grid gap-4 md:grid-cols-2" aria-label="Рестораны">
        {items.map((r) => <RestaurantCard key={r.id} r={r} />)}
      </ul>
      {q.hasNextPage ? (
        <div className="mt-6 text-center">
          <Button variant="outline" onClick={() => q.fetchNextPage()} disabled={q.isFetchingNextPage}>{q.isFetchingNextPage ? "Загружаем…" : "Показать ещё"}</Button>
        </div>
      ) : null}
    </div>
  );
}

function RestaurantCard({ r }: { r: RestaurantSummary }) {
  const photo = imageOf(r);
  return (
    <li>
      <Link to={`/r/${r.id}`} className="group block overflow-hidden rounded-card bg-white shadow-[0_1px_0_var(--lt-border),0_10px_28px_-16px_rgba(18,19,26,0.28)] transition-transform hover:-translate-y-0.5 motion-reduce:transition-none">
        <div className="relative h-36 overflow-hidden">
          {photo ? <img src={photo} alt="" loading="lazy" className="size-full object-cover" /> : <Cover name={r.name} seed={r.id} className={`h-full w-full ${r.openNow ? "" : "grayscale"}`} />}
          <Badge tone={r.openNow ? "ok" : "neutral"} className={`absolute left-3 top-3 ${r.openNow ? "" : "bg-white"}`}>{r.openNow ? "Открыто" : "Закрыто"}</Badge>
          {r.distanceMeters !== undefined ? <Badge className="absolute right-3 top-3 bg-white">{km(r.distanceMeters)}</Badge> : null}
        </div>
        <div className="p-4">
          <h3 className="text-[length:var(--lt-fs-price)]">{r.name}</h3>
          <p className="mt-0.5 text-muted">{r.city}, {r.address}</p>
          <p className="mt-3 flex flex-wrap gap-2 text-sm">
            <Badge tone={r.deliveryAvailable ? "warn" : "neutral"}>{r.deliveryAvailable ? "Есть доставка" : "Только самовывоз"}</Badge>
          </p>
        </div>
      </Link>
    </li>
  );
}

function Chip({ on, onClick, children }: { on: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button type="button" aria-pressed={on} onClick={onClick}
      className={`min-h-11 rounded-full border-2 px-4 font-semibold transition-colors ${on ? "border-ink bg-ink text-white" : "border-ink/25 bg-white hover:border-ink"}`}>
      {children}
    </button>
  );
}
