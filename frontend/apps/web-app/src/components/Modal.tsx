import { useEffect, useRef, type ReactNode } from "react";
import { createPortal } from "react-dom";

const FOCUSABLE = 'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';

/**
 * Доступный диалог: role=dialog, Escape, ловушка фокуса, блокировка прокрутки страницы, возврат фокуса.
 * onClose держим в ref: иначе эффект перезапускался на каждый рендер родителя и крал фокус у полей.
 */
export function Modal({ title, onClose, children, hero, footer }: { title: string; onClose: () => void; children: ReactNode; hero?: ReactNode; footer?: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const close = useRef(onClose);
  close.current = onClose;

  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    const el = ref.current;
    el?.focus();
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") { close.current(); return; }
      if (e.key !== "Tab" || !el) return;
      const items = [...el.querySelectorAll<HTMLElement>(FOCUSABLE)];
      if (items.length === 0) { e.preventDefault(); return; }
      const first = items[0]!, last = items[items.length - 1]!;
      if (e.shiftKey && (document.activeElement === first || document.activeElement === el)) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    };
    document.addEventListener("keydown", onKey);
    return () => { document.removeEventListener("keydown", onKey); document.body.style.overflow = overflow; prev?.focus?.(); };
  }, []);

  /* Портал в body: внутри страницы с анимацией transform «fixed» считается от неё, и окно липло к верху прокрученной страницы. */
  return createPortal(
    <div className="fixed inset-0 z-50 flex items-end justify-center bg-ink/60 backdrop-blur-[2px] md:items-center md:p-6" onClick={() => close.current()}>
      <div ref={ref} tabIndex={-1} role="dialog" aria-modal="true" aria-label={title} onClick={(e) => e.stopPropagation()}
        className="lt-rise relative flex max-h-[92dvh] w-full max-w-lg flex-col overflow-hidden rounded-t-card bg-white outline-none md:rounded-card">
        <button type="button" onClick={() => close.current()} aria-label="Закрыть"
          className="absolute right-3 top-3 z-10 grid size-11 place-items-center rounded-full bg-white/95 text-lg font-bold shadow">✕</button>
        <div className="overflow-y-auto">
          {hero}
          <div className="p-5">
            <h2 className="mb-3 pr-10 text-[length:var(--lt-fs-h2)] leading-tight">{title}</h2>
            {children}
          </div>
        </div>
        {footer ? <div className="border-t border-ink/10 bg-white p-4 pb-[max(1rem,env(safe-area-inset-bottom))]">{footer}</div> : null}
      </div>
    </div>,
    document.body,
  );
}
