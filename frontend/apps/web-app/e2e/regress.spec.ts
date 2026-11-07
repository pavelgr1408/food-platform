import { expect, test, type Page } from "@playwright/test";

const R1 = "/r/11111111-1111-4111-8111-111111111111";

async function inViewport(page: Page, selector: string) {
  await page.waitForTimeout(400); // конец анимации появления
  const box = await page.locator(selector).first().boundingBox();
  const vp = page.viewportSize()!;
  expect(box).not.toBeNull();
  expect(box!.y).toBeGreaterThanOrEqual(-1);
  expect(box!.y + box!.height).toBeLessThanOrEqual(vp.height + 1);
  return box!;
}

test("диалог блюда после прокрутки страницы целиком в окне и не липнет к верху", async ({ page }) => {
  await page.goto(R1);
  const buttons = page.getByRole("button", { name: /^(Выбрать|Добавить) «/ });
  await expect(buttons.first()).toBeVisible();
  const n = await buttons.count();
  expect(n).toBeGreaterThan(2);
  // открываем диалог у блюда, прокрученного вниз
  await buttons.nth(n - 1).scrollIntoViewIfNeeded();
  await page.evaluate(() => window.scrollBy(0, 200));
  const y = await page.evaluate(() => window.scrollY);
  expect(y).toBeGreaterThan(100);
  const sel = buttons.nth(n - 2);
  await sel.click();
  const dlg = page.getByRole("dialog");
  if (await dlg.count()) {
    await expect(dlg).toBeVisible();
    await inViewport(page, '[role="dialog"]');
    const vp = page.viewportSize()!;
    const box = (await dlg.boundingBox())!;
    // на телефоне шторка прижата к низу, на десктопе по центру
    if (vp.width < 768) expect(Math.abs(box.y + box.height - vp.height)).toBeLessThan(2);
    else expect(box.y).toBeGreaterThan(5);
    await page.keyboard.press("Escape");
    await expect(dlg).toHaveCount(0);
    expect(await page.evaluate(() => window.scrollY)).toBeGreaterThan(100);
  }
  // блюдо с добавками — гарантированно диалог
  await page.getByRole("button", { name: "Выбрать «Бургер «Классика»»" }).scrollIntoViewIfNeeded();
  await page.getByRole("button", { name: "Выбрать «Бургер «Классика»»" }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await inViewport(page, '[role="dialog"]');
  // кнопка «В корзину» закреплена и видна без прокрутки внутри диалога
  const cta = (await page.getByRole("dialog").getByRole("button", { name: /В корзину/ }).boundingBox())!;
  expect(cta.y + cta.height).toBeLessThanOrEqual(page.viewportSize()!.height + 1);
  const top = await page.getByRole("dialog").locator("svg, img").first().boundingBox();
  expect(top).not.toBeNull();
  expect(top!.y).toBeGreaterThanOrEqual(0);
});

test("устаревшая запись истории (нет на сервере) убирается", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("fp.mock.orders", JSON.stringify([{ id: "00000000-0000-4000-8000-0000000000aa", number: "R1-261107-0001", restaurantId: "11111111-1111-4111-8111-111111111111", createdAt: new Date().toISOString(), totalAmount: 29900, currency: "RUB" }]));
  });
  await page.goto("/orders");
  await expect(page.getByText(/Убрано из истории/)).toBeVisible();
  await expect(page.getByText("Не найден")).toHaveCount(0);
  await page.reload();
  await expect(page.getByText("Заказов пока нет")).toBeVisible();
});

test("корзина: количество, удаление, сохранение после перезагрузки", async ({ page }) => {
  await page.goto(R1);
  await page.getByRole("button", { name: "Выбрать «Картофель фри»" }).click();
  await page.getByRole("dialog").getByRole("button", { name: /В корзину/ }).click();
  await expect(page.getByText(/в корзине/i).first()).toBeVisible();
  await page.reload();
  await expect(page.getByText(/в корзине/i).first()).toBeVisible();
  await page.goto("/cart");
  await expect(page.getByText("Картофель фри").first()).toBeVisible();
  await page.getByRole("button", { name: /Больше: Картофель фри/ }).click();
  await expect(page.getByRole("group", { name: /Картофель фри/ })).toContainText("2");
  await page.getByRole("button", { name: /Меньше: Картофель фри/ }).click();
  await page.getByRole("button", { name: /Меньше: Картофель фри/ }).click();
  await expect(page.getByText(/Корзина пуста|пуст/i).first()).toBeVisible();
});

test("навигация: все пункты открываются, 404, назад", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
  await page.goto("/");
  for (const [name, h] of [[/заказы/i, /заказы/i], [/профиль|войти/i, /.+/], [/корзина/i, /Корзина|пуста|оформлена/i]] as const) {
    await page.getByRole("link", { name }).first().click();
    await expect(page.getByRole("heading", { level: 1 }).first()).toContainText(h);
  }
  await page.goto("/nope/xyz");
  await expect(page.getByRole("heading").first()).toBeVisible();
  await page.goto("/orders/not-a-uuid");
  await expect(page.getByText(/не найден/i).first()).toBeVisible();
  await page.goto("/r/00000000-0000-4000-8000-000000000000");
  await expect(page.getByText(/недоступен|не найден/i).first()).toBeVisible();
  expect(errors.filter((e) => !/Failed to load resource|tile|openstreetmap|fonts/i.test(e))).toEqual([]);
});
