import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { makeApi } from "@fp/ui";
import { App } from "./app/App";
import { API_BASE, API_MODE } from "./lib/env";

async function boot() {
  if (API_MODE === "mock") {
    try {
      const { worker } = await import("@fp/mocks/browser");
      await worker.start({ onUnhandledFrame: "bypass", serviceWorker: { url: "/mockServiceWorker.js" } });
    } catch (e) {
      // без Service Worker моки не работают (встроенные браузеры, приватный режим) — объясняем, а не показываем пустой экран
      document.getElementById("root")!.innerHTML = '<main style="font:16px system-ui;max-width:36rem;margin:3rem auto;padding:0 1rem"><h1>Моки не запустились</h1><p>Режим mock требует Service Worker. Откройте страницу в обычном Chrome или Safari (не во встроенном просмотрщике и не в приватном окне), либо запустите режим real: <code>make web-staff-dev</code>.</p></main>';
      console.error(e);
      return;
    }
  }
  createRoot(document.getElementById("root")!).render(
    <StrictMode><App api={makeApi(API_BASE, window.localStorage)} /></StrictMode>,
  );
}
void boot();
