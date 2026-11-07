/** UC-ACC-01: E.164 — «+» и 8–15 цифр, первая не 0. */
export const E164 = /^\+[1-9][0-9]{7,14}$/;

/** Убирает пробелы, скобки, дефисы; «8…» и «7…» для РФ приводит к «+7…». */
export function normalizePhone(raw: string): string {
  const hasPlus = raw.trim().startsWith("+");
  let digits = raw.replace(/\D/g, "");
  if (!hasPlus && digits.length === 11 && digits.startsWith("8")) digits = "7" + digits.slice(1);
  if (!hasPlus && digits.length === 10) digits = "7" + digits;
  return digits ? `+${digits}` : "";
}

export const isE164 = (v: string): boolean => E164.test(v);

/** UC-ACC-02: пароль 6–72 символа, но bcrypt режет по БАЙТАМ (длинный не-ASCII даёт 500). */
export function passwordProblem(p: string): "short" | "long" | "blank" | null {
  if (p.length < 6) return "short";
  if (p.trim() === "") return "blank";
  if (new TextEncoder().encode(p).length > 72) return "long";
  return null;
}
