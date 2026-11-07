/** Деньги — только целые копейки (ADR-13). Форматирование — единственная функция на весь фронт (DESIGN-SYSTEM §5). */

const SYMBOLS: Record<string, string> = { RUB: "₽", USD: "$", EUR: "€" };
const NBSP = " ";

const intFmt = new Intl.NumberFormat("ru-RU", { maximumFractionDigits: 0 });

/** 54990 → «549,90 ₽»; 34900 → «349 ₽»; копейки показываются, только если ненулевые. */
export function formatMoney(amount: number, currency = "RUB"): string {
  if (!Number.isSafeInteger(amount)) throw new RangeError(`money amount must be an integer, got ${amount}`);
  const sign = amount < 0 ? "−" : "";
  const abs = Math.abs(amount);
  const rub = Math.trunc(abs / 100);
  const kop = abs % 100;
  const body = kop === 0 ? intFmt.format(rub) : `${intFmt.format(rub)},${String(kop).padStart(2, "0")}`;
  return `${sign}${body}${NBSP}${SYMBOLS[currency] ?? currency}`;
}

/** Ввод рублей → копейки: «549,90» / «549.9» / «349» → целое. Дробь длиннее 2 знаков и мусор → null. */
export function parseRubles(input: string): number | null {
  const m = /^\s*(\d{1,9})(?:[.,](\d{1,2}))?\s*$/.exec(input);
  if (!m) return null;
  const rub = Number(m[1]);
  const kop = m[2] ? Number(m[2].padEnd(2, "0")) : 0;
  return rub * 100 + kop;
}

/** Сотые доли процента (1500) → «15 %»; 1250 → «12,5 %». */
export function formatBasisPoints(bp: number): string {
  if (!Number.isSafeInteger(bp)) throw new RangeError("basis points must be an integer");
  const whole = Math.trunc(bp / 100);
  const frac = bp % 100;
  const s = frac === 0 ? String(whole) : `${whole},${String(frac).padStart(2, "0").replace(/0$/, "")}`;
  return `${s}${NBSP}%`;
}
