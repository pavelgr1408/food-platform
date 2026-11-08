import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { makeApi } from "@fp/ui";
import { App } from "./app/App";
import { API_BASE, API_MODE } from "./lib/env";

async function boot() {
  if (API_MODE === "mock") {
    const { worker } = await import("@fp/mocks/browser");
    await worker.start({ onUnhandledFrame: "bypass", serviceWorker: { url: "/mockServiceWorker.js" } });
  }
  createRoot(document.getElementById("root")!).render(
    <StrictMode><App api={makeApi(API_BASE, window.localStorage)} /></StrictMode>,
  );
}
void boot();
