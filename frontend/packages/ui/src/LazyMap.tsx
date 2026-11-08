import { lazy, Suspense } from "react";
import type { ComponentProps } from "react";

const Impl = lazy(() => import("./MapPicker").then((m) => ({ default: m.MapPicker })));
export type { LatLon } from "./MapPicker";

/** Leaflet тяжёлый — грузим только когда карта реально показана. */
export function MapPicker(props: ComponentProps<typeof Impl>) {
  return (
    <Suspense fallback={<div role="status" className="grid h-64 place-items-center rounded-tile border-2 border-ink text-muted">Загружаем карту…</div>}>
      <Impl {...props} />
    </Suspense>
  );
}
