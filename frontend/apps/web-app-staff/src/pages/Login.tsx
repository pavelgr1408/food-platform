import { useEffect, useState } from "react";
import { Navigate, useNavigate, useSearchParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { isApiError, type ApiError } from "@fp/api";
import { isE164, normalizePhone } from "@fp/shared";
import { Button, Field, Notice, PageTitle, Surface, errorText, useApi } from "@fp/ui";
import { useStaffAuth } from "../app/auth";

function useCountdown(sec?: number) {
  const [left, setLeft] = useState(0);
  useEffect(() => {
    if (!sec) return;
    setLeft(sec);
    const t = setInterval(() => setLeft((l) => (l > 0 ? l - 1 : 0)), 1000);
    return () => clearInterval(t);
  }, [sec]);
  return left;
}

/** UC-ACC-04: вход персонала. Регистрации нет: учётные записи сотрудников заводятся только в БД. */
export function Login() {
  const api = useApi();
  const qc = useQueryClient();
  const nav = useNavigate();
  const auth = useStaffAuth();
  const [sp] = useSearchParams();
  const raw = sp.get("next");
  const next = raw && raw.startsWith("/") && !raw.startsWith("//") ? raw : "/";
  const [phone, setPhone] = useState("");
  const [password, setPassword] = useState("");
  const [touched, setTouched] = useState(false);

  const phoneN = normalizePhone(phone);
  const phoneErr = touched && !isE164(phoneN) ? "Формат: +79991234567 (8–15 цифр)" : null;
  const pwErr = touched && !password ? "Введите пароль" : null;

  const run = useMutation({
    mutationFn: () => api.auth.login(phoneN, password),
    onSuccess: async () => { await qc.invalidateQueries({ queryKey: ["me"] }); nav(next, { replace: true }); },
  });
  const err = run.error as ApiError | null;
  const wait = useCountdown(isApiError(err) && err.status === 429 ? err.retryAfter ?? 30 : undefined);

  if (auth.status === "user") return <Navigate to={next} replace />;

  return (
    <div className="lt-rise mx-auto max-w-md px-4 pt-10">
      <PageTitle sub="Для сотрудников ресторанов, владельцев франшизы и HQ.">Вход на кассу</PageTitle>
      <Surface>
        <form noValidate className="grid gap-4" onSubmit={(e) => { e.preventDefault(); setTouched(true); if (isE164(phoneN) && password) run.mutate(); }}>
          <Field label="Телефон" type="tel" inputMode="tel" autoComplete="username" placeholder="+7 999 123-45-67" value={phone}
            onChange={(e) => setPhone(e.target.value)} onBlur={() => setPhone(normalizePhone(phone))} error={phoneErr} />
          <Field label="Пароль" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} error={pwErr} />
          {err ? <Notice>{errorText(err)}{wait > 0 ? ` Повторите через ${wait} с.` : ""}</Notice> : null}
          <Button type="submit" variant="accent" cta disabled={run.isPending || wait > 0}>{run.isPending ? "Подождите…" : "Войти"}</Button>
        </form>
        <p className="mt-4 text-xs text-muted">Учётную запись сотрудника выдаёт владелец франшизы или HQ. Забыли пароль — обратитесь к ним.</p>
      </Surface>
    </div>
  );
}
