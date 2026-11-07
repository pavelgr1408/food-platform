import { useEffect, useSyncExternalStore, type ReactNode } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router";
import { formatMoney } from "@fp/shared";
import { useAuth } from "../app/auth";
import { useCart } from "../lib/cart";

const Icon = ({ children }: { children: ReactNode }) => (
  <svg viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden>{children}</svg>
);
const ICONS = {
  home: <Icon><path d="M3 10l9-7 9 7v10a1 1 0 0 1-1 1h-5v-6H9v6H4a1 1 0 0 1-1-1z" /></Icon>,
  orders: <Icon><path d="M6 3h12v18l-3-2-3 2-3-2-3 2z" /><path d="M9 8h6M9 12h6" /></Icon>,
  cart: <Icon><path d="M5 8h14l-1 12H6z" /><path d="M9 8V6a3 3 0 0 1 6 0v2" /></Icon>,
  user: <Icon><circle cx="12" cy="8" r="4" /><path d="M4 21c0-4 4-6 8-6s8 2 8 6" /></Icon>,
};

const MQ = "(min-width: 768px)";
const subscribeMq = (cb: () => void) => {
  if (typeof window === "undefined" || !window.matchMedia) return () => undefined;
  const m = window.matchMedia(MQ);
  m.addEventListener("change", cb);
  return () => m.removeEventListener("change", cb);
};
/** Рендерим только одну навигацию: две сразу — дубли ориентиров для скринридера и тестов. */
const useIsDesktop = () => useSyncExternalStore(subscribeMq, () => (window.matchMedia ? window.matchMedia(MQ).matches : true), () => true);

/** Верхняя панель (≥ md) и нижняя таб-панель (< md): основные действия всегда под большим пальцем. */
export function Layout() {
  const auth = useAuth();
  const { count, cart } = useCart();
  const loc = useLocation();
  const desktop = useIsDesktop();
  // при переходе между экранами — наверх (иначе страница заказа открывается «посередине»)
  useEffect(() => { if (!loc.hash) window.scrollTo({ top: 0 }); }, [loc.pathname, loc.hash]);

  const profileTo = auth.status === "user" ? "/profile" : "/login";
  const profileLabel = auth.status === "user" ? "Профиль" : "Войти";
  const total = cart && count > 0 ? formatMoney(cart.total.amount, cart.total.currency) : null;

  const top = "inline-flex min-h-[var(--lt-tap)] items-center gap-2 rounded-full px-4 font-semibold transition-colors hover:bg-ink/5 aria-[current=page]:bg-ink aria-[current=page]:text-white";
  const tab = "relative flex min-h-14 flex-1 flex-col items-center justify-center gap-0.5 text-xs font-semibold text-muted aria-[current=page]:text-tomato";

  const badge = count > 0 ? (
    <span className="grid min-w-6 place-items-center rounded-full bg-lemon px-1.5 text-sm font-bold text-ink" aria-label={`позиций: ${count}`}>{count}</span>
  ) : null;

  return (
    <div className="flex min-h-dvh flex-col">
      <a href="#main" className="sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-4 focus:z-50 focus:rounded-full focus:bg-ink focus:px-4 focus:py-2 focus:text-white">К содержимому</a>
      <header className="sticky top-0 z-30 border-b border-ink/10 bg-paper/90 backdrop-blur">
        <div className="mx-auto flex max-w-5xl items-center justify-between gap-3 px-4 py-2.5">
          <Link to="/" className="rounded-full bg-tomato px-4 py-2 font-display text-lg font-semibold text-white" aria-label="Лоток — на главную">Лоток</Link>
          {desktop ? <nav aria-label="Основная" className="flex items-center gap-1">
            <NavLink to="/" end className={top}>Рестораны</NavLink>
            <NavLink to="/orders" end className={top}>Мои заказы</NavLink>
            <NavLink to="/cart" className={top}>Корзина {badge}{total ? <span className="font-normal opacity-80">· {total}</span> : null}</NavLink>
            <NavLink to={profileTo} className={top}>{profileLabel}</NavLink>
          </nav> : (
            <Link to="/cart" className="inline-flex min-h-[var(--lt-tap)] items-center gap-2 rounded-full bg-ink px-4 font-semibold text-white" aria-label={count > 0 ? `Корзина, позиций: ${count}` : "Корзина"}>
              {ICONS.cart}{count > 0 ? <span>{total}</span> : null}
            </Link>
          )}
        </div>
      </header>

      <main id="main" tabIndex={-1} className="mx-auto w-full max-w-5xl flex-1 px-4 pb-28 pt-5 outline-none md:pb-16">
        <Outlet />
      </main>

      {desktop ? <footer className="mx-auto w-full max-w-5xl px-4 pb-8 text-sm text-faint">Лоток — учебный проект «Заказ еды»</footer> : null}

      {!desktop ? <nav aria-label="Разделы" className="fixed inset-x-0 bottom-0 z-30 flex border-t border-ink/10 bg-white/95 pb-[env(safe-area-inset-bottom)] backdrop-blur">
        <NavLink to="/" end className={tab}>{ICONS.home}Рестораны</NavLink>
        <NavLink to="/orders" end className={tab}>{ICONS.orders}Заказы</NavLink>
        <NavLink to="/cart" className={tab}>
          <span className="relative">{ICONS.cart}{count > 0 ? <span className="absolute -right-3 -top-2 grid min-w-5 place-items-center rounded-full bg-tomato px-1 text-[11px] font-bold text-white">{count}</span> : null}</span>
          Корзина
        </NavLink>
        <NavLink to={profileTo} className={tab}>{ICONS.user}{profileLabel}</NavLink>
      </nav> : null}
    </div>
  );
}
