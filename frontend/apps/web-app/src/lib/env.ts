export const API_MODE: "mock" | "real" = import.meta.env.VITE_API_MODE === "mock" ? "mock" : "real";
export const API_BASE: string = (import.meta.env.VITE_API_BASE_URL as string | undefined) ?? "/api";

/**
 * redirect — после создания платежа уходим на paymentUrl шлюза и возвращаемся по returnUrl (прод, мок-шлюз MSW).
 * poll     — остаёмся на странице заказа и опрашиваем статус: у mock-externals страница /pay/{id} отдаёт JSON,
 *            возврата на returnUrl нет, а итог приходит вебхуком сам (локальный стенд).
 */
export const PAYMENT_MODE: "redirect" | "poll" =
  API_MODE === "mock" ? "redirect" : (import.meta.env.VITE_PAYMENT_MODE as string | undefined) === "poll" ? "poll" : "redirect";
