import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { db, R1, R2, R3, renderApp, resetWorld, server } from "./helpers";
import { settleMockPayment } from "../mocks/handlers";

// Карта (Leaflet) в jsdom не нужна: подменяем заглушкой, координаты вводятся полями
vi.mock("../components/LazyMap", () => ({ MapPicker: ({ label }: { label: string }) => <div role="application" aria-label={label} /> }));

beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
afterEach(() => server.resetHandlers());
afterAll(() => server.close());
beforeEach(() => resetWorld());

describe("каталог (UC-CAT)", () => {
  it("показывает рестораны без центра: по названию, без расстояний", async () => {
    renderApp("/");
    expect(await screen.findByRole("link", { name: /Лоток на Тверской/ })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Лоток у вокзала/ })).toHaveTextContent("Закрыто");
  });

  it("город из справочника ищет, неизвестный адрес даёт подсказку «Уточните адрес» (не ошибку)", async () => {
    const u = userEvent.setup();
    renderApp("/");
    await screen.findByRole("link", { name: /Невском/ });
    await u.type(screen.getByLabelText("Город или адрес"), "Нижний Новгород");
    await u.click(screen.getByRole("button", { name: "Найти" }));
    expect(await screen.findByText("Ничего не нашли")).toBeInTheDocument();
    expect(screen.getByText(/Уточните адрес/)).toBeInTheDocument();
  });

  it("deliveryOnly вне зоны доставки → подсказка pickup и кнопка снятия фильтра", async () => {
    renderApp(`/?lat=55.9&lon=37.6&radius=50000&deliveryOnly=1`);
    expect(await screen.findByText(/Доставка сюда не доходит/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Снять фильтр доставки" })).toBeInTheDocument();
  });

  it("меню: недоступная позиция без кнопки «+», с пометкой", async () => {
    renderApp(`/r/${R1}`);
    expect(await screen.findByText("Веган-бургер")).toBeInTheDocument();
    expect(screen.getAllByText("Нет в наличии").length).toBeGreaterThan(0);
    expect(screen.queryByRole("button", { name: "Выбрать «Веган-бургер»" })).toBeNull();
    expect(screen.getByRole("button", { name: "Выбрать «Бургер «Классика»»" })).toBeInTheDocument();
  });

  it("MENU_NOT_FOUND → «Меню временно недоступно»", async () => {
    renderApp(`/r/${R3}`);
    expect(await screen.findByText("Меню временно недоступно.")).toBeInTheDocument();
  });
});

async function addBurger(u: ReturnType<typeof userEvent.setup>) {
  await u.click(await screen.findByRole("button", { name: "Выбрать «Бургер «Классика»»" }));
  const dlg = await screen.findByRole("dialog");
  await u.click(within(dlg).getByLabelText(/Двойной сыр/));
  // предпросмотр: 249 + 50 = 299 ₽
  expect(within(dlg).getByRole("button", { name: /В корзину · 299/ })).toBeInTheDocument();
  await u.click(within(dlg).getByRole("button", { name: /В корзину/ }));
  await screen.findByText(/в корзине/);
}

