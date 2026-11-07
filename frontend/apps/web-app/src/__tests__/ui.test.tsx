import { beforeAll, afterAll, afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { db, server, renderApp, resetWorld, R1, R2 } from "./helpers";
import { dishKind } from "../components/art";

vi.mock("../components/LazyMap", () => ({ MapPicker: ({ label }: { label: string }) => <div role="application" aria-label={label} /> }));

beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
afterEach(() => server.resetHandlers());
afterAll(() => server.close());
beforeEach(() => resetWorld());

describe("корзина: быстрые клики не теряют позиции", () => {
  it("три мгновенных «+» у блюда без добавок → количество 3 и одна корзина", async () => {
    renderApp(`/r/${R2}`);
    const add = await screen.findByRole("button", { name: "Добавить «Сэндвич»" });
    fireEvent.click(add);
    fireEvent.click(add);
    fireEvent.click(add);
    await waitFor(() => expect(screen.getByRole("group", { name: "Сэндвич" })).toHaveTextContent("3"));
    expect(db.carts.size).toBe(1);
    const cart = [...db.carts.values()][0]!.cart;
    expect(cart.items.map((i) => [i.menuItemId, i.quantity])).toEqual([["i-s", 3]]);
  });

  it("степпер в карточке: − до нуля убирает строку", async () => {
    const u = userEvent.setup();
    renderApp(`/r/${R2}`);
    await u.click(await screen.findByRole("button", { name: "Добавить «Сэндвич»" }));
    await u.click(await screen.findByRole("button", { name: "Меньше: Сэндвич" }));
    expect(await screen.findByRole("button", { name: "Добавить «Сэндвич»" })).toBeInTheDocument();
    expect([...db.carts.values()][0]!.cart.items).toHaveLength(0);
  });
});

describe("корзина: названия добавок", () => {
  it("вместо «Добавки: 1» показывает название", async () => {
    const u = userEvent.setup();
    renderApp(`/r/${R1}`);
    await u.click(await screen.findByRole("button", { name: "Выбрать «Бургер «Классика»»" }));
    const dlg = await screen.findByRole("dialog");
    await u.click(within(dlg).getByLabelText(/Двойной сыр/));
    await u.click(within(dlg).getByRole("button", { name: /В корзину/ }));
    await screen.findByText(/в корзине/);
    await u.click(screen.getByRole("button", { name: "Корзина" }));
    expect(await screen.findByText("Двойной сыр")).toBeInTheDocument();
    expect(screen.queryByText(/Добавок: 1/)).toBeNull();
  });
});

describe("диалог", () => {
  it("Tab не уводит фокус за пределы диалога, Escape закрывает", async () => {
    const u = userEvent.setup();
    renderApp(`/r/${R1}`);
    await u.click(await screen.findByRole("button", { name: "Выбрать «Бургер «Классика»»" }));
    const dlg = await screen.findByRole("dialog");
    for (let i = 0; i < 12; i++) { await u.tab(); expect(dlg.contains(document.activeElement)).toBe(true); }
    await u.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("фокус на чекбоксе не сбрасывается при обновлении родителя", async () => {
    const u = userEvent.setup();
    renderApp(`/r/${R1}`);
    await u.click(await screen.findByRole("button", { name: "Выбрать «Бургер «Классика»»" }));
    const dlg = await screen.findByRole("dialog");
    const cheese = within(dlg).getByLabelText(/Двойной сыр/);
    await u.click(cheese);
    expect(document.activeElement).toBe(cheese);
  });
});

describe("меню", () => {
  it("поиск по названию скрывает остальное и показывает пустую выдачу", async () => {
    const u = userEvent.setup();
    renderApp(`/r/${R1}`);
    await screen.findByText("Чизкейк «Нью-Йорк»");
    await u.type(screen.getByLabelText("Поиск по меню"), "капуч");
    expect(screen.queryByText("Чизкейк «Нью-Йорк»")).toBeNull();
    expect(screen.getByText("Капучино")).toBeInTheDocument();
    await u.clear(screen.getByLabelText("Поиск по меню"));
    await u.type(screen.getByLabelText("Поиск по меню"), "борщ");
    expect(await screen.findByText("Такого блюда нет")).toBeInTheDocument();
  });
});

describe("главная: координаты", () => {
  it("значение вне диапазона подсвечивается, запрос не уходит с неполными данными", async () => {
    const u = userEvent.setup();
    renderApp("/");
    await screen.findByRole("link", { name: /Лоток на Тверской/ });
    await u.click(screen.getByRole("button", { name: "На карте" }));
    await u.type(await screen.findByLabelText("Широта"), "95");
    expect(await screen.findByText("От -90 до 90")).toBeInTheDocument();
    expect(screen.getAllByText("Укажите и это значение").length).toBeGreaterThan(0);
    expect(screen.getByRole("link", { name: /Лоток на Тверской/ })).toBeInTheDocument();
  });
});

describe("иллюстрации: подбор по названию", () => {
  it.each([
    ["Бургер «Классика»", "burger"], ["Чикен-ролл", "roll"], ["Крылышки BBQ", "chicken"], ["Картофель фри", "fries"],
    ["Капучино", "coffee"], ["Лимонад", "drink"], ["Чизкейк «Нью-Йорк»", "dessert"], ["Салат «Цезарь»", "salad"],
    ["Суп-лапша", "soup"], ["Сэндвич", "sandwich"], ["Пепперони", "pizza"], ["Что-то новое", "plate"],
  ])("%s → %s", (name, kind) => { expect(dishKind(name)).toBe(kind); });
});

describe("диалоги и история заказов (регрессии ручного теста)", () => {
  it("диалог блюда рисуется порталом в body, а не внутри анимируемой страницы", async () => {
    const u = userEvent.setup();
    renderApp(`/r/${R1}`);
    await u.click(await screen.findByRole("button", { name: "Выбрать «Бургер «Классика»»" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog.parentElement?.parentElement).toBe(document.body);
  });

  it("заказ, которого нет на сервере (404), убирается из локальной истории", async () => {
    const { localOrders } = await import("../lib/local");
    localOrders.add({ id: "00000000-0000-4000-8000-0000000000aa", number: "R1-261107-0001", restaurantId: R1, createdAt: new Date().toISOString(), totalAmount: 29900, currency: "RUB" });
    renderApp("/orders");
    expect(await screen.findByText(/Убрано из истории/)).toBeInTheDocument();
    await waitFor(() => expect(localOrders.list()).toHaveLength(0));
    expect(screen.getByText("Заказов пока нет")).toBeInTheDocument();
  });
});

describe("смена ресторана и состояния заказа", () => {
  it("позиция из другого ресторана → диалог; «Начать новую» оставляет корзину только с новой позицией", async () => {
    const u = userEvent.setup();
    const { router } = renderApp(`/r/${R2}`);
    await u.click(await screen.findByRole("button", { name: "Добавить «Сэндвич»" }));
    await waitFor(() => expect(db.carts.size).toBe(1));
    await router.navigate(`/r/${R1}`);
    await u.click(await screen.findByRole("button", { name: "Выбрать «Бургер «Классика»»" }));
    await u.click(within(await screen.findByRole("dialog")).getByRole("button", { name: /В корзину/ }));
    const sw = await screen.findByRole("dialog", { name: "Начать новую корзину?" });
    await u.click(within(sw).getByRole("button", { name: "Начать новую" }));
    await waitFor(() => expect(db.carts.size).toBe(2));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const open = [...db.carts.values()].filter((c) => c.cart.status !== "ordered" && c.cart.items.length > 0);
    expect(open.map((c) => c.cart.restaurantId).sort()).toEqual([R1, R2].sort());
  });

  it.each([
    ["cancelled", "payment_timeout"], ["not_received", "pickup_timeout"],
  ] as const)("статус %s с причиной показывается на странице заказа", async (status, reason) => {
    const u = userEvent.setup();
    const { router } = renderApp(`/r/${R1}`);
    await u.click(await screen.findByRole("button", { name: "Выбрать «Картофель фри»" }));
    await u.click(within(await screen.findByRole("dialog")).getByRole("button", { name: /В корзину/ }));
    await waitFor(() => expect(db.carts.size).toBe(1));
    await router.navigate("/cart");
    await u.click(await screen.findByRole("radio", { name: "Самовывоз" }));
    await screen.findByText(/Выбрано: самовывоз/);
    await u.click(screen.getByRole("radio", { name: "Картой онлайн" }));
    await u.type(screen.getByLabelText(/Телефон \(обязательно\)/), "89991234567");
    const place = screen.getByRole("button", { name: /Оформить заказ/ });
    await waitFor(() => expect(place).toBeEnabled());
    await u.click(place);
    await waitFor(() => expect(router.state.location.pathname, document.body.textContent ?? "").toMatch(/^\/orders\/.+/));
    const m = [...db.orders.values()][0]!;
    m.order = { ...m.order, status, statusReason: reason } as typeof m.order;
    await router.navigate("/orders");
    expect(await screen.findByText(/R\d-\d{6}-\d{4}/)).toBeInTheDocument();
    await router.navigate(`/orders/${m.order.id}`);
    expect((await screen.findAllByText(status === "cancelled" ? "Отменён" : "Не получен")).length).toBeGreaterThan(0);
    expect((await screen.findAllByText(status === "cancelled" ? "Не оплачен вовремя" : "Заказ не забрали за 2 часа")).length).toBeGreaterThan(0);
  });
});

describe("баг-репорт 2026-11-07: UI", () => {
  it("B-03: внутренний statusReason не показывается пользователю", async () => {
    const u = userEvent.setup();
    const { router } = renderApp(`/r/${R1}`);
    await u.click(await screen.findByRole("button", { name: "Выбрать «Картофель фри»" }));
    await u.click(within(await screen.findByRole("dialog")).getByRole("button", { name: /В корзину/ }));
    await waitFor(() => expect(db.carts.size).toBe(1));
    await router.navigate("/cart");
    await u.click(await screen.findByRole("radio", { name: "Самовывоз" }));
    await screen.findByText(/Выбрано: самовывоз/);
    await u.click(screen.getByRole("radio", { name: "Картой онлайн" }));
    await u.type(screen.getByLabelText(/Телефон \(обязательно\)/), "89991234567");
    const place = screen.getByRole("button", { name: /Оформить заказ/ });
    await waitFor(() => expect(place).toBeEnabled());
    await u.click(place);
    await waitFor(() => expect(router.state.location.pathname).toMatch(/^\/orders\/.+/));
    const m = [...db.orders.values()][0]!;
    m.order = { ...m.order, status: "ready", paymentStatus: "paid", statusReason: "production" } as typeof m.order;
    await screen.findAllByText("Готов");
    await waitFor(() => expect(screen.queryByText("production")).toBeNull());
  });

  it("B-05: выход очищает историю заказов, платежи и контакты", async () => {
    const { localOrders, localContact, localPayments, clearDeviceData } = await import("../lib/local");
    localOrders.add({ id: "x1", number: "R1-1", restaurantId: R1, createdAt: new Date().toISOString(), totalAmount: 100, currency: "RUB" });
    localContact.set({ phone: "+79990000001" });
    localPayments.set("x1", "p1");
    clearDeviceData();
    expect(localOrders.list()).toEqual([]);
    expect(localContact.get()).toEqual({});
    expect(localPayments.get("x1")).toBeNull();
  });

  it("B-12: пароль из пробелов отклоняется с понятным сообщением", async () => {
    const u = userEvent.setup();
    renderApp("/register");
    await u.type(await screen.findByLabelText("Телефон"), "+79990000009");
    await u.type(screen.getByLabelText("Пароль"), "      ");
    await u.click(screen.getByRole("button", { name: "Зарегистрироваться" }));
    expect(await screen.findByText(/одних пробелов/)).toBeInTheDocument();
    expect(db.users.has("+79990000009")).toBe(false);
  });

  it("B-16: город не дублируется, если адрес уже начинается с него", async () => {
    const { fullAddress } = await import("../lib/format");
    expect(fullAddress({ city: "Москва", address: "Москва, Тверская ул., 12" })).toBe("Москва, Тверская ул., 12");
    expect(fullAddress({ city: "Москва", address: "ул. Тверская, 7" })).toBe("Москва, ул. Тверская, 7");
  });

  it("B-09: серия быстрых кликов по «Оплатить» создаёт один платёж", async () => {
    const u = userEvent.setup();
    const { router } = renderApp(`/r/${R1}`);
    await u.click(await screen.findByRole("button", { name: "Выбрать «Картофель фри»" }));
    await u.click(within(await screen.findByRole("dialog")).getByRole("button", { name: /В корзину/ }));
    await waitFor(() => expect(db.carts.size).toBe(1));
    await router.navigate("/cart");
    await u.click(await screen.findByRole("radio", { name: "Самовывоз" }));
    await screen.findByText(/Выбрано: самовывоз/);
    await u.click(screen.getByRole("radio", { name: "Картой онлайн" }));
    await u.type(screen.getByLabelText(/Телефон \(обязательно\)/), "89991234567");
    const place = screen.getByRole("button", { name: /Оформить заказ/ });
    await waitFor(() => expect(place).toBeEnabled());
    let orderPosts = 0;
    const onOrder = ({ request }: { request: Request }) => { if (request.method === "POST" && new URL(request.url).pathname.endsWith("/orders")) orderPosts++; };
    server.events.on("request:start", onOrder);
    fireEvent.click(place); fireEvent.click(place); fireEvent.click(place);
    const pay = await screen.findByRole("button", { name: /Оплатить \d/ });
    server.events.removeListener("request:start", onOrder);
    expect(orderPosts).toBe(1);
    await waitFor(() => expect(pay).toBeEnabled());
    let posts = 0;
    const on = ({ request }: { request: Request }) => { if (request.method === "POST" && /\/payments$/.test(new URL(request.url).pathname)) posts++; };
    server.events.on("request:start", on);
    fireEvent.click(pay); fireEvent.click(pay); fireEvent.click(pay);
    await waitFor(() => expect([...db.orders.values()][0]!.attempt).toBeGreaterThan(0));
    server.events.removeListener("request:start", on);
    expect(posts).toBe(1);
  });
});
