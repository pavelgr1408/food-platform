import { expect, test } from "@playwright/test";

// Сквозной сценарий гостя на мок-бэкенде: поиск → меню → корзина → самовывоз → онлайн-оплата → мок-шлюз → заказ принят.
test("гость: от поиска до оплаченного заказа", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("link", { name: /Лоток на Тверской/ })).toBeVisible();
  await page.getByRole("link", { name: /Лоток на Тверской/ }).click();

  await page.getByRole("button", { name: "Выбрать «Бургер «Классика»»" }).click();
  await page.getByRole("dialog").getByLabel(/Двойной сыр/).check();
  await page.getByRole("dialog").getByRole("button", { name: /В корзину · 299/ }).click();
  await page.getByRole("button", { name: "Корзина" }).last().click();

  await expect(page.getByRole("heading", { name: "Корзина" })).toBeVisible();
  await page.getByRole("radio", { name: "Самовывоз" }).check({ force: true });
  await expect(page.getByText(/Выбрано: самовывоз/)).toBeVisible();
  await page.getByRole("radio", { name: "Картой онлайн" }).check({ force: true });
  await page.getByLabel(/Телефон \(обязательно\)/).fill("8 999 123 45 67");
  await page.getByRole("button", { name: /Оформить заказ/ }).click();

  await expect(page.getByRole("heading", { level: 1 })).toContainText(/R1-\d{6}-\d{4}/);
  await page.getByRole("button", { name: /Оплатить 299/ }).click();

  // мок-шлюз банка → успех → возврат на страницу заказа
  await expect(page.getByRole("heading", { name: "Мок-шлюз оплаты" })).toBeVisible();
  await page.getByRole("button", { name: "Оплатить успешно" }).click();
  await expect(page.getByText("Оплачено")).toBeVisible({ timeout: 15_000 });

  // заказ попал в локальную историю
  await page.getByRole("link", { name: "Мои заказы" }).click();
  await expect(page.getByText(/R1-\d{6}-0001/)).toBeVisible();
});

test("отказ банка: можно оплатить снова", async ({ page }) => {
  await page.goto("/r/11111111-1111-4111-8111-111111111111");
  await page.getByRole("button", { name: "Выбрать «Картофель фри»" }).click();
  await page.getByRole("dialog").getByRole("button", { name: /В корзину/ }).click();
  await page.getByRole("button", { name: "Корзина" }).last().click();
  await page.getByRole("radio", { name: "Самовывоз" }).check({ force: true });
  await expect(page.getByText(/Выбрано: самовывоз/)).toBeVisible();
  await page.getByRole("radio", { name: "Картой онлайн" }).check({ force: true });
  await page.getByLabel(/Телефон \(обязательно\)/).fill("+79991234567");
  await page.getByRole("button", { name: /Оформить заказ/ }).click();
  await page.getByRole("button", { name: /Оплатить/ }).click();
  await page.getByRole("button", { name: "Отказ банка" }).click();
  await expect(page.getByText(/Банк отказал в оплате/)).toBeVisible({ timeout: 15_000 });
  await expect(page.getByRole("button", { name: /Оплатить/ })).toBeEnabled();
});

test("мобильная вёрстка: нет горизонтального скролла, цели нажатия ≥ 44 px", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("link", { name: /Лоток на Тверской/ })).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  const small = await page.evaluate(() =>
    [...document.querySelectorAll("button, a, input:not([type=range]), select")].filter((e) => {
      const r = (e as HTMLElement).getBoundingClientRect();
      return r.width > 0 && r.height > 0 && r.height < 44 && !(e as HTMLElement).closest("[hidden]") && getComputedStyle(e).display !== "inline";
    }).map((e) => (e as HTMLElement).outerHTML.slice(0, 80)));
  expect(small).toEqual([]);
});