describe("сквозной сценарий гостя: меню → корзина → заказ → оплата", () => {
  it("самовывоз + оплата картой онлайн", async () => {
    const u = userEvent.setup();
    const { router } = renderApp(`/r/${R1}`);
    await addBurger(u);
    await u.click(screen.getByRole("button", { name: "Корзина" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/cart"));

    expect(await screen.findByText("Бургер «Классика»")).toBeInTheDocument();
    // оформить нельзя, пока нет способа получения / оплаты / телефона гостя
    const place = screen.getByRole("button", { name: /Оформить заказ/ });
    expect(place).toBeDisabled();

    await u.click(screen.getByRole("radio", { name: "Самовывоз" }));
    await screen.findByText(/Выбрано: самовывоз/);
    await u.click(screen.getByRole("radio", { name: "Картой онлайн" }));
    expect(place).toBeDisabled();
    await u.type(screen.getByLabelText(/Телефон \(обязательно\)/), "8 999 123 45 67");
    await waitFor(() => expect(place).toBeEnabled());
    expect(place).toHaveTextContent(/299/);

    await u.click(place);
    await waitFor(() => expect(router.state.location.pathname).toMatch(/^\/orders\/.+/));
    expect(await screen.findByRole("heading", { level: 1, name: /Номер заказа R1-/ })).toBeInTheDocument();
    expect(screen.getAllByText("Ждёт оплаты").length).toBeGreaterThan(0);

    // кнопка оплаты появляется после payment-methods
    const pay = await screen.findByRole("button", { name: /Оплатить 299/ });
    await waitFor(() => expect(pay).toBeEnabled());
    const orderId = router.state.location.pathname.split("/").pop()!;
    const assign = vi.fn();
    Object.defineProperty(window, "location", { value: { ...window.location, origin: "http://localhost:3000", assign }, writable: true });
    await u.click(pay);
    await waitFor(() => expect(assign).toHaveBeenCalled());
    expect(String(assign.mock.calls[0]![0])).toContain("/mock-gateway?orderId=" + orderId);

    // банк подтвердил → заказ принят
    settleMockPayment(db, orderId, true);
    // опрос заказа раз в 4 с подхватывает оплату
    expect(await screen.findByText("Оплачено", {}, { timeout: 8000 })).toBeInTheDocument();
    expect(db.orders.get(orderId)!.order.status).toBe("accepted");
  }, 20000);
});

describe("оформление: коды ошибок → поведение экрана", () => {
  async function toCheckout(u: ReturnType<typeof userEvent.setup>, restaurant = R1) {
    renderApp(`/r/${restaurant}`);
    await addBurger(u);
    await u.click(screen.getByRole("button", { name: "Корзина" }));
    await screen.findByText("Бургер «Классика»");
  }

  it("ADDRESS_OUT_OF_ZONE: расстояние и радиус, «Заберу сам» переключает на самовывоз", async () => {
    const u = userEvent.setup();
    await toCheckout(u);
    await u.click(screen.getByRole("radio", { name: "Доставка" }));
    await u.type(screen.getByLabelText("Адрес"), "Дубна, ул. Дальняя 1");
    await u.clear(screen.getByLabelText("Широта"));
    await u.type(screen.getByLabelText("Широта"), "56.7");
    await u.clear(screen.getByLabelText("Долгота"));
    await u.type(screen.getByLabelText("Долгота"), "37.1");
    await u.click(screen.getByRole("button", { name: "Доставить сюда" }));
    expect(await screen.findByText(/зона доставки — 5000 м/)).toBeInTheDocument();
    await u.click(screen.getByRole("button", { name: "Заберу сам" }));
    expect(await screen.findByText(/Выбрано: самовывоз/)).toBeInTheDocument();
  });

  it("DELIVERY_NOT_AVAILABLE для ресторана без доставки", async () => {
    const u = userEvent.setup();
    renderApp(`/r/${R2}`);
    await u.click(await screen.findByRole("button", { name: "Выбрать «Сэндвич»" }));
    await u.click(within(await screen.findByRole("dialog")).getByRole("button", { name: /В корзину/ }));
    await screen.findByText(/в корзине/);
    await u.click(screen.getByRole("button", { name: "Корзина" }));
    await u.click(await screen.findByRole("radio", { name: "Доставка" }));
    await u.type(screen.getByLabelText("Адрес"), "Любой");
    await u.click(screen.getByRole("button", { name: "Доставить сюда" }));
    expect(await screen.findByText(/не доставляет/)).toBeInTheDocument();
  });

  it("PRICE_CHANGED: диалог «Цены обновились», подтверждение создаёт заказ с новым ключом", async () => {
    const u = userEvent.setup();
    await toCheckout(u);
    await u.click(screen.getByRole("radio", { name: "Самовывоз" }));
    await screen.findByText(/Выбрано: самовывоз/);
    await u.click(screen.getByRole("radio", { name: "На кассе" }));
    await u.type(screen.getByLabelText(/Телефон \(обязательно\)/), "+79991234567");
    db.priceChangeOnNextOrder = true;
    await u.click(screen.getByRole("button", { name: /Оформить заказ/ }));
    const dlg = await screen.findByRole("dialog", { name: "Цены обновились" });
    expect(db.orders.size).toBe(0);
    await u.click(within(dlg).getByRole("button", { name: "Подтвердить" }));
    await waitFor(() => expect(db.orders.size).toBe(1));
    expect(db.idem.size).toBe(2); // 4xx сохранён под первым ключом, второй заказ — под новым
  });

  it("RESTAURANT_CLOSED (409): заказ не создан, корзина цела", async () => {
    const u = userEvent.setup();
    renderApp(`/r/${R2}`);
    await u.click(await screen.findByRole("button", { name: "Выбрать «Сэндвич»" }));
    await u.click(within(await screen.findByRole("dialog")).getByRole("button", { name: /В корзину/ }));
    await screen.findByText(/в корзине/);
    await u.click(screen.getByRole("button", { name: "Корзина" }));
    await u.click(await screen.findByRole("radio", { name: "Самовывоз" }));
    await screen.findByText(/Выбрано: самовывоз/);
    await u.click(screen.getByRole("radio", { name: "На кассе" }));
    await u.type(screen.getByLabelText(/Телефон \(обязательно\)/), "+79991234567");
    await u.click(screen.getByRole("button", { name: /Оформить заказ/ }));
    expect(await screen.findByText(/Ресторан сейчас закрыт/)).toBeInTheDocument();
    expect(db.orders.size).toBe(0);
    expect(screen.getByText("Сэндвич")).toBeInTheDocument();
  });
});

describe("сессия (UC-ACC)", () => {
  it("неверный пароль → «Неверный телефон или пароль»", async () => {
    const u = userEvent.setup();
    renderApp("/login");
    await u.type(screen.getByLabelText("Телефон"), "+79990000001");
    await u.type(screen.getByLabelText("Пароль"), "wrongpass");
    await u.click(screen.getByRole("button", { name: "Войти" }));
    expect(await screen.findByText("Неверный телефон или пароль.")).toBeInTheDocument();
  });

  it("регистрация: занятый номер → ссылка «Войти»", async () => {
    const u = userEvent.setup();
    renderApp("/register");
    await u.type(screen.getByLabelText("Телефон"), "+79990000001");
    await u.type(screen.getByLabelText("Пароль"), "abcdef");
    await u.click(screen.getByRole("button", { name: "Зарегистрироваться" }));
    expect(await screen.findByText(/уже зарегистрирован/)).toBeInTheDocument();
  });

  it("вход → профиль; выход очищает сессию", async () => {
    const u = userEvent.setup();
    const { router, api } = renderApp("/login");
    await u.type(screen.getByLabelText("Телефон"), "+79990000001");
    await u.type(screen.getByLabelText("Пароль"), "secret1");
    await u.click(screen.getByRole("button", { name: "Войти" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/"));
    await u.click(await screen.findByRole("link", { name: "Профиль" }));
    expect(await screen.findByText("+79990000001")).toBeInTheDocument();
    await u.click(screen.getByRole("button", { name: "Выйти" }));
    await waitFor(() => expect(api.session.hasSession).toBe(false));
    expect(await screen.findByRole("link", { name: "Войти" })).toBeInTheDocument();
  });

  it("регистрация сразу выполняет вход (UC-ACC-03)", async () => {
    const u = userEvent.setup();
    const { api } = renderApp("/register");
    await u.type(screen.getByLabelText("Телефон"), "+79995550000");
    await u.type(screen.getByLabelText("Пароль"), "abcdef");
    await u.click(screen.getByRole("button", { name: "Зарегистрироваться" }));
    await waitFor(() => expect(api.session.hasSession).toBe(true));
    expect(db.users.has("+79995550000")).toBe(true);
  });
});

describe("заказ: терминальные и особые состояния", () => {
  it("неизвестный заказ → «Заказ не найден»", async () => {
    renderApp("/orders/00000000-0000-4000-8000-000000000000");
    expect(await screen.findByText("Заказ не найден")).toBeInTheDocument();
  });
});
