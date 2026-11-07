import { useEffect, useRef, type ReactNode } from "react";
import { Button } from "./ui";

/** Доступный диалог: role=dialog, Escape, фокус на закрытие, возврат фокуса. */
export function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    ref.current?.focus();
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    document.addEventListener("keydown", onKey);
    return () => { document.removeEventListener("keydown", onKey); prev?.focus?.(); };
  }, [onClose]);
  return (
    <div className="fixed inset-0 z-50 flex items-end justify-center bg-ink/60 p-0 md:items-center md:p-6" onClick={onClose}>
      <div ref={ref} tabIndex={-1} role="dialog" aria-modal="true" aria-label={title} onClick={(e) => e.stopPropagation()}
        className="lt-rise max-h-[90dvh] w-full max-w-lg overflow-y-auto rounded-t-card bg-white p-5 md:rounded-card">
        <div className="mb-3 flex items-start justify-between gap-3">
          <h2 className="text-[length:var(--lt-fs-h2)] leading-tight">{title}</h2>
          <Button variant="ghost" onClick={onClose} aria-label="Закрыть" className="!px-4">✕</Button>
        </div>
        {children}
      </div>
    </div>
  );
}
