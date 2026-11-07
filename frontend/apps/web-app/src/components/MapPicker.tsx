import { useEffect, useRef } from "react";
import L from "leaflet";
import iconUrl from "leaflet/dist/images/marker-icon.png";
import iconRetinaUrl from "leaflet/dist/images/marker-icon-2x.png";
import shadowUrl from "leaflet/dist/images/marker-shadow.png";

// бандлеры ломают автоопределение путей иконок Leaflet
L.Icon.Default.mergeOptions({ iconUrl, iconRetinaUrl, shadowUrl });

export interface LatLon { lat: number; lon: number }

/**
 * Карта-пин (UC-CAT-01, UC-FUL-03). Тайлы OSM — внешний сервис: без сети карта пустая,
 * поэтому рядом всегда есть ручной ввод координат. Круг — зона доставки/радиус поиска.
 */
export function MapPicker({ value, onChange, radiusMeters, markers, label }: {
  value: LatLon | null; onChange?: (p: LatLon) => void; radiusMeters?: number;
  markers?: { id: string; name: string; at: LatLon }[]; label: string;
}) {
  const el = useRef<HTMLDivElement>(null);
  const map = useRef<L.Map | null>(null);
  const pin = useRef<L.Marker | null>(null);
  const circle = useRef<L.Circle | null>(null);
  const layer = useRef<L.LayerGroup | null>(null);
  const cb = useRef(onChange);
  cb.current = onChange;

  useEffect(() => {
    if (!el.current || map.current) return;
    const m = L.map(el.current, { center: value ? [value.lat, value.lon] : [55.7558, 37.6173], zoom: value ? 13 : 10 });
    L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", { attribution: "© OpenStreetMap", maxZoom: 19 }).addTo(m);
    layer.current = L.layerGroup().addTo(m);
    m.on("click", (e: L.LeafletMouseEvent) => cb.current?.({ lat: +e.latlng.lat.toFixed(6), lon: +e.latlng.lng.toFixed(6) }));
    map.current = m;
    return () => { m.remove(); map.current = null; pin.current = null; circle.current = null; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const m = map.current;
    if (!m) return;
    if (value) {
      const ll: L.LatLngExpression = [value.lat, value.lon];
      if (!pin.current) {
        pin.current = L.marker(ll, { draggable: !!cb.current, title: "Точка" }).addTo(m);
        pin.current.on("dragend", () => {
          const p = pin.current!.getLatLng();
          cb.current?.({ lat: +p.lat.toFixed(6), lon: +p.lng.toFixed(6) });
        });
      } else pin.current.setLatLng(ll);
      if (radiusMeters) {
        if (!circle.current) circle.current = L.circle(ll, { radius: radiusMeters, color: "#D62A16", weight: 2, fillOpacity: 0.08 }).addTo(m);
        else { circle.current.setLatLng(ll); circle.current.setRadius(radiusMeters); }
      } else if (circle.current) { circle.current.remove(); circle.current = null; }
      m.setView(ll, m.getZoom());
    }
  }, [value, radiusMeters]);

  useEffect(() => {
    layer.current?.clearLayers();
    for (const mk of markers ?? []) L.circleMarker([mk.at.lat, mk.at.lon], { radius: 8, color: "#12131A", fillColor: "#FFD43B", fillOpacity: 1, weight: 2 }).bindTooltip(mk.name).addTo(layer.current!);
  }, [markers]);

  return <div ref={el} role="application" aria-label={label} className="h-64 w-full overflow-hidden rounded-tile border-2 border-ink" />;
}
