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
export const agoSeconds = (iso: string, now = Date.now()) => Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
