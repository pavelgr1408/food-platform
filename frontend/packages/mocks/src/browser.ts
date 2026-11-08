import { setupWorker } from "msw/browser";
import { createHandlers } from "./handlers";
import { activeDb, loadDb, resetPersistedDb, saveDb } from "./db";

/**
 * Мок-бэкенд в браузере. Состояние живёт в памяти страницы и дублируется в localStorage (ключ `fp.mockdb`):
 * редирект на мок-шлюз оплаты перезагружает страницу.
 */
loadDb(activeDb);
export const worker = setupWorker(...createHandlers(activeDb));
// после каждого ответа сохраняем состояние
worker.events.on("response:mocked", () => saveDb(activeDb));

/** Отладка сценариев из консоли: __mock.db.priceChangeOnNextOrder = true; __mock.reset() */
(window as unknown as { __mock: unknown }).__mock = {
  db: activeDb,
  reset: () => { resetPersistedDb(); window.localStorage.clear(); window.location.reload(); },
};
