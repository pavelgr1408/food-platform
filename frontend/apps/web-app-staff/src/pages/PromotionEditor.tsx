import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { isApiError, type ApiError, type Promotion, type PromotionInput } from "@fp/api";
import { Badge, Button, Empty, Field, Modal, Notice, PageTitle, Skeleton, Surface, cx, errorText, fmtDateTime, useApi } from "@fp/ui";
import { useStaffAuth } from "../app/auth";
import { useWorkplace } from "../app/workplace";
import { ToneBadge } from "../components/bits";
import { PHASE_LABEL, PHASE_TONE, buildInput, discountText, emptyForm, formFromPromotion, fmtPeriod, phaseOf, type FormErrors, type PromoForm } from "../lib/promo";

const ACTION_LABEL = { created: "Создана", updated: "Изменена", activated: "Активирована", completed: "Завершена" } as const;
/** Тексты для кодов, у которых на этом экране особый смысл. */
const OVERRIDE = {
  PROMOTION_INVALID: "Сервер отклонил параметры акции. Проверьте период, размер скидки и условия.",
  PROMOTION_CONFLICT: "Акция конфликтует с уже действующей: нестекуемые акции одного приоритета с пересечением по времени, позициям и охвату нельзя включить вместе.",
};

/** Создание и редактирование акции: UC-PRM-01…13. Тот же экран — карточка, действия и история. */
export function PromotionEditor() {
  const { promotionId } = useParams();
  return promotionId ? <Existing id={promotionId} /> : <Editor />;
}

function Existing({ id }: { id: string }) {
  const api = useApi();
  const q = useQuery({ queryKey: ["promotion", id], queryFn: () => api.promotions.get(id), retry: false });
  if (q.isLoading) return <div role="status" aria-label="Загружаем акцию" className="grid gap-4"><Skeleton className="h-24 rounded-card" /><Skeleton className="h-64 rounded-card" /></div>;
  if (q.isError || !q.data) {
    const nf = isApiError(q.error) && q.error.code === "PROMOTION_NOT_FOUND";
    return (
      <Empty page title={nf ? "Акция не найдена" : "Не удалось открыть акцию"}>
        <p>{errorText(q.error)}</p>
        <Link to="/promotions" className="mt-3 inline-block rounded-full bg-lemon px-6 py-3 font-semibold text-ink">К списку акций</Link>
      </Empty>
    );
  }
  return <Editor promotion={q.data} />;
}

