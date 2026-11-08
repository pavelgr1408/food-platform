import type { Promotion, PromotionInput } from "@fp/api";
import { formatBasisPoints, formatMoney, parseRubles } from "@fp/shared";

/* ---------------- Жизненный цикл: в БД draft → active → completed; остальное выводит клиент ---------------- */
export type Phase = "draft" | "scheduled" | "running" | "expired" | "completed";

/**
 * «Запланирована» и «истекла» в БД не хранятся: по сроку акция сама не завершается (FRONTEND-CONTRACT, раздел акций).
 * Скидка применяется к заказам только в окне [startsAt, endsAt), пока статус active.
 */
export function phaseOf(p: Pick<Promotion, "status" | "startsAt" | "endsAt">, now = Date.now()): Phase {
  if (p.status === "draft") return "draft";
  if (p.status === "completed") return "completed";
  if (now < Date.parse(p.startsAt)) return "scheduled";
  if (now >= Date.parse(p.endsAt)) return "expired";
  return "running";
}
export const PHASE_LABEL: Record<Phase, string> = { draft: "Черновик", scheduled: "Запланирована", running: "Действует", expired: "Срок истёк", completed: "Завершена" };
export const PHASE_TONE: Record<Phase, "neutral" | "ok" | "warn" | "hot"> = { draft: "neutral", scheduled: "warn", running: "ok", expired: "hot", completed: "neutral" };

export function discountText(d: { type: "percent" | "fixed"; value: number }, currency = "RUB"): string {
  return d.type === "percent" ? `−${formatBasisPoints(d.value)}` : `−${formatMoney(d.value, currency)}`;
}

/* ---------------- Ввод скидки: проценты ↔ сотые доли процента (1500 = 15 %), рубли ↔ копейки ---------------- */
/** «15» → 1500, «12,5» → 1250, «0,01» → 1; больше двух знаков, мусор, вне 0,01…100 → null. Деньги и проценты — только целые (ADR-13). */
export function parsePercent(input: string): number | null {
  const m = /^\s*(\d{1,3})(?:[.,](\d{1,2}))?\s*$/.exec(input);
  if (!m) return null;
  const bp = Number(m[1]) * 100 + (m[2] ? Number(m[2].padEnd(2, "0")) : 0);
  return bp >= 1 && bp <= 10_000 ? bp : null;
}
export const percentInput = (bp: number): string => {
  const whole = Math.trunc(bp / 100), frac = bp % 100;
  return frac === 0 ? String(whole) : `${whole},${String(frac).padStart(2, "0").replace(/0$/, "")}`;
};
export const rublesInput = (kop: number): string => {
  const rub = Math.trunc(kop / 100), k = kop % 100;
  return k === 0 ? String(rub) : `${rub},${String(k).padStart(2, "0")}`;
};

/* ---------------- Время: поле вводится в поясе ресторана, на сервер уходит UTC ---------------- */
function offsetMs(tz: string, ts: number): number {
  const parts = new Intl.DateTimeFormat("en-US", { timeZone: tz, hourCycle: "h23", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" }).formatToParts(new Date(ts));
  const g = (t: string) => Number(parts.find((p) => p.type === t)?.value);
  return Date.UTC(g("year"), g("month") - 1, g("day"), g("hour"), g("minute"), g("second")) - Math.floor(ts / 1000) * 1000;
}

/** «2026-11-10T09:00» в поясе `tz` → ISO UTC. Пустое или битое значение → null. */
export function zonedToUtcIso(local: string, tz: string): string | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(local);
  if (!m) return null;
  const asUtc = Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]), Number(m[4]), Number(m[5]));
  if (Number.isNaN(asUtc)) return null;
  try {
    let utc = asUtc - offsetMs(tz, asUtc);
    utc = asUtc - offsetMs(tz, utc); // второй проход: смещение в целевой момент (переход на летнее/зимнее время)
    return new Date(utc).toISOString();
  } catch { return null; }
}

/** ISO UTC → «2026-11-10T09:00» в поясе `tz` (значение для input[type=datetime-local]). */
export function utcToZoned(iso: string, tz: string): string {
  const ts = Date.parse(iso);
  if (Number.isNaN(ts)) return "";
  try {
    const z = new Date(ts + offsetMs(tz, ts)).toISOString();
    return z.slice(0, 16);
  } catch { return ""; }
}

