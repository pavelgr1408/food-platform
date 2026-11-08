import { useEffect } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router";
import { fullAddress } from "@fp/ui";
import { useStaffAuth } from "../app/auth";
import { useWorkplace } from "../app/workplace";

const ROLE_LABEL: Record<string, string> = { STAFF: "Кассир", FRANCHISE_OWNER: "Владелец", HQ_STAFF: "HQ" };

/**
 * Каркас: тёмная шапка отличает кассу от клиентского «Лотка». Навигация — крупные пилюли (цель нажатия 48 px),
 * на узких экранах прокручивается по горизонтали, а не прячется в меню: на кассе меню под рукой всегда.
 */
export function Shell() {
  const auth = useStaffAuth();
  const wp = useWorkplace();
  const loc = useLocation();
  useEffect(() => { if (!loc.hash) window.scrollTo({ top: 0 }); }, [loc.pathname, loc.hash]);

  const role = auth.principal?.roles.map((r) => ROLE_LABEL[r] ?? r)[0] ?? "";
  const link = "inline-flex min-h-[var(--lt-tap)] shrink-0 items-center rounded-full px-5 font-semibold text-white/85 transition-colors hover:bg-white/10 aria-[current=page]:bg-lemon aria-[current=page]:text-ink";

  return (
    <div className="flex min-h-dvh flex-col">
      <a href="#main" className="sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-4 focus:z-50 focus:rounded-full focus:bg-ink focus:px-4 focus:py-2 focus:text-white">К содержимому</a>
      <header className="sticky top-0 z-30 bg-ink text-white">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center gap-x-4 gap-y-2 px-4 py-2.5">
          <Link to="/" className="rounded-full bg-tomato px-4 py-2 font-display text-lg font-semibold text-white" aria-label="Лоток, касса — на главный экран">Лоток · Касса</Link>
          <nav aria-label="Разделы" className="order-3 -mx-1 flex w-full gap-1 overflow-x-auto px-1 md:order-none md:w-auto">
            <NavLink to="/" end className={link}>Касса</NavLink>
            <NavLink to="/fax" className={link}>Заказ по факсу</NavLink>
            {auth.canPromotions ? <NavLink to="/promotions" className={link}>Акции</NavLink> : null}
          </nav>
          <div className="ml-auto flex flex-wrap items-center gap-3">
            {wp.restaurants.length > 1 ? (
              <label className="flex items-center gap-2 text-sm">
                <span className="text-white/70">Ресторан</span>
                <select aria-label="Рабочий ресторан" value={wp.current?.id} onChange={(e) => wp.select(e.target.value)}
                  className="min-h-11 max-w-56 rounded-input border-2 border-white/30 bg-ink px-3 text-base text-white">
                  {wp.restaurants.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
                </select>
              </label>
            ) : wp.current ? (
              <p className="text-sm leading-tight"><span className="block font-semibold">{wp.current.name}</span><span className="text-white/70">{fullAddress(wp.current)}</span></p>
            ) : null}
            <p className="text-sm text-white/70">{role}{auth.principal ? ` · ${auth.principal.phone}` : ""}</p>
            <button type="button" onClick={() => void auth.logout()} className="min-h-11 rounded-full border-2 border-white/40 px-4 text-sm font-semibold hover:bg-white/10">Выйти</button>
          </div>
        </div>
      </header>
      <main id="main" tabIndex={-1} className="mx-auto w-full max-w-6xl flex-1 px-4 pb-16 pt-5 outline-none">
        <Outlet />
      </main>
    </div>
  );
}
