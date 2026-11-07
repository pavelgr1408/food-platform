import type { RestaurantDetails, WorkingHours } from "@fp/api";

export const DAY_NAMES = ["", "Понедельник", "Вторник", "Среда", "Четверг", "Пятница", "Суббота", "Воскресенье"];

export interface DayRow { day: number; name: string; intervals: string[] }

/** UC-CAT-09: интервалов в дне может быть несколько (перерыв). 24:00 и ночной 02:00 показываем как есть. */
export function scheduleRows(r: Pick<RestaurantDetails, "workingHours">): DayRow[] {
  const byDay = new Map<number, WorkingHours[]>();
  for (const w of r.workingHours) byDay.set(w.dayOfWeek, [...(byDay.get(w.dayOfWeek) ?? []), w]);
  return [1, 2, 3, 4, 5, 6, 7].map((d) => {
    const intervals = (byDay.get(d) ?? []).sort((a, b) => a.opensAt.localeCompare(b.opensAt)).map((w) => `${w.opensAt}–${w.closesAt}`);
    return { day: d, name: DAY_NAMES[d] ?? "", intervals };
  });
}

export function upcomingExceptions(r: Pick<RestaurantDetails, "scheduleExceptions">, today = new Date()) {
  const todayIso = today.toISOString().slice(0, 10);
  const byDate = new Map<string, string[]>();
  const notes = new Map<string, string>();
  for (const e of (r.scheduleExceptions ?? []).filter((x) => x.date >= todayIso)) {
    byDate.set(e.date, e.closed ? ["закрыто"] : [...(byDate.get(e.date) ?? []), `${e.opensAt}–${e.closesAt}`]);
    if (e.note) notes.set(e.date, e.note);
  }
  return [...byDate.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([date, text]) => ({ date, text: text.join(", "), note: notes.get(date) }));
}
