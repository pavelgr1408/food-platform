import { Navigate, useNavigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useApi } from "../app/api-context";
import { useAuth } from "../app/auth";
import { Button, Empty, Notice, PageTitle, Spinner, Surface } from "../components/ui";
import { errorText } from "../lib/errors";
import { fmtDateTime } from "../lib/format";

const ROLE: Record<string, string> = { CUSTOMER: "Покупатель", STAFF: "Сотрудник кассы", FRANCHISE_OWNER: "Владелец франшизы", HQ_STAFF: "Головной офис" };

/** UC-ACC-07 профиль + UC-NTF-03/04 устройства. Редактирования профиля в API нет. */
export function Profile() {
  const auth = useAuth();
  const nav = useNavigate();
  if (auth.status === "loading") return <Spinner />;
  if (auth.status === "guest") return <Navigate to="/login?next=/profile" replace />;
  const p = auth.principal!;
  return (
    <div className="lt-rise grid max-w-xl gap-5">
      <PageTitle>Профиль</PageTitle>
      <Surface>
        <dl className="grid gap-2">
          <div className="flex justify-between gap-3"><dt className="text-muted">Телефон</dt><dd className="font-semibold">{p.phone}</dd></div>
          <div className="flex justify-between gap-3"><dt className="text-muted">Роли</dt><dd className="font-semibold">{p.roles.map((r) => ROLE[r] ?? r).join(", ")}</dd></div>
          <div className="flex justify-between gap-3"><dt className="text-muted">Статус</dt><dd className="font-semibold">{p.status === "ACTIVE" ? "Активен" : p.status}</dd></div>
        </dl>
        {auth.isStaff ? <Notice tone="info" className="mt-4">Это рабочий аккаунт. Кассу, оформление по факсу и акции откроет отдельное приложение для сотрудников — оно будет следующим этапом.</Notice> : null}
        <div className="mt-4"><Button variant="outline" onClick={async () => { await auth.logout(); nav("/"); }}>Выйти</Button></div>
        <p className="mt-3 text-xs text-muted">Смены пароля, имени и сохранённых адресов пока нет.</p>
      </Surface>
      <Devices />
    </div>
  );
}

function Devices() {
  const api = useApi();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["devices"], queryFn: () => api.notifications.list(), retry: false });
  const del = useMutation({
    mutationFn: (id: string) => api.notifications.remove(id).catch((e) => { if (e?.code !== "DEVICE_TOKEN_NOT_FOUND") throw e; }), // уже удалён = успех
    onSuccess: () => qc.invalidateQueries({ queryKey: ["devices"] }),
  });
  return (
    <Surface>
      <h2 className="mb-1 text-[length:var(--lt-fs-price)]">Устройства для уведомлений</h2>
      <p className="mb-3 text-sm text-muted">Уведомления о статусе заказа приходят только авторизованным покупателям. Регистрация браузера (web push) пока не подключена — нужен провайдер FCM.</p>
      {q.isLoading ? <Spinner /> : null}
      {q.isError ? <Notice>{errorText(q.error)}</Notice> : null}
      {q.data && q.data.length === 0 ? <Empty title="Устройств нет" /> : null}
      <ul className="grid gap-2">
        {(q.data ?? []).map((d) => (
          <li key={d.id} className="flex flex-wrap items-center justify-between gap-3 rounded-tile border-2 border-ink/20 px-4 py-2">
            <span><strong>{d.platform.toUpperCase()}</strong> · {fmtDateTime(d.registeredAt)} · {d.permissionsGranted ? "уведомления включены" : "выключены"}</span>
            <Button variant="ghost" onClick={() => del.mutate(d.id)} disabled={del.isPending}>Убрать</Button>
          </li>
        ))}
      </ul>
      {del.error ? <Notice className="mt-3">{errorText(del.error)}</Notice> : null}
    </Surface>
  );
}
