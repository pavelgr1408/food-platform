import { isApiError } from "@fp/api";

/** Код ошибки → текст экрана. Ошибка объясняет, что случилось и что делать, без извинений (DESIGN-SYSTEM §5). */
const MESSAGES: Record<string, string> = {
  VALIDATION_FAILED: "Проверьте поля формы.",
  INVALID_PARAMETER: "Некорректное значение. Проверьте введённые данные.",
  COORDINATES_INCOMPLETE: "Укажите обе координаты: широту и долготу.",
  PHONE_ALREADY_REGISTERED: "Этот номер уже зарегистрирован. Войдите или используйте другой номер.",
  INVALID_CREDENTIALS: "Неверный телефон или пароль.",
  USER_NOT_ACTIVE: "Аккаунт заблокирован.",
  INVALID_REFRESH_TOKEN: "Сессия истекла. Войдите снова.",
  REFRESH_TOKEN_EXPIRED: "Сессия истекла. Войдите снова.",
  INVALID_TOKEN: "Сессия истекла. Войдите снова.",
  RATE_LIMITED: "Слишком много запросов. Подождите немного.",
  RESTAURANT_NOT_FOUND: "Ресторан недоступен.",
  MENU_NOT_FOUND: "Меню временно недоступно.",
  CART_NOT_FOUND: "Корзина не найдена — создадим новую.",
  CART_CLOSED: "Корзина уже оформлена. Откройте заказ или начните новую корзину.",
  CART_EMPTY: "Корзина пуста. Добавьте блюда.",
  CART_CHANGED: "Корзина изменилась. Проверьте состав и оформите ещё раз.",
  CONTACT_REQUIRED: "Укажите телефон, чтобы мы могли связаться с вами по заказу.",
  FULFILLMENT_REQUIRED: "Выберите способ получения.",
  ADDRESS_REQUIRED: "Укажите адрес доставки.",
  ADDRESS_LOCATION_REQUIRED: "Поставьте точку доставки на карте.",
  ADDRESS_OUT_OF_ZONE: "Адрес вне зоны доставки ресторана.",
  DELIVERY_NOT_AVAILABLE: "Этот ресторан не доставляет. Выберите самовывоз.",
  PAYMENT_METHOD_NOT_ALLOWED: "Этот способ оплаты недоступен для выбранного способа получения.",
  PRICE_CHANGED: "Цены обновились.",
  TOTAL_CHANGED: "Итог изменился из-за акции.",
  ITEMS_UNAVAILABLE: "Некоторые блюда закончились.",
  RESTAURANT_CLOSED: "Ресторан сейчас закрыт. Корзина сохранена — оформите заказ в рабочее время.",
  IDEMPOTENCY_IN_PROGRESS: "Заказ уже оформляется. Подождите пару секунд.",
  IDEMPOTENCY_KEY_REUSED: "Данные изменились. Подтвердите заказ ещё раз.",
  MENU_UNAVAILABLE: "Меню временно недоступно. Попробуйте позже — корзина сохранена.",
  RESTAURANT_UNAVAILABLE: "Сервис ресторанов временно недоступен. Попробуйте позже — корзина сохранена.",
  UPSTREAM_TIMEOUT: "Сервер долго отвечает. Попробуйте ещё раз — корзина сохранена.",
  UPSTREAM_UNAVAILABLE: "Сервис временно недоступен. Попробуйте позже — корзина сохранена.",
  NETWORK_ERROR: "Нет связи с сервером. Проверьте интернет и повторите.",
  ORDER_NOT_FOUND: "Заказ не найден.",
  PAYMENT_DECLINED: "Банк отказал в оплате. Попробуйте другую карту.",
  PAYMENT_IN_PROGRESS: "Платёж уже обрабатывается. Подождите результата.",
  ALREADY_PAID: "Заказ уже оплачен.",
  ORDER_NOT_PAYABLE: "Этот заказ нельзя оплатить.",
  PAYMENT_STATUS_UNKNOWN: "Проверяем платёж. Не платите повторно.",
  PAYMENT_METHOD_NOT_SUPPORTED: "Для этого заказа онлайн-оплата не используется.",
  INVALID_SLOT: "Выберите время в будущем: не дальше 7 суток и не длиннее 4 часов.",
  ORDER_STATE_CONFLICT: "Заказ уже готовится — время изменить нельзя.",
  DEVICE_TOKEN_NOT_FOUND: "Устройство уже удалено.",
  UNAUTHORIZED: "Войдите, чтобы продолжить.",
  UNAUTHENTICATED: "Войдите или продолжите как гость.",
};

export function errorText(e: unknown, fallback = "Что-то пошло не так. Повторите действие."): string {
  if (!isApiError(e)) return fallback;
  const base = MESSAGES[e.code];
  if (e.status === 429 && e.retryAfter) return `${MESSAGES.RATE_LIMITED ?? ""} Повторите через ${e.retryAfter} с.`.trim();
  if (e.code === "ADDRESS_OUT_OF_ZONE") {
    const d = e.extra.distanceMeters, r = e.extra.radiusMeters;
    if (typeof d === "number" && typeof r === "number") return `До вас ${d} м, зона доставки — ${r} м. Выберите самовывоз или другой адрес.`;
  }
  if (base) return base;
  if (e.status >= 500) return "Сервис временно недоступен. Попробуйте позже.";
  return e.detail ?? e.title ?? fallback;
}
