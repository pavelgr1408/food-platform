import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PHONES, R1, db, faxOrder, renderApp, resetWorld, server } from "./helpers";

beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
afterAll(() => server.close());
beforeEach(resetWorld);
afterEach(() => server.resetHandlers());

const heading = (name: string | RegExp) => screen.findByRole("heading", { name });

describe("вход и доступ", () => {
  it("гость уходит на экран входа; неверный пароль объясняется; кассир попадает на кассу", async () => {
    const user = userEvent.setup();
    await renderApp("/");
    await heading("Вход на кассу");
    await user.type(screen.getByLabelText("Телефон"), PHONES.staff);
    await user.type(screen.getByLabelText("Пароль"), "wrong-pass");
    await user.click(screen.getByRole("button", { name: "Войти" }));
    expect(await screen.findByText("Неверный телефон или пароль.")).toBeInTheDocument();
    await user.clear(screen.getByLabelText("Пароль"));
    await user.type(screen.getByLabelText("Пароль"), "secret1");
    await user.click(screen.getByRole("button", { name: "Войти" }));
    await heading("Касса");
    expect(screen.getByRole("link", { name: "Заказ по факсу" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Акции" })).not.toBeInTheDocument(); // кассир акциями не управляет
  });

  it("покупатель видит отказ и может выйти", async () => {
    await renderApp("/", PHONES.customer);
    expect(await heading("Нет доступа")).toBeInTheDocument();
    expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
  });

  it("сотрудник без привязки к ресторанам: «Нет доступа к ресторанам»", async () => {
    db.users.set("+79990000020", { id: "u-lonely", phone: "+79990000020", password: "secret1", roles: ["STAFF"], restaurants: [] });
    await renderApp("/", "+79990000020");
    expect(await heading("Нет доступа к ресторанам")).toBeInTheDocument();
  });

  it("кассир на /promotions видит отказ, а не 404", async () => {
    await renderApp("/promotions", PHONES.staff);
    expect(await heading("Раздел недоступен")).toBeInTheDocument();
  });

  it("выход очищает сессию и возвращает на вход", async () => {
    const user = userEvent.setup();
    await renderApp("/", PHONES.staff);
    await heading("Касса");
    await user.click(screen.getByRole("button", { name: "Выйти" }));
    await heading("Вход на кассу");
  });
});

describe("касса: очередь и поиск (UC-STF-01, UC-STF-10)", () => {
  it("показывает входящие, счётчики вкладок, переключает вкладки", async () => {
    const o = await faxOrder();
    const cooking = await faxOrder({ item: "i-fries" });
    db.orders.get(cooking.id)!.order.status = "cooking";
    const user = userEvent.setup();
    await renderApp("/", PHONES.staff);
    expect(await screen.findByText(o.number)).toBeInTheDocument();
    expect(screen.queryByText(cooking.number)).not.toBeInTheDocument();
    expect(await screen.findByRole("tab", { name: /Входящие/ })).toHaveTextContent("1");
    await user.click(screen.getByRole("tab", { name: /Готовятся/ }));
    expect(await screen.findByText(cooking.number)).toBeInTheDocument();
    await user.click(screen.getByRole("tab", { name: /Готовы/ }));
    expect(await screen.findByText("Готовых заказов нет.")).toBeInTheDocument();
  });

  it("поиск по номеру в любом регистре открывает карточку с контактами; неверный формат и чужой номер — понятные ошибки", async () => {
    const o = await faxOrder();
    const user = userEvent.setup();
    await renderApp("/", PHONES.staff);
    await heading("Касса");
    await user.type(screen.getByLabelText("Номер заказа"), "abc");
    await user.click(screen.getByRole("button", { name: "Найти заказ" }));
    expect(await screen.findByText(/Номер не по формату/)).toBeInTheDocument();
    await user.clear(screen.getByLabelText("Номер заказа"));
    await user.type(screen.getByLabelText("Номер заказа"), "R1-000000-0001");
    await user.click(screen.getByRole("button", { name: "Найти заказ" }));
    expect(await screen.findByText(/Заказ не найден/)).toBeInTheDocument();
    await user.clear(screen.getByLabelText("Номер заказа"));
    await user.type(screen.getByLabelText("Номер заказа"), ` ${o.number.toLowerCase()} `);
    await user.click(screen.getByRole("button", { name: "Найти заказ" }));
    await heading(`Заказ ${o.number}`);
    expect(screen.getByText("+79995550101")).toBeInTheDocument();
    expect(screen.getByText("Анна")).toBeInTheDocument();
  });

  it("пустая очередь — подсказка, а не пустота", async () => {
    await renderApp("/", PHONES.staff);
    expect(await screen.findByText("Новых заказов нет.")).toBeInTheDocument();
  });
});

describe("заказ по факсу (UC-STF-02…05) → оплата → выдача (UC-STF-06, 07)", () => {
  it("весь путь: меню → получение → клиент → заказ принят → оплата на кассе → готов → выдан", async () => {
    const user = userEvent.setup();
    await renderApp("/fax", PHONES.staff);
    await user.click(await screen.findByRole("button", { name: "Добавить «Чикен-ролл»" }));
    expect(await screen.findByText("К оплате")).toBeInTheDocument();
    const submit = screen.getByRole("button", { name: /Оформить заказ/ });
    expect(submit).toBeDisabled(); // нет способа получения и телефона
    expect(screen.getByText("Выберите способ получения")).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: /Самовывоз/ }));
    await screen.findByText(/Выбрано: самовывоз/);
    expect(screen.getByText("Укажите телефон клиента")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Телефон (обязательно)"), "8 (999) 555-01-01");
    await user.type(screen.getByLabelText("Имя"), "Анна");
    await waitFor(() => expect(screen.getByRole("button", { name: /Оформить заказ · 199,90/ })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: /Оформить заказ · 199,90/ }));

    expect(await screen.findByText("Заказ принят")).toBeInTheDocument();
    const num = screen.getByLabelText(/Номер заказа R1-/);
    const number = num.textContent!;
    expect(db.orders.size).toBe(1);
    const stored = [...db.orders.values()][0]!;
    expect(stored.order.channel).toBe("staff_fax");
    expect(stored.order.paymentMethod).toBe("pos");
    expect(stored.contact).toMatchObject({ phone: "+79995550101", name: "Анна" });

    await user.click(screen.getByRole("link", { name: "Открыть заказ" }));
    await heading(`Заказ ${number}`);
    expect(screen.getByText("К оплате на кассе")).toBeInTheDocument();
    expect(screen.getByText("Заказ ещё не готов")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Выдать заказ" })).toBeDisabled();

    await user.click(screen.getByRole("button", { name: /Принять оплату · 199,90/ }));
    const dlg = await screen.findByRole("dialog", { name: "Принять оплату" });
    await user.click(within(dlg).getByRole("radio", { name: /Наличными/ }));
    await user.click(within(dlg).getByRole("button", { name: /Принять 199,90/ }));
    expect(await screen.findByText("Оплачен")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(await screen.findByText(/Платёж · Наличными/)).toBeInTheDocument(); // чек: paymentId запомнен на устройстве

    // готовка идёт на производстве: подтягиваем статус как это сделал бы mock-production
    stored.order.status = "ready";
    await waitFor(() => expect(screen.getByRole("button", { name: "Выдать заказ" })).toBeEnabled(), { timeout: 8000 });
    await user.click(screen.getByRole("button", { name: "Выдать заказ" }));
    const issue = await screen.findByRole("dialog", { name: "Выдать заказ?" });
    await user.click(within(issue).getByRole("button", { name: "Выдать и закрыть" }));
    expect(await screen.findByText(`Заказ ${number} выдан и закрыт.`)).toBeInTheDocument();
    expect(stored.order.status).toBe("closed");
    expect(screen.getByRole("button", { name: "Выдать заказ" })).toBeDisabled();
    expect(screen.getByText("Заказ уже выдан")).toBeInTheDocument();
  }, 40_000);

  it("недоступные блюда не добавить; смена ресторана не нужна — корзина восстанавливается после перезагрузки", async () => {
    const user = userEvent.setup();
    const first = await renderApp("/fax", PHONES.staff);
    expect(await screen.findByText("Веган-бургер")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Добавить «Веган-бургер»" })).not.toBeInTheDocument();
    expect(screen.getAllByText("Нет в наличии").length).toBeGreaterThan(0);
    await user.click(screen.getByRole("button", { name: "Добавить «Чикен-ролл»" }));
    await screen.findByText("К оплате");
    first.unmount();
    await renderApp("/fax", PHONES.staff);
    await waitFor(() => expect(screen.getByText("К оплате")).toBeInTheDocument());
    expect([...db.carts.values()].length).toBe(1); // вторая корзина не создана
  });

  it("добавки: позиция с модификаторами добавляется через диалог, цена строки учитывает добавку", async () => {
    const user = userEvent.setup();
    await renderApp("/fax", PHONES.staff);
    await user.click(await screen.findByRole("button", { name: "Добавить «Бургер «Классика»» с добавками" }));
    const dlg = await screen.findByRole("dialog", { name: "Бургер «Классика»" });
    expect(within(dlg).getByRole("checkbox", { name: /Халапеньо/ })).toBeDisabled(); // нет в наличии
    await user.click(within(dlg).getByRole("checkbox", { name: /Двойной сыр/ }));
    await user.click(within(dlg).getByRole("button", { name: /Добавить · 299/ }));
    expect(await screen.findByText("Двойной сыр", { selector: "p.text-sm" })).toBeInTheDocument();
    const cart = [...db.carts.values()][0]!.cart;
    expect(cart.items[0]!.modifierIds).toEqual(["m-cheese"]);
    expect(cart.total.amount).toBe(29900);
  });
});

describe("оплата на кассе: исходы (UC-STF-06)", () => {
  it("отказ кассы: объяснение, заказ остаётся неоплаченным, можно повторить", async () => {
    const o = await faxOrder({ total: 24913 });
    const user = userEvent.setup();
    await renderApp(`/o/${o.number}`, PHONES.staff);
    await user.click(await screen.findByRole("button", { name: /Принять оплату/ }));
    const dlg = await screen.findByRole("dialog", { name: "Принять оплату" });
    await user.click(within(dlg).getByRole("button", { name: /Принять 249,13/ }));
    expect(await within(dlg).findByText(/Касса отказала в операции/)).toBeInTheDocument();
    expect(db.orders.get(o.id)!.order.paymentStatus).toBe("pending");
    // повтор: новый ключ, теперь касса проведёт (сумма изменилась)
    db.orders.get(o.id)!.order.total = { amount: 24900, currency: "RUB" };
    await user.click(within(dlg).getByRole("button", { name: /Принять 249/ }));
    expect(await screen.findByText("Оплачен")).toBeInTheDocument();
  });

  it("исход неизвестен: предупреждение «не принимайте повторно», кнопка заблокирована", async () => {
    const o = await faxOrder({ total: 24999 });
    db.posDelayMs = 60_000;
    const user = userEvent.setup();
    await renderApp(`/o/${o.number}`, PHONES.staff);
    await user.click(await screen.findByRole("button", { name: /Принять оплату/ }));
    const dlg = await screen.findByRole("dialog", { name: "Принять оплату" });
    await user.click(within(dlg).getByRole("button", { name: /Принять 249,99/ }));
    expect(await screen.findByText(/Не принимайте оплату повторно/)).toBeInTheDocument();
    expect(await screen.findByText("Проверяем платёж")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Оплата обрабатывается…" })).toBeDisabled();
  });

  it("заказ уже оплачен в другой вкладке: окно закрывается, показываем актуальное состояние", async () => {
    const o = await faxOrder();
    const user = userEvent.setup();
    await renderApp(`/o/${o.number}`, PHONES.staff);
    await user.click(await screen.findByRole("button", { name: /Принять оплату/ }));
    const dlg = await screen.findByRole("dialog", { name: "Принять оплату" });
    // параллельный кассир успел раньше
    db.orders.get(o.id)!.order.paymentStatus = "paid";
    await user.click(within(dlg).getByRole("button", { name: /Принять/ }));
    expect(await screen.findByText("Заказ уже оплачен.")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await screen.findByText("Оплачен")).toBeInTheDocument();
  });
});

describe("отмена (UC-STF-08)", () => {
  it("принятый заказ отменяется с причиной и уходит из очереди", async () => {
    const o = await faxOrder();
    const user = userEvent.setup();
    await renderApp(`/o/${o.number}`, PHONES.staff);
    await user.click(await screen.findByRole("button", { name: "Отменить заказ" }));
    const dlg = await screen.findByRole("dialog", { name: "Отменить заказ?" });
    await user.selectOptions(within(dlg).getByLabelText("Причина"), "out_of_stock");
    await user.click(within(dlg).getByRole("button", { name: "Отменить заказ" }));
    expect(await screen.findByText(`Заказ ${o.number} отменён.`)).toBeInTheDocument();
    expect(db.orders.get(o.id)!.order.statusReason).toBe("out_of_stock");
    expect(screen.getByRole("button", { name: "Отменить заказ" })).toBeDisabled();
    expect(screen.getByText("Блюда закончились", { exact: false })).toBeInTheDocument();
  });

  it("оплаченный заказ: предупреждение о возврате и ход возврата на карточке", async () => {
    const o = await faxOrder();
    const user = userEvent.setup();
    await renderApp(`/o/${o.number}`, PHONES.staff);
    await user.click(await screen.findByRole("button", { name: /Принять оплату/ }));
    await user.click(within(await screen.findByRole("dialog", { name: "Принять оплату" })).getByRole("button", { name: /Принять/ }));
    await screen.findByText("Оплачен");
    await user.click(screen.getByRole("button", { name: "Отменить заказ" }));
    const dlg = await screen.findByRole("dialog", { name: "Отменить заказ?" });
    expect(within(dlg).getByText(/деньги вернутся клиенту автоматически/)).toBeInTheDocument();
    await user.click(within(dlg).getByRole("button", { name: "Отменить заказ" }));
    expect(await screen.findByText("Возврат денег клиенту выполняется.")).toBeInTheDocument();
    db.orders.get(o.id)!.refund!.requestedAt = new Date(Date.now() - 5000).toISOString();
    expect(await screen.findByText("Деньги клиенту возвращены.", undefined, { timeout: 8000 })).toBeInTheDocument();
  }, 30_000);

  it("готовящийся заказ не отменить: кнопка заблокирована с причиной", async () => {
    const o = await faxOrder();
    db.orders.get(o.id)!.order.status = "cooking";
    await renderApp(`/o/${o.number}`, PHONES.staff);
    await heading(`Заказ ${o.number}`);
    expect(screen.getByRole("button", { name: "Отменить заказ" })).toBeDisabled();
    expect(screen.getByText("Заказ уже готовится — отменить нельзя")).toBeInTheDocument();
  });

  it("гонка: заказ ушёл в готовку, пока открыт диалог — 409 объясняется", async () => {
    const o = await faxOrder();
    const user = userEvent.setup();
    await renderApp(`/o/${o.number}`, PHONES.staff);
    await user.click(await screen.findByRole("button", { name: "Отменить заказ" }));
    const dlg = await screen.findByRole("dialog", { name: "Отменить заказ?" });
    db.orders.get(o.id)!.order.status = "cooking";
    await user.click(within(dlg).getByRole("button", { name: "Отменить заказ" }));
    expect(await within(dlg).findByText(/уже готовится — отменить нельзя/)).toBeInTheDocument();
  });
});

describe("выдача: условия (UC-STF-07)", () => {
  it("доставку выдаёт курьер; готовый неоплаченный — ORDER_NOT_PAID предлагает принять оплату", async () => {
    const o = await faxOrder();
    db.orders.get(o.id)!.order.status = "ready";
    // кнопка по данным заблокирована, но сервер — последняя инстанция: гонка с обновлением статуса оплаты
    const user = userEvent.setup();
    await renderApp(`/o/${o.number}`, PHONES.staff);
    await heading(`Заказ ${o.number}`);
    expect(screen.getByRole("button", { name: "Выдать заказ" })).toBeDisabled();
    expect(screen.getByText("Сначала примите оплату")).toBeInTheDocument();
    db.orders.get(o.id)!.order.paymentStatus = "paid"; // оплатили в другом окне — карточка ещё не знает
    await user.click(await screen.findByRole("button", { name: "Выдать заказ" }).then(async (b) => { await waitFor(() => expect(b).toBeEnabled(), { timeout: 8000 }); return b; }));
    const dlg = await screen.findByRole("dialog", { name: "Выдать заказ?" });
    db.orders.get(o.id)!.order.paymentStatus = "pending"; // откат: сервер отказывает
    await user.click(within(dlg).getByRole("button", { name: "Выдать и закрыть" }));
    expect(await within(dlg).findByText(/Заказ не оплачен/)).toBeInTheDocument();
    await user.click(within(dlg).getByRole("button", { name: "Принять оплату" }));
    expect(await screen.findByRole("dialog", { name: "Принять оплату" })).toBeInTheDocument();
  }, 30_000);

  it("заказ с доставкой: объяснение вместо кнопки", async () => {
    const o = await faxOrder();
    db.orders.get(o.id)!.order.fulfillment = { type: "delivery" };
    await renderApp(`/o/${o.number}`, PHONES.staff);
    await heading(`Заказ ${o.number}`);
    expect(screen.getByText("Заказ с доставкой выдаёт курьер")).toBeInTheDocument();
  });

  it("заказ другого ресторана: «Заказ не найден»", async () => {
    const o = await faxOrder();
    db.orders.get(o.id)!.order.restaurantId = "22222222-2222-4222-8222-222222222222";
    await renderApp(`/o/${o.number}`, PHONES.staff);
    expect(await heading("Заказ не найден")).toBeInTheDocument();
  });
});

describe("акции (UC-PRM-01…13)", () => {
  const fill = async (user: ReturnType<typeof userEvent.setup>, title: string, over: { value?: string; priority?: string } = {}) => {
    await user.type(await screen.findByLabelText("Название"), title);
    fireEvent.change(screen.getByLabelText("Начало"), { target: { value: "2026-11-01T09:00" } });
    fireEvent.change(screen.getByLabelText("Окончание"), { target: { value: "2027-02-01T09:00" } });
    await user.type(screen.getByLabelText("Размер, %"), over.value ?? "15");
    if (over.priority) { await user.clear(screen.getByLabelText("Приоритет")); await user.type(screen.getByLabelText("Приоритет"), over.priority); }
  };

  it("владелец видит свои акции, но не национальные; HQ — все", async () => {
    const o = await renderApp("/promotions", PHONES.owner);
    expect(await screen.findByText("−10% от 500 ₽")).toBeInTheDocument();
    expect(screen.queryByText("−100 ₽ на самовывоз")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Охват")).not.toBeInTheDocument(); // фильтр охвата — только HQ
    o.unmount();
    await renderApp("/promotions", PHONES.hq);
    expect(await screen.findByText("−10% от 500 ₽")).toBeInTheDocument();
    expect(await screen.findByText("−100 ₽ на самовывоз")).toBeInTheDocument();
    expect(screen.getByText("Вся сеть")).toBeInTheDocument();
  });

  it("черновик → активация → конфликт → смена приоритета → завершение; список показывает статусы", async () => {
    const user = userEvent.setup();
    await renderApp("/promotions/new", PHONES.owner);
    expect(screen.queryByRole("radio", { name: "Вся сеть" })).not.toBeInTheDocument(); // национальные — только HQ
    await fill(user, "Хит недели");
    expect(screen.getByLabelText("Размер скидки")).toHaveTextContent("−15 %"); // предпросмотр
    await user.click(screen.getByRole("button", { name: "Создать черновик" }));
    await heading("Хит недели");
    expect(await screen.findByText("Черновик")).toBeInTheDocument();
    const [first] = [...db.promotions.values()].filter((p) => p.title === "Хит недели");
    expect(first).toMatchObject({ status: "draft", scope: "local", restaurantId: R1, discount: { type: "percent", value: 1500 }, priority: 0 });
    expect(first!.startsAt).toBe("2026-11-01T06:00:00.000Z"); // 09:00 по Москве = 06:00 UTC

    await user.click(screen.getByRole("button", { name: "Активировать" }));
    await user.click(within(await screen.findByRole("dialog", { name: "Активировать акцию?" })).getByRole("button", { name: "Активировать" }));
    expect(await screen.findByText(/Акция активна/)).toBeInTheDocument();
    expect(first!.status).toBe("active");
    expect(await screen.findByText("Создана", { selector: "span", exact: false })).toBeInTheDocument(); // история: «v2 · Активирована»

    // вторая акция того же приоритета конфликтует
    await user.click(screen.getByRole("link", { name: "← Все акции" }));
    await user.click(await screen.findByRole("link", { name: "Новая акция" }));
    await fill(user, "Двойной хит", { value: "20" });
    await user.click(screen.getByRole("button", { name: "Создать черновик" }));
    await heading("Двойной хит");
    await user.click(screen.getByRole("button", { name: "Активировать" }));
    const dlg = await screen.findByRole("dialog", { name: "Активировать акцию?" });
    await user.click(within(dlg).getByRole("button", { name: "Активировать" }));
    expect(await within(dlg).findByText(/конфликтует с уже действующей/)).toBeInTheDocument();
    expect(await within(dlg).findByRole("link", { name: "Хит недели" })).toBeInTheDocument(); // название добрано по id
    await user.click(within(dlg).getByRole("button", { name: "Отмена" }));

    // разводим приоритетом и сохраняем, затем активируем
    await user.clear(screen.getByLabelText("Приоритет"));
    await user.type(screen.getByLabelText("Приоритет"), "30");
    await user.click(screen.getByRole("button", { name: "Сохранить изменения" }));
    expect(await screen.findByText("Изменения сохранены.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Активировать" }));
    await user.click(within(await screen.findByRole("dialog", { name: "Активировать акцию?" })).getByRole("button", { name: "Активировать" }));
    expect(await screen.findByText(/Акция активна/)).toBeInTheDocument();

    // завершение досрочно
    await user.click(screen.getByRole("button", { name: "Завершить досрочно" }));
    await user.click(within(await screen.findByRole("dialog", { name: "Завершить акцию досрочно?" })).getByRole("button", { name: "Завершить" }));
    expect(await screen.findByText("Акция завершена и больше не меняется.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Сохранить изменения" })).not.toBeInTheDocument();

    // список: фильтр по статусу
    await user.click(screen.getByRole("link", { name: "← Все акции" }));
    await user.click(await screen.findByRole("tab", { name: "Завершённые" }));
    expect(await screen.findByText("Двойной хит")).toBeInTheDocument();
    expect(screen.queryByText("Хит недели")).not.toBeInTheDocument();
  }, 60_000);

  it("черновик удаляется после подтверждения; ошибки формы объясняются до отправки", async () => {
    const user = userEvent.setup();
    await renderApp("/promotions/new", PHONES.owner);
    await user.click(await screen.findByRole("button", { name: "Создать черновик" }));
    expect(await screen.findByText("От 1 до 120 символов")).toBeInTheDocument();
    expect(screen.getByText("Укажите начало")).toBeInTheDocument();
    expect(screen.getByText(/Процент от 0,01 до 100/)).toBeInTheDocument();
    expect(db.promotions.size).toBe(2); // на сервер ничего не ушло
    await fill(user, "Черновик");
    await user.click(screen.getByRole("button", { name: "Создать черновик" }));
    await heading("Черновик");
    await user.click(screen.getByRole("button", { name: "Удалить черновик" }));
    await user.click(within(await screen.findByRole("dialog", { name: "Удалить черновик?" })).getByRole("button", { name: "Удалить" }));
    await heading("Акции");
    expect([...db.promotions.values()].some((p) => p.title === "Черновик")).toBe(false);
  }, 30_000);

  it("HQ создаёт национальную акцию без ресторана и позиций", async () => {
    const user = userEvent.setup();
    await renderApp("/promotions/new", PHONES.hq);
    await user.click(await screen.findByRole("radio", { name: "Вся сеть" }));
    expect(document.getElementById("promo-rest")).toBeNull();
    expect(screen.getByText(/Для национальной акции позиции не выбираются/)).toBeInTheDocument();
    await fill(user, "Сезонная");
    await user.click(screen.getByRole("radio", { name: "Сумма" }));
    await user.type(screen.getByLabelText("Размер, ₽"), "99,90");
    await user.click(screen.getByRole("button", { name: "Создать черновик" }));
    await heading("Сезонная");
    const p = [...db.promotions.values()].find((x) => x.title === "Сезонная")!;
    expect(p).toMatchObject({ scope: "national", discount: { type: "fixed", value: 9990 } });
    expect(p.restaurantId).toBeUndefined();
    expect(screen.getByRole("radio", { name: "Вся сеть" })).toBeDisabled(); // охват после создания не меняется
  }, 30_000);
});
