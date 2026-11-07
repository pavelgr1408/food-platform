/** Время приходит в UTC (RFC 3339); показываем в поясе ресторана (FRONTEND-CONTRACT, «Соглашения»). */
export function fmtDateTime(iso: string | undefined, tz?: string): string {
  if (!iso) return "—";
  try {
    return new Intl.DateTimeFormat("ru-RU", { day: "numeric", month: "long", hour: "2-digit", minute: "2-digit", timeZone: tz }).format(new Date(iso));
  } catch {
    return new Date(iso).toLocaleString("ru-RU");
  }
}
export function fmtTime(iso: string | undefined, tz?: string): string {
  if (!iso) return "—";
  try {
    return new Intl.DateTimeFormat("ru-RU", { hour: "2-digit", minute: "2-digit", timeZone: tz }).format(new Date(iso));
  } catch {
    return new Date(iso).toLocaleTimeString("ru-RU");
  }
}
/** В данных ресторана address уже может начинаться с города — не дублируем его. */
export function fullAddress(r: { city?: string; address?: string }): string {
  const a = (r.address ?? "").trim(), c = (r.city ?? "").trim();
  if (!c || a.toLowerCase().startsWith(c.toLowerCase())) return a || c;
  return a ? `${c}, ${a}` : c;
}
export const agoSeconds = (iso: string, now = Date.now()) => Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
