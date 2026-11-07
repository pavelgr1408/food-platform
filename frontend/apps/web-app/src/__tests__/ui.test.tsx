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
