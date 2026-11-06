import { Link, NavLink, Outlet } from "react-router";
import { useAuth } from "../app/auth";
import { useCart } from "../lib/cart";

export function Layout() {
  const auth = useAuth();
  const { count } = useCart();
  const nav = "inline-flex min-h-[var(--lt-tap)] items-center rounded-full px-4 font-semibold hover:bg-ink/5 aria-[current=page]:bg-ink aria-[current=page]:text-white";
  return (
    <div className="mx-auto flex min-h-dvh max-w-5xl flex-col px-4 pb-16">
      <header className="flex flex-wrap items-center justify-between gap-2 py-4">
        <Link to="/" className="rounded-full bg-tomato px-4 py-2 font-display text-lg font-semibold text-white">Лоток</Link>
        <nav aria-label="Основная" className="flex flex-wrap items-center gap-1">
          <NavLink to="/" end className={nav}>Рестораны</NavLink>
          <NavLink to="/orders" end className={nav}>Мои заказы</NavLink>
          <NavLink to="/cart" className={nav}>
            Корзина{count > 0 ? <span className="ml-2 rounded-full bg-lemon px-2 text-sm text-ink" aria-label={`позиций: ${count}`}>{count}</span> : null}
          </NavLink>
          {auth.status === "user" ? (
            <NavLink to="/profile" className={nav}>Профиль</NavLink>
          ) : (
            <NavLink to="/login" className={nav}>Войти</NavLink>
          )}
        </nav>
      </header>
      <main className="flex-1"><Outlet /></main>
      <footer className="mt-10 text-sm text-faint">Лоток — учебный проект «Заказ еды»</footer>
    </div>
  );
}
