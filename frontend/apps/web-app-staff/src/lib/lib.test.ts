import { describe, expect, it } from "vitest";
import { parsePercent, percentInput, phaseOf, rublesInput, utcToZoned, zonedToUtcIso } from "./promo";
import { canTakePosPayment, cancelVerdict, isSettled, ORDER_NUMBER } from "./order";

describe("promo", () => {
  it("процент: сотые доли без float", () => {
    expect(parsePercent("15")).toBe(1500);
    expect(parsePercent("12,5")).toBe(1250);
    expect(parsePercent("0,01")).toBe(1);
    expect(parsePercent("100")).toBe(10000);
    expect(parsePercent("100,01")).toBeNull();
    expect(parsePercent("0")).toBeNull();
    expect(parsePercent("abc")).toBeNull();
    expect(percentInput(1250)).toBe("12,5");
    expect(rublesInput(9990)).toBe("99,90");
  });
  it("время в поясе ресторана ↔ UTC", () => {
    expect(zonedToUtcIso("2026-11-01T09:00", "Europe/Moscow")).toBe("2026-11-01T06:00:00.000Z");
    expect(utcToZoned("2026-11-01T06:00:00.000Z", "Europe/Moscow")).toBe("2026-11-01T09:00");
    expect(zonedToUtcIso("", "Europe/Moscow")).toBeNull();
  });
  it("фаза выводится из статуса и окна", () => {
    const now = Date.parse("2026-11-08T12:00:00Z");
    const w = { startsAt: "2026-11-01T00:00:00Z", endsAt: "2026-12-01T00:00:00Z" };
    expect(phaseOf({ status: "draft", ...w }, now)).toBe("draft");
    expect(phaseOf({ status: "active", ...w }, now)).toBe("running");
    expect(phaseOf({ status: "active", startsAt: "2026-12-01T00:00:00Z", endsAt: "2027-01-01T00:00:00Z" }, now)).toBe("scheduled");
    expect(phaseOf({ status: "active", startsAt: "2026-01-01T00:00:00Z", endsAt: "2026-02-01T00:00:00Z" }, now)).toBe("expired");
    expect(phaseOf({ status: "completed", ...w }, now)).toBe("completed");
  });
});

describe("order", () => {
  it("номер заказа", () => {
    expect(ORDER_NUMBER.test("R1-261108-0007")).toBe(true);
    expect(ORDER_NUMBER.test("r1-261108-0007")).toBe(true);
    expect(ORDER_NUMBER.test("abc")).toBe(false);
  });
  it("оплата на кассе только для pos и неоплаченного", () => {
    expect(canTakePosPayment({ paymentMethod: "pos", paymentStatus: "pending", status: "accepted" })).toBe(true);
    expect(canTakePosPayment({ paymentMethod: "pos", paymentStatus: "paid", status: "accepted" })).toBe(false);
    expect(canTakePosPayment({ paymentMethod: "online_card", paymentStatus: "pending", status: "accepted" })).toBe(false);
    expect(canTakePosPayment({ paymentMethod: "pos", paymentStatus: "pending", status: "cancelled" })).toBe(false);
  });
  it("отмена и завершённость", () => {
    expect(cancelVerdict({ status: "accepted" }).ok).toBe(true);
    expect(cancelVerdict({ status: "cooking" }).ok).toBe(false);
    expect(isSettled({ status: "cancelled", paymentStatus: "pending", refundStatus: undefined })).toBe(true);
    expect(isSettled({ status: "cancelled", paymentStatus: "paid", refundStatus: "pending" })).toBe(false);
    expect(isSettled({ status: "accepted", paymentStatus: "paid", refundStatus: undefined })).toBe(false);
  });
});