export function fmtPeriod(startsAt: string, endsAt: string, tz?: string): string {
  const f = new Intl.DateTimeFormat("ru-RU", { day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit", timeZone: tz });
  return `${f.format(new Date(startsAt))} — ${f.format(new Date(endsAt))}`;
}

/* ---------------- Форма акции ---------------- */
export interface PromoForm {
  title: string; description: string;
  scope: "local" | "national"; restaurantId: string;
  startsLocal: string; endsLocal: string;
  discountType: "percent" | "fixed"; discountValue: string;
  menuItemIds: string[]; minSubtotal: string; fulfillment: "" | "pickup" | "delivery"; stackable: boolean;
  priority: string;
}
export const emptyForm = (restaurantId: string): PromoForm => ({
  title: "", description: "", scope: "local", restaurantId, startsLocal: "", endsLocal: "", discountType: "percent", discountValue: "",
  menuItemIds: [], minSubtotal: "", fulfillment: "", stackable: false, priority: "0",
});

export function formFromPromotion(p: Promotion, tz: string): PromoForm {
  return {
    title: p.title, description: p.description ?? "", scope: p.scope, restaurantId: p.restaurantId ?? "",
    startsLocal: utcToZoned(p.startsAt, tz), endsLocal: utcToZoned(p.endsAt, tz),
    discountType: p.discount.type, discountValue: p.discount.type === "percent" ? percentInput(p.discount.value) : rublesInput(p.discount.value),
    menuItemIds: p.conditions?.menuItemIds ?? [], minSubtotal: p.conditions?.minSubtotal ? rublesInput(p.conditions.minSubtotal) : "",
    fulfillment: p.conditions?.fulfillment ?? "", stackable: p.conditions?.stackable ?? false, priority: String(p.priority),
  };
}

export type FormErrors = Partial<Record<"title" | "description" | "restaurantId" | "startsLocal" | "endsLocal" | "discountValue" | "minSubtotal" | "priority", string>>;

/** Клиентская проверка зеркалит сервер (PROMOTION_INVALID); последнее слово всё равно за сервером. */
export function buildInput(f: PromoForm, tz: string): { input: PromotionInput; errors: null } | { input: null; errors: FormErrors } {
  const errors: FormErrors = {};
  const title = f.title.trim();
  if (title.length < 1 || title.length > 120) errors.title = "От 1 до 120 символов";
  if (f.description.length > 1000) errors.description = "Не более 1000 символов";
  if (f.scope === "local" && !f.restaurantId) errors.restaurantId = "Выберите ресторан";
  const startsAt = zonedToUtcIso(f.startsLocal, tz), endsAt = zonedToUtcIso(f.endsLocal, tz);
  if (!startsAt) errors.startsLocal = "Укажите начало";
  if (!endsAt) errors.endsLocal = "Укажите окончание";
  if (startsAt && endsAt && Date.parse(endsAt) <= Date.parse(startsAt)) errors.endsLocal = "Окончание должно быть позже начала";
  let value: number | null = null;
  if (f.discountType === "percent") {
    value = parsePercent(f.discountValue);
    if (value === null) errors.discountValue = "Процент от 0,01 до 100, не больше двух знаков после запятой";
  } else {
    value = parseRubles(f.discountValue);
    if (value === null || value < 1 || value > 1e10) { errors.discountValue = "Сумма больше 0, не больше двух знаков после запятой"; value = null; }
  }
  let minSubtotal: number | undefined;
  if (f.minSubtotal.trim()) {
    const m = parseRubles(f.minSubtotal);
    if (m === null || m > 1e10) errors.minSubtotal = "Сумма в рублях, не больше двух знаков после запятой"; else minSubtotal = m;
  }
  const priority = Number(f.priority);
  if (!/^\d{1,4}$/.test(f.priority.trim()) || priority < 0 || priority > 1000) errors.priority = "Целое от 0 до 1000";
  if (Object.keys(errors).length || !startsAt || !endsAt || value === null) return { input: null, errors };
  return {
    errors: null,
    input: {
      title, ...(f.description.trim() ? { description: f.description.trim() } : {}),
      scope: f.scope, ...(f.scope === "local" ? { restaurantId: f.restaurantId } : {}),
      discount: { type: f.discountType, value }, currency: "RUB", startsAt, endsAt, priority,
      conditions: {
        ...(f.scope === "local" && f.menuItemIds.length ? { menuItemIds: f.menuItemIds } : {}),
        ...(minSubtotal !== undefined ? { minSubtotal } : {}),
        ...(f.fulfillment ? { fulfillment: f.fulfillment } : {}), stackable: f.stackable,
      },
    },
  };
}

/** Условия одной строкой для списка. */
export function conditionsText(p: Pick<Promotion, "conditions">): string {
  const c = p.conditions;
  const parts: string[] = [];
  if (c?.minSubtotal) parts.push(`от ${formatMoney(c.minSubtotal)}`);
  if (c?.fulfillment) parts.push(c.fulfillment === "pickup" ? "самовывоз" : "доставка");
  if (c?.menuItemIds?.length) parts.push(`позиций: ${c.menuItemIds.length}`);
  parts.push(c?.stackable ? "суммируется" : "не суммируется");
  return parts.join(" · ");
}
