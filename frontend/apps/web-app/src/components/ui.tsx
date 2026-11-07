import { forwardRef, useId, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes } from "react";
import { formatMoney } from "@fp/shared";
import type { Money, OrderStatus } from "@fp/api";
import { STATUS_LABEL } from "../lib/status";

const cx = (...a: (string | false | undefined | null)[]) => a.filter(Boolean).join(" ");

/* ---- Кнопки: пилюли, ≥48 px (DESIGN-SYSTEM §4) ---- */
type Variant = "primary" | "accent" | "outline" | "ghost";
const VARIANT: Record<Variant, string> = {
  primary: "bg-ink text-white hover:bg-black",
  accent: "bg-lemon text-ink hover:brightness-95",
  outline: "border-2 border-ink text-ink bg-transparent hover:bg-ink/5",
  ghost: "text-ink hover:bg-ink/5",
};
export function Button({ variant = "primary", cta, className, ...p }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; cta?: boolean }) {
  return (
    <button
      type="button"
      {...p}
      className={cx(
        "inline-flex items-center justify-center gap-2 rounded-full px-6 font-semibold transition-[filter,background-color] duration-[var(--lt-dur-fast)] disabled:cursor-not-allowed disabled:opacity-50",
        cta ? "min-h-[var(--lt-tap-cta)] text-lg" : "min-h-[var(--lt-tap)]",
        VARIANT[variant],
        className,
      )}
    />
  );
}

/* ---- Поле: видимая label, рамка 2 px ink, радиус 14 ---- */
interface FieldProps extends InputHTMLAttributes<HTMLInputElement> { label: string; error?: string | null; hint?: string }
export const Field = forwardRef<HTMLInputElement, FieldProps>(function Field({ label, error, hint, className, ...p }, ref) {
  const id = useId();
  const desc = error ? `${id}-e` : hint ? `${id}-h` : undefined;
  return (
    <div className={className}>
      <label htmlFor={id} className="mb-1 block text-sm font-semibold">{label}</label>
      <input
        ref={ref}
        id={id}
        aria-invalid={error ? true : undefined}
        aria-describedby={desc}
        {...p}
        className="min-h-[var(--lt-tap)] w-full rounded-input border-2 border-ink bg-white px-4 text-base placeholder:text-faint aria-[invalid=true]:border-tomato"
      />
      {error ? <p id={`${id}-e`} role="alert" className="mt-1 text-sm font-medium text-tomato">{error}</p> : hint ? <p id={`${id}-h`} className="mt-1 text-sm text-muted">{hint}</p> : null}
    </div>
  );
});

export function Select({ label, children, className, ...p }: SelectHTMLAttributes<HTMLSelectElement> & { label: string }) {
  const id = useId();
  return (
    <div className={className}>
      <label htmlFor={id} className="mb-1 block text-sm font-semibold">{label}</label>
      <select id={id} {...p} className="min-h-[var(--lt-tap)] w-full rounded-input border-2 border-ink bg-white px-3 text-base">{children}</select>
    </div>
  );
}

/* ---- Цена: лимонная плашка, Unbounded 600 ---- */
export function Price({ money, className, big }: { money: Money; className?: string; big?: boolean }) {
  return (
    <span className={cx("inline-block rounded-full bg-lemon px-3 py-1 font-display font-semibold text-ink whitespace-nowrap", big ? "text-xl" : "text-[length:var(--lt-fs-price)]", className)}>
      {formatMoney(money.amount, money.currency)}
    </span>
  );
}

export function Surface({ children, className, as: As = "section" }: { children: ReactNode; className?: string; as?: "section" | "div" | "article" }) {
  return <As className={cx("rounded-card bg-white p-5 shadow-[0_1px_0_var(--lt-border)]", className)}>{children}</As>;
}

export function Notice({ tone = "error", children, className }: { tone?: "error" | "info" | "success"; children: ReactNode; className?: string }) {
  const t = { error: "bg-coral text-ink", info: "bg-lemon/60 text-ink", success: "bg-mint text-ink" }[tone];
  return (
    <div role={tone === "error" ? "alert" : "status"} className={cx("rounded-tile px-4 py-3 text-sm font-medium", t, className)}>
      {children}
    </div>
  );
}

export function Spinner({ label = "Загрузка…" }: { label?: string }) {
  return (
    <div role="status" className="flex items-center gap-3 py-6 text-muted">
      <span aria-hidden className="size-5 animate-spin rounded-full border-2 border-ink border-t-transparent motion-reduce:animate-none" />
      <span>{label}</span>
    </div>
  );
}

/* ---- Статус заказа: пилюли по tokens.json → orderStatus ---- */
const PILL: Partial<Record<OrderStatus, string>> = {
  pending_payment: "border-2 border-ink text-ink",
  accepted: "border-2 border-ink text-ink",
  cooking: "bg-lemon text-ink",
  ready: "bg-mint text-ink",
  on_delivery: "bg-tomato text-white",
  delivered: "bg-ink text-white",
  handed_over: "bg-ink text-white",
  closed: "bg-ink text-white",
  cancelled: "bg-coral text-ink",
  not_received: "bg-coral text-ink",
};
export function StatusPill({ status }: { status: OrderStatus }) {
  return <span className={cx("inline-block rounded-full px-3 py-1 text-sm font-semibold", PILL[status])}>{STATUS_LABEL[status]}</span>;
}

export function PageTitle({ children, sub }: { children: ReactNode; sub?: ReactNode }) {
  return (
    <header className="mb-5">
      <h1 className="text-[length:var(--lt-fs-h2)] leading-tight md:text-[length:var(--lt-fs-h1)]">{children}</h1>
      {sub ? <p className="mt-2 text-muted">{sub}</p> : null}
    </header>
  );
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="rounded-card border-2 border-dashed border-ink/30 p-8 text-center">
      <p className="font-display text-lg font-semibold">{title}</p>
      {children ? <div className="mt-3 text-muted">{children}</div> : null}
    </div>
  );
}
