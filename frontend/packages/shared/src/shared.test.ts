import { describe, expect, it } from "vitest";
import { formatBasisPoints, formatMoney, isE164, normalizePhone, parseRubles, passwordProblem } from "./index";

const N = " ";
describe("formatMoney", () => {
  it("целые рубли без копеек", () => expect(formatMoney(34900)).toBe(`349${N}₽`));
  it("копейки, если ненулевые", () => expect(formatMoney(54990)).toBe(`549,90${N}₽`));
  it("ведущий ноль копеек", () => expect(formatMoney(10005)).toBe(`100,05${N}₽`));
  it("ноль", () => expect(formatMoney(0)).toBe(`0${N}₽`));
  it("тысячи", () => expect(formatMoney(123456789)).toContain("1"));
  it("float запрещён", () => expect(() => formatMoney(10.5)).toThrow(RangeError));
});
describe("parseRubles", () => {
  it.each([["549,90", 54990], ["549.9", 54990], ["349", 34900], [" 1 ", 100]] as const)("%s", (i, o) => expect(parseRubles(i)).toBe(o));
  it.each(["", "1,234", "abc", "-5", "1e3"])("отвергает %s", (i) => expect(parseRubles(i)).toBeNull());
});
describe("formatBasisPoints", () => {
  it("15 %", () => expect(formatBasisPoints(1500)).toBe(`15${N}%`));
  it("12,5 %", () => expect(formatBasisPoints(1250)).toBe(`12,5${N}%`));
});
describe("phone", () => {
  it("нормализация", () => expect(normalizePhone("+7 (999) 123-45-67")).toBe("+79991234567"));
  it("8 → +7", () => expect(normalizePhone("8 999 123 45 67")).toBe("+79991234567"));
  it("E.164", () => { expect(isE164("+79991234567")).toBe(true); expect(isE164("+0123456789")).toBe(false); expect(isE164("79991234567")).toBe(false); });
  it("пароль по байтам", () => {
    expect(passwordProblem("12345")).toBe("short");
    expect(passwordProblem("a".repeat(72))).toBeNull();
    expect(passwordProblem("я".repeat(37))).toBe("long"); // 74 байта
  });
});
