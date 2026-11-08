import { useEffect, useState, type ReactNode } from "react";
import { Badge, cx } from "@fp/ui";
import type { Tone } from "../lib/order";

/** Текущее время для подписей «N мин назад»: перерисовка раз в `ms`. */
export function useNow(ms = 30_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => { const t = setInterval(() => setNow(Date.now()), ms); return () => clearInterval(t); }, [ms]);
  return now;
}

export function ago(iso: string, now: number): string {
  const min = Math.max(0, Math.round((now - Date.parse(iso)) / 60_000));
  if (min < 1) return "только что";
  if (min < 60) return `${min} мин назад`;
  const h = Math.floor(min / 60);
  return h < 24 ? `${h} ч ${min % 60} мин назад` : `${Math.floor(h / 24)} дн назад`;
}

export const ToneBadge = ({ tone, children }: { tone: Tone; children: ReactNode }) => <Badge tone={tone}>{children}</Badge>;

/** Подпись к значению: dl-пара в карточке заказа. */
export function KV({ k, children, className }: { k: string; children: ReactNode; className?: string }) {
  return (
    <div className={cx("flex justify-between gap-4 py-1.5", className)}>
      <dt className="text-muted">{k}</dt>
      <dd className="text-right font-medium">{children}</dd>
    </div>
  );
}
