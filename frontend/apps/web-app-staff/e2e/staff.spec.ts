import { expect, test, type Page } from "@playwright/test";

async function login(page: Page, phone: string) {
  await page.goto("/");
  await page.getByLabel("Телефон").fill(phone);
  await page.getByLabel("Пароль").fill("secret1");
  await page.getByRole("button", { name: "Войти" }).click();
}

// Сквозной сценарий кассира на мок-бэкенде: вход → заказ по факсу → оплата на кассе → готов → выдача.
test("кассир: факс → оплата на кассе → выдача", async ({ page }) => {
  await login(page, "+79990000010");
  await expect(page.getByRole("heading", { name: "Касса" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Акции" })).toHaveCount(0);

  await page.getByRole("link", { name: "Заказ по факсу" }).click();
  await page.getByRole("button", { name: "Добавить «Чикен-ролл»" }).click();
  await page.getByRole("radio", { name: /Самовывоз/ }).check({ force: true });
  await expect(page.getByText(/Выбрано: самовывоз/)).toBeVisible();
  await page.getByLabel("Телефон (обязательно)").fill("8 999 555 01 01");
  await page.getByRole("button", { name: /Оформить заказ/ }).click();
  await expect(page.getByText("Заказ принят")).toBeVisible();
  const number = (await page.getByLabel(/Номер заказа R1-/).textContent())!.trim();

  await page.getByRole("link", { name: "Открыть заказ" }).click();
  await page.getByRole("button", { name: /Принять оплату/ }).click();
  const dlg = page.getByRole("dialog", { name: "Принять оплату" });
  await dlg.getByRole("radio", { name: /Наличными/ }).check({ force: true });
  await dlg.getByRole("button", { name: /^Принять/ }).click();
  await expect(page.getByText("Оплачен", { exact: true }).first()).toBeVisible({ timeout: 10_000 });

  // кухня (мок-production) довела заказ до «готов»
  await page.evaluate((n) => {
    const m = (window as unknown as { __mock: { db: { orders: Map<string, { order: { number: string; status: string } }> } } }).__mock;
    for (const o of m.db.orders.values()) if (o.order.number === n) o.order.status = "ready";
  }, number);
  const issue = page.getByRole("button", { name: "Выдать заказ" });
  await expect(issue).toBeEnabled({ timeout: 10_000 });
  await issue.click();
  await page.getByRole("dialog").getByRole("button", { name: "Выдать и закрыть" }).click();
  await expect(page.getByText(`Заказ ${number} выдан и закрыт.`)).toBeVisible();

  // поиск по номеру из очереди
  await page.getByRole("link", { name: "← Очередь" }).click();
  await page.getByLabel("Номер заказа").fill(number.toLowerCase());
  await page.getByRole("button", { name: "Найти заказ" }).click();
  await expect(page.getByRole("heading", { level: 1 })).toContainText(number);
});

test("владелец: акция — черновик → активация → завершение", async ({ page }) => {
  await login(page, "+79990000011");
  await page.getByRole("link", { name: "Акции" }).click();
  await page.getByRole("link", { name: "Новая акция" }).click();
  await page.getByLabel("Название").fill("E2E-акция");
  await page.getByLabel("Начало").fill("2026-11-01T09:00");
  await page.getByLabel("Окончание").fill("2027-02-01T09:00");
  await page.getByLabel("Размер, %").fill("15");
  await page.getByRole("button", { name: "Создать черновик" }).click();
  await expect(page.getByRole("heading", { name: "E2E-акция" })).toBeVisible();
  await page.getByRole("button", { name: "Активировать" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Активировать" }).click();
  await expect(page.getByText(/Акция активна/)).toBeVisible();
  await page.getByRole("button", { name: "Завершить досрочно" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Завершить" }).click();
  await expect(page.getByText("Акция завершена и больше не меняется.")).toBeVisible();
});
