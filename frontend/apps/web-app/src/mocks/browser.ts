import { setupWorker } from "msw/browser";
import { createHandlers } from "./handlers";
import { activeDb, loadDb, resetPersistedDb, saveDb } from "./db";

loadDb(activeDb);
export const worker = setupWorker(...createHandlers(activeDb));
// после каждого ответа сохраняем состояние: редирект на мок-шлюз перезагружает страницу
worker.events.on("response:mocked", () => saveDb(activeDb));

/** Отладка сценариев из консоли: __mock.db.priceChangeOnNextOrder = true; __mock.reset() */
(window as unknown as { __mock: unknown }).__mock = {
  db: activeDb,
  reset: () => { resetPersistedDb(); window.localStorage.clear(); window.location.reload(); },
};