function Editor({ promotion }: { promotion?: Promotion }) {
  const api = useApi();
  const qc = useQueryClient();
  const nav = useNavigate();
  const auth = useStaffAuth();
  const wp = useWorkplace();
  const editing = !!promotion;
  const readOnly = promotion?.status === "completed";

  // владельцу доступны его рестораны (wp.restaurants — из привязки), HQ — все
  const restaurantOptions = wp.restaurants;
  const tzOf = (rid: string) => restaurantOptions.find((r) => r.id === rid)?.timezone ?? wp.current?.timezone ?? "Europe/Moscow";

  const [form, setForm] = useState<PromoForm>(() => promotion ? formFromPromotion(promotion, tzOf(promotion.restaurantId ?? wp.current!.id)) : emptyForm(wp.current!.id));
  const [errors, setErrors] = useState<FormErrors>({});
  const [flash, setFlash] = useState<string | null>(null);
  const [dialog, setDialog] = useState<null | "activate" | "complete" | "delete">(null);
  const set = <K extends keyof PromoForm>(k: K, v: PromoForm[K]) => { setForm((f) => ({ ...f, [k]: v })); setErrors((e) => ({ ...e, [k]: undefined })); };
  const tz = tzOf(form.scope === "local" ? form.restaurantId : promotion?.restaurantId ?? wp.current!.id);

  // позиции меню — для условий (только local: id позиций у каждого ресторана свои)
  const menu = useQuery({ queryKey: ["menu", form.restaurantId], queryFn: () => api.restaurants.menu(form.restaurantId), enabled: form.scope === "local" && !!form.restaurantId, staleTime: 60_000, retry: false });

  // UC-PRM-07: один Idempotency-Key на одно нажатие; после 4xx или смены тела — новый
  const key = useRef<{ key: string; sig: string } | null>(null);
  const busy = useRef(false);

  const save = useMutation({
    onSettled: () => { busy.current = false; },
    mutationFn: async (input: PromotionInput) => {
      if (editing) return api.promotions.update(promotion.id, input);
      const sig = JSON.stringify(input);
      if (!key.current || key.current.sig !== sig) key.current = { key: api.newIdempotencyKey(), sig };
      return api.promotions.create(input, key.current.key);
    },
    onSuccess: (p) => {
      qc.setQueryData(["promotion", p.id], p);
      void qc.invalidateQueries({ queryKey: ["promotions"] });
      void qc.invalidateQueries({ queryKey: ["promotion-history", p.id] });
      if (editing) setFlash("Изменения сохранены."); else nav(`/promotions/${p.id}`, { replace: true });
    },
    onError: (e) => { if (isApiError(e) && e.status >= 400 && e.status < 500) key.current = null; },
  });
  const submit = () => {
    setFlash(null);
    const r = buildInput(form, tz);
    if (r.errors) { setErrors(r.errors); return; }
    if (busy.current) return;
    busy.current = true;
    save.mutate(r.input);
  };
  const saveErr = save.error as ApiError | null;

  const act = useMutation({
    mutationFn: async (kind: "activate" | "complete" | "delete") => {
      if (kind === "activate") return api.promotions.activate(promotion!.id);
      if (kind === "complete") return api.promotions.complete(promotion!.id);
      await api.promotions.remove(promotion!.id);
      return null;
    },
    onSuccess: (p, kind) => {
      void qc.invalidateQueries({ queryKey: ["promotions"] });
      setDialog(null);
      if (kind === "delete") { qc.removeQueries({ queryKey: ["promotion", promotion!.id] }); nav("/promotions", { replace: true }); return; }
      if (p) { qc.setQueryData(["promotion", p.id], p); void qc.invalidateQueries({ queryKey: ["promotion-history", p.id] }); }
      setFlash(kind === "activate" ? "Акция активна. Скидка применяется к новым заказам с небольшой задержкой." : "Акция завершена. Ранее оформленные заказы сохраняют скидку.");
    },
  });
  const actErr = act.error as ApiError | null;
  const conflictIds = (actErr?.code === "PROMOTION_CONFLICT" || saveErr?.code === "PROMOTION_CONFLICT" ? ((actErr ?? saveErr)!.extra.conflictingPromotionIds as string[] | undefined) : undefined) ?? [];

  const ph = promotion ? phaseOf(promotion) : null;
  const hq = auth.isHq;
  const locked = readOnly || save.isPending;

  return (
    <div className="lt-rise grid gap-5 lg:grid-cols-[1fr_340px]">
      <div className="grid content-start gap-5">
        <header>
          <p className="mb-1 text-sm"><Link to="/promotions" className="font-semibold underline">← Все акции</Link></p>
          <PageTitle sub={editing ? undefined : "Сначала сохраняется черновик: скидка заработает после активации."}>{editing ? promotion.title : "Новая акция"}</PageTitle>
          {ph ? <p className="flex flex-wrap items-center gap-2"><ToneBadge tone={PHASE_TONE[ph]}>{PHASE_LABEL[ph]}</ToneBadge><Badge tone="neutral">версия {promotion!.version}</Badge></p> : null}
        </header>

        {flash ? <Notice tone="success">{flash}</Notice> : null}
        {readOnly ? <Notice tone="info">Акция завершена и больше не меняется.</Notice> : null}
        {promotion?.status === "active" ? <Notice tone="info">Акция действует. Изменения применяются к новым заказам с небольшой задержкой — заказы в этот момент считаются по прежним условиям.</Notice> : null}

        <Surface>
          <h2 className="mb-3 text-[length:var(--lt-fs-price)]">Что за акция</h2>
          <div className="grid gap-4">
            <Field label="Название" value={form.title} maxLength={120} disabled={locked} error={errors.title} onChange={(e) => set("title", e.target.value)} hint={`${form.title.trim().length} / 120`} />
            <div>
              <label htmlFor="promo-desc" className="mb-1 block text-sm font-semibold">Описание</label>
              <textarea id="promo-desc" value={form.description} maxLength={1000} rows={3} disabled={locked} onChange={(e) => set("description", e.target.value)}
                aria-describedby="promo-desc-h" className="w-full rounded-input border-2 border-ink bg-white px-4 py-3 text-base" />
              <p id="promo-desc-h" className={cx("mt-1 text-sm", errors.description ? "font-medium text-tomato" : "text-muted")}>{errors.description ?? `${form.description.length} / 1000`}</p>
            </div>
          </div>
        </Surface>

        <Surface>
          <h2 className="mb-3 text-[length:var(--lt-fs-price)]">Где действует</h2>
          <fieldset className="grid gap-3" disabled={editing || locked}>
            <legend className="sr-only">Охват</legend>
            <div role="radiogroup" aria-label="Охват" className="grid gap-2 sm:grid-cols-2">
              <Radio checked={form.scope === "local"} onChange={() => set("scope", "local")} label="Один ресторан" desc="Локальная акция" />
              {hq ? <Radio checked={form.scope === "national"} onChange={() => set("scope", "national")} label="Вся сеть" desc="Национальная акция — только HQ" /> : null}
            </div>
            {form.scope === "local" ? (
              <div>
                <label htmlFor="promo-rest" className="mb-1 block text-sm font-semibold">Ресторан</label>
                <select id="promo-rest" value={form.restaurantId} onChange={(e) => { set("restaurantId", e.target.value); set("menuItemIds", []); }}
                  aria-invalid={errors.restaurantId ? true : undefined} className="min-h-[var(--lt-tap)] w-full rounded-input border-2 border-ink bg-white px-3 text-base">
                  {restaurantOptions.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
                </select>
                {errors.restaurantId ? <p role="alert" className="mt-1 text-sm font-medium text-tomato">{errors.restaurantId}</p> : null}
              </div>
            ) : null}
          </fieldset>
          {editing ? <p className="mt-2 text-sm text-muted">Охват и ресторан после создания не меняются.</p> : null}
        </Surface>

        <Surface>
          <h2 className="mb-3 text-[length:var(--lt-fs-price)]">Когда</h2>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Начало" type="datetime-local" value={form.startsLocal} disabled={locked} error={errors.startsLocal} onChange={(e) => set("startsLocal", e.target.value)} />
            <Field label="Окончание" type="datetime-local" value={form.endsLocal} disabled={locked} error={errors.endsLocal} onChange={(e) => set("endsLocal", e.target.value)} />
          </div>
          <p className="mt-2 text-sm text-muted">Время — в поясе ресторана ({tz}); на сервере хранится в UTC. Акция начнёт действовать после активации, но не раньше начала.</p>
        </Surface>

        <Surface>
          <h2 className="mb-3 text-[length:var(--lt-fs-price)]">Скидка</h2>
          <div className="grid gap-4 sm:grid-cols-[auto_1fr] sm:items-start">
            <div role="radiogroup" aria-label="Тип скидки" className="grid gap-2">
              <Radio checked={form.discountType === "percent"} onChange={() => { set("discountType", "percent"); set("discountValue", ""); }} label="Процент" desc="От суммы заказа" disabled={locked} />
              <Radio checked={form.discountType === "fixed"} onChange={() => { set("discountType", "fixed"); set("discountValue", ""); }} label="Сумма" desc="Фиксированная, в рублях" disabled={locked} />
            </div>
            <Field label={form.discountType === "percent" ? "Размер, %" : "Размер, ₽"} inputMode="decimal" value={form.discountValue} disabled={locked} error={errors.discountValue}
              placeholder={form.discountType === "percent" ? "15" : "100"} onChange={(e) => set("discountValue", e.target.value)}
              hint={form.discountType === "percent" ? "Например 15 или 12,5" : "Например 100 или 99,90"} />
          </div>
        </Surface>

        <Surface>
          <h2 className="mb-3 text-[length:var(--lt-fs-price)]">Условия</h2>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Минимальная сумма заказа, ₽" inputMode="decimal" value={form.minSubtotal} disabled={locked} error={errors.minSubtotal} onChange={(e) => set("minSubtotal", e.target.value)} hint="Пусто — без ограничения" />
            <div>
              <label htmlFor="promo-ful" className="mb-1 block text-sm font-semibold">Способ получения</label>
              <select id="promo-ful" value={form.fulfillment} disabled={locked} onChange={(e) => set("fulfillment", e.target.value as PromoForm["fulfillment"])} className="min-h-[var(--lt-tap)] w-full rounded-input border-2 border-ink bg-white px-3 text-base">
                <option value="">Любой</option><option value="pickup">Только самовывоз</option><option value="delivery">Только доставка</option>
              </select>
            </div>
            <Field label="Приоритет" inputMode="numeric" value={form.priority} disabled={locked} error={errors.priority} onChange={(e) => set("priority", e.target.value)} hint="0–1000: больше — важнее при расчёте" />
            <label className="mt-1 flex min-h-[var(--lt-tap)] cursor-pointer items-center gap-3 self-start rounded-input border-2 border-ink/30 px-4">
              <input type="checkbox" className="size-5 accent-[var(--lt-tomato)]" checked={form.stackable} disabled={locked} onChange={(e) => set("stackable", e.target.checked)} />
              <span><span className="block font-semibold">Суммируется с другими акциями</span><span className="block text-sm text-muted">Две нестекуемые акции одного приоритета с пересечением активировать вместе нельзя</span></span>
            </label>
          </div>
          <fieldset className="mt-4" disabled={locked || form.scope !== "local"}>
            <legend className="mb-1 text-sm font-semibold">Позиции меню</legend>
            {form.scope !== "local" ? <p className="text-sm text-muted">Для национальной акции позиции не выбираются: у каждого ресторана свои идентификаторы блюд. Скидка действует на весь заказ.</p>
              : menu.isLoading ? <Skeleton className="h-16 rounded-tile" />
              : menu.isError ? <p className="text-sm text-muted">Меню ресторана не загрузилось — условие по позициям пока недоступно.</p>
              : (
                <>
                  <p className="mb-2 text-sm text-muted">Пусто — скидка на весь заказ. Выбранные позиции: {form.menuItemIds.length}.</p>
                  <div className="grid gap-1 sm:grid-cols-2">
                    {(menu.data?.categories ?? []).flatMap((c) => c.items).map((it) => (
                      <label key={it.id} className="flex min-h-11 cursor-pointer items-center gap-3 rounded-input px-2 hover:bg-ink/5">
                        <input type="checkbox" className="size-5 accent-[var(--lt-tomato)]" checked={form.menuItemIds.includes(it.id)}
                          onChange={(e) => set("menuItemIds", e.target.checked ? [...form.menuItemIds, it.id] : form.menuItemIds.filter((x) => x !== it.id))} />
                        <span>{it.name}</span>
                      </label>
                    ))}
                  </div>
                </>
              )}
          </fieldset>
        </Surface>

        {saveErr ? <Notice>{errorText(saveErr, undefined, OVERRIDE)}</Notice> : null}
        {actErr && !dialog ? <Notice>{errorText(actErr, undefined, OVERRIDE)}</Notice> : null}
        {conflictIds.length ? <Conflicts ids={conflictIds} /> : null}

        {!readOnly ? (
          <div className="sticky bottom-3 z-20 flex flex-wrap items-center gap-2 rounded-full bg-paper/95 p-2 backdrop-blur">
            <Button variant="accent" cta disabled={save.isPending} onClick={submit}>{save.isPending ? "Сохраняем…" : editing ? "Сохранить изменения" : "Создать черновик"}</Button>
            {promotion?.status === "draft" ? <Button variant="primary" cta disabled={act.isPending} onClick={() => { act.reset(); setDialog("activate"); }}>Активировать</Button> : null}
            {promotion?.status === "active" ? <Button variant="outline" disabled={act.isPending} onClick={() => { act.reset(); setDialog("complete"); }}>Завершить досрочно</Button> : null}
            {promotion?.status === "draft" ? <Button variant="ghost" disabled={act.isPending} onClick={() => { act.reset(); setDialog("delete"); }}>Удалить черновик</Button> : null}
          </div>
        ) : null}
      </div>

      <aside className="grid content-start gap-5 lg:sticky lg:top-24 lg:self-start">
        <Surface>
          <h2 className="mb-2 text-[length:var(--lt-fs-price)]">Как увидит клиент</h2>
          <Preview form={form} />
          {promotion ? <p className="mt-3 text-sm text-muted">Период: {fmtPeriod(promotion.startsAt, promotion.endsAt, tz)}<br />Создана {fmtDateTime(promotion.createdAt, tz)}</p> : null}
        </Surface>
        {promotion ? <History id={promotion.id} tz={tz} /> : null}
      </aside>

      {dialog === "activate" ? (
        <Modal title="Активировать акцию?" onClose={() => setDialog(null)}
          footer={<div className="flex flex-wrap gap-2"><Button variant="accent" cta className="flex-1" disabled={act.isPending} onClick={() => act.mutate("activate")}>{act.isPending ? "Активируем…" : "Активировать"}</Button><Button variant="outline" onClick={() => setDialog(null)}>Отмена</Button></div>}>
          <p className="text-muted">Скидка «{promotion!.title}» начнёт применяться к новым заказам. Сначала сохраните изменения, если вы их вносили: активируется сохранённая версия.</p>
          {actErr ? <Notice className="mt-4">{errorText(actErr, undefined, OVERRIDE)}</Notice> : null}
          {conflictIds.length ? <div className="mt-3"><Conflicts ids={conflictIds} /></div> : null}
        </Modal>
      ) : null}
      {dialog === "complete" ? (
        <Modal title="Завершить акцию досрочно?" onClose={() => setDialog(null)}
          footer={<div className="flex flex-wrap gap-2"><Button variant="primary" cta className="flex-1" disabled={act.isPending} onClick={() => act.mutate("complete")}>{act.isPending ? "Завершаем…" : "Завершить"}</Button><Button variant="outline" onClick={() => setDialog(null)}>Оставить</Button></div>}>
          <p className="text-muted">Новые заказы перестанут получать эту скидку. Действие необратимо. Заказы, оформленные раньше, скидку сохраняют.</p>
          {actErr ? <Notice className="mt-4">{errorText(actErr)}</Notice> : null}
        </Modal>
      ) : null}
      {dialog === "delete" ? (
        <Modal title="Удалить черновик?" onClose={() => setDialog(null)}
          footer={<div className="flex flex-wrap gap-2"><Button variant="primary" cta className="flex-1" disabled={act.isPending} onClick={() => act.mutate("delete")}>{act.isPending ? "Удаляем…" : "Удалить"}</Button><Button variant="outline" onClick={() => setDialog(null)}>Оставить</Button></div>}>
          <p className="text-muted">Черновик «{promotion!.title}» и его история будут удалены без возможности восстановления. Действующую акцию удалить нельзя — её завершают.</p>
          {actErr ? <Notice className="mt-4">{errorText(actErr)}</Notice> : null}
        </Modal>
      ) : null}
    </div>
  );
}

function Radio({ checked, onChange, label, desc, disabled }: { checked: boolean; onChange: () => void; label: string; desc?: string; disabled?: boolean }) {
  return (
    <label className={cx("flex min-h-[var(--lt-tap)] items-center gap-3 rounded-input border-2 px-4 py-2 has-[:focus-visible]:outline has-[:focus-visible]:outline-[3px] has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-ink",
      disabled ? "cursor-not-allowed opacity-60" : "cursor-pointer", checked ? "border-ink bg-lemon/40" : "border-ink/30")}>
      <input type="radio" className="sr-only" disabled={disabled} checked={checked} onChange={onChange} aria-label={label} />
      <span aria-hidden className={cx("grid size-6 shrink-0 place-items-center rounded-full border-2 border-ink", checked ? "bg-ink" : "bg-white")}>{checked ? <span className="size-2 rounded-full bg-lemon" /> : null}</span>
      <span><span className="block font-semibold">{label}</span>{desc ? <span className="block text-sm text-muted">{desc}</span> : null}</span>
    </label>
  );
}

/** Предпросмотр баннера по текущим значениям формы. */
function Preview({ form }: { form: PromoForm }) {
  const r = useMemo(() => buildInput(form, "UTC"), [form]);
  const d = r.input?.discount;
  const bad = !d;
  return (
    <div className="rounded-tile bg-lemon/50 p-4">
      <p className="inline-block rounded-full bg-tomato px-3 py-1 font-display text-lg font-semibold text-white" aria-label="Размер скидки">{d ? discountText(d) : "—"}</p>
      <p className="mt-2 font-semibold">{form.title.trim() || "Название акции"}</p>
      {form.description.trim() ? <p className="text-sm text-muted">{form.description.trim()}</p> : null}
      {bad ? <p className="mt-2 text-xs text-muted">Заполните размер скидки, чтобы увидеть предпросмотр.</p> : null}
    </div>
  );
}

/** Конфликт: из ответа приходят только id — названия добираем отдельными запросами (UC-PRM-10). */
function Conflicts({ ids }: { ids: string[] }) {
  const api = useApi();
  const rs = useQueries({ queries: ids.map((id) => ({ queryKey: ["promotion", id], queryFn: () => api.promotions.get(id), retry: false, staleTime: 30_000 })) });
  return (
    <Notice tone="info">
      <p className="font-semibold">Конфликтующие акции</p>
      <ul className="mt-1 list-disc pl-5">
        {ids.map((id, i) => {
          const p = rs[i]?.data;
          return <li key={id}>{p ? <Link className="font-bold underline" to={`/promotions/${id}`}>{p.title}</Link> : rs[i]?.isLoading ? "загружаем…" : "акция другого ресторана или сети"}{p ? ` — ${PHASE_LABEL[phaseOf(p)].toLowerCase()}, приоритет ${p.priority}` : ""}</li>;
        })}
      </ul>
      <p className="mt-2 text-sm">Чтобы активировать, измените приоритет, сделайте одну из акций суммируемой, сузьте условия или сдвиньте период.</p>
    </Notice>
  );
}

function History({ id, tz }: { id: string; tz?: string }) {
  const api = useApi();
  const h = useQuery({ queryKey: ["promotion-history", id], queryFn: () => api.promotions.history(id), retry: false });
  const [open, setOpen] = useState<number | null>(null);
  useEffect(() => setOpen(null), [id]);
  return (
    <Surface>
      <h2 className="mb-2 text-[length:var(--lt-fs-price)]">История</h2>
      {h.isLoading ? <Skeleton className="h-16 rounded-tile" /> : h.isError ? <p className="text-sm text-muted">{errorText(h.error, "История недоступна.")}</p> : (
        <ol className="grid gap-2">
          {[...(h.data ?? [])].reverse().map((r) => (
            <li key={r.version} className="rounded-tile bg-ink/5 p-3 text-sm">
              <button type="button" className="flex w-full items-center justify-between gap-2 text-left font-semibold" aria-expanded={open === r.version} onClick={() => setOpen(open === r.version ? null : r.version)}>
                <span>v{r.version} · {ACTION_LABEL[r.action]}</span><span className="font-normal text-muted">{fmtDateTime(r.occurredAt, tz)}</span>
              </button>
              {open === r.version ? (
                <dl className="mt-2 grid gap-0.5 text-muted">
                  <div>Название: {r.snapshot.title}</div>
                  <div>Скидка: {discountText(r.snapshot.discount, r.snapshot.currency)}</div>
                  <div>Период: {fmtPeriod(r.snapshot.startsAt, r.snapshot.endsAt, tz)}</div>
                  <div>Приоритет: {r.snapshot.priority}</div>
                  <div>Статус: {({ draft: "черновик", active: "активна", completed: "завершена" } as const)[r.snapshot.status]}</div>
                </dl>
              ) : null}
            </li>
          ))}
        </ol>
      )}
    </Surface>
  );
}
