import { useEffect, useState } from "react";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { isApiError, type ApiError } from "@fp/api";
import { isE164, normalizePhone, passwordProblem } from "@fp/shared";
import { useApi } from "../app/api-context";
import { useAuth } from "../app/auth";
import { Button, Field, Notice, PageTitle, Surface } from "../components/ui";
import { errorText } from "../lib/errors";

function useCountdown(sec?: number) {
  const [left, setLeft] = useState(0);
  useEffect(() => { if (!sec) return; setLeft(sec); const t = setInterval(() => setLeft((l) => (l > 0 ? l - 1 : 0)), 1000); return () => clearInterval(t); }, [sec]);
  return left;
}

function AuthForm({ mode }: { mode: "login" | "register" }) {
  const api = useApi();
  const qc = useQueryClient();
  const nav = useNavigate();
  const auth = useAuth();
  const [sp] = useSearchParams();
  const next = sp.get("next") && sp.get("next")!.startsWith("/") ? sp.get("next")! : "/";
  const [phone, setPhone] = useState("");
  const [password, setPassword] = useState("");
  const [show, setShow] = useState(false);
  const [touched, setTouched] = useState(false);

  const phoneN = normalizePhone(phone);
  const phoneErr = touched && !isE164(phoneN) ? "Формат: +79991234567 (8–15 цифр)" : null;
  const pw = passwordProblem(password);
  const pwErr = touched && mode === "register" && pw ? (pw === "short" ? "Минимум 6 символов" : "Слишком длинный пароль (до 72 байт)") : touched && !password ? "Введите пароль" : touched && pw === "long" ? "Слишком длинный пароль" : null;

  const run = useMutation({
    mutationFn: async () => {
      if (mode === "register") await api.auth.register(phoneN, password);
      await api.auth.login(phoneN, password); // UC-ACC-03: после регистрации сразу вход
    },
    onSuccess: async () => { await qc.invalidateQueries({ queryKey: ["me"] }); nav(next, { replace: true }); },
  });
  const err = run.error as ApiError | null;
  const wait = useCountdown(isApiError(err) && err.status === 429 ? err.retryAfter ?? 30 : undefined);

  if (auth.status === "user") return <Navigate to={next} replace />;

  return (
    <div className="lt-rise mx-auto max-w-md">
      <div role="tablist" aria-label="Вход или регистрация" className="mb-5 grid grid-cols-2 gap-1 rounded-full bg-ink/5 p-1">
        <Link role="tab" aria-selected={mode === "login"} to={`/login?next=${encodeURIComponent(next)}`} replace className={`grid min-h-11 place-items-center rounded-full font-semibold ${mode === "login" ? "bg-ink text-white" : ""}`}>Вход</Link>
        <Link role="tab" aria-selected={mode === "register"} to={`/register?next=${encodeURIComponent(next)}`} replace className={`grid min-h-11 place-items-center rounded-full font-semibold ${mode === "register" ? "bg-ink text-white" : ""}`}>Регистрация</Link>
      </div>
      <PageTitle sub={mode === "login" ? "Войдите, чтобы заказы и корзина были под рукой." : "Телефон и пароль — больше ничего не нужно."}>{mode === "login" ? "Вход" : "Регистрация"}</PageTitle>
      <Surface>
        <form noValidate className="grid gap-4" onSubmit={(e) => { e.preventDefault(); setTouched(true); if (isE164(phoneN) && !passwordProblem(password)) run.mutate(); else if (mode === "login" && isE164(phoneN) && password && passwordProblem(password) !== "long") run.mutate(); }}>
          <Field label="Телефон" type="tel" inputMode="tel" autoComplete="tel" placeholder="+7 999 123-45-67" value={phone} onChange={(e) => setPhone(e.target.value)} onBlur={() => setPhone(normalizePhone(phone))} error={phoneErr} />
          <div>
            <Field label="Пароль" type={show ? "text" : "password"} autoComplete={mode === "login" ? "current-password" : "new-password"} value={password} onChange={(e) => setPassword(e.target.value)} error={pwErr} hint={mode === "register" ? "От 6 до 72 символов" : undefined} />
            <label className="mt-2 inline-flex min-h-[var(--lt-tap)] cursor-pointer items-center gap-2 text-sm font-medium">
              <input type="checkbox" className="size-5 accent-[var(--lt-tomato)]" checked={show} onChange={(e) => setShow(e.target.checked)} /> Показать пароль
            </label>
          </div>
          {err ? (
            <Notice>
              {errorText(err)}{err.code === "PHONE_ALREADY_REGISTERED" ? <> <Link to="/login" className="font-bold underline">Войти</Link></> : null}
              {wait > 0 ? ` Повторите через ${wait} с.` : null}
            </Notice>
          ) : null}
          <Button type="submit" variant="accent" cta disabled={run.isPending || wait > 0}>{run.isPending ? "Подождите…" : mode === "login" ? "Войти" : "Зарегистрироваться"}</Button>
        </form>
        <p className="mt-4 text-sm">
          {mode === "login" ? <>Нет аккаунта? <Link to={`/register?next=${encodeURIComponent(next)}`} className="font-semibold underline">Зарегистрироваться</Link></> : <>Уже есть аккаунт? <Link to={`/login?next=${encodeURIComponent(next)}`} className="font-semibold underline">Войти</Link></>}
        </p>
        <p className="mt-2 text-xs text-muted">Восстановления пароля пока нет — сохраните его.</p>
      </Surface>
    </div>
  );
}
export const Login = () => <AuthForm mode="login" />;
export const Register = () => <AuthForm mode="register" />;
