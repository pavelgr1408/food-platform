/** application/problem+json (RFC 9457). Фронт ветвится по `code`, а не по тексту (FRONTEND-CONTRACT, «Соглашения»). */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly title?: string;
  readonly detail?: string;
  /** Доп. поля problem: unavailableItems, paymentId, distanceMeters, hint, conflictingPromotionIds … */
  readonly extra: Record<string, unknown>;
  /** Секунды из Retry-After (429, 404 с ожиданием), если заголовок есть. */
  readonly retryAfter?: number;
  /** Нет ответа: сеть/CORS/таймаут. */
  readonly network: boolean;

  constructor(init: { status: number; code?: string; title?: string; detail?: string; extra?: Record<string, unknown>; retryAfter?: number; network?: boolean }) {
    super(init.detail ?? init.title ?? init.code ?? `HTTP ${init.status}`);
    this.name = "ApiError";
    this.status = init.status;
    this.code = init.code ?? (init.network ? "NETWORK_ERROR" : `HTTP_${init.status}`);
    this.title = init.title;
    this.detail = init.detail;
    this.extra = init.extra ?? {};
    this.retryAfter = init.retryAfter;
    this.network = init.network ?? false;
  }

  is(...codes: string[]): boolean {
    return codes.includes(this.code);
  }
}

export const isApiError = (e: unknown): e is ApiError => e instanceof ApiError;

export async function problemFromResponse(res: Response): Promise<ApiError> {
  let body: Record<string, unknown> = {};
  try {
    const text = await res.text();
    if (text) body = JSON.parse(text) as Record<string, unknown>;
  } catch {
    /* не JSON — оставляем пустое тело */
  }
  const { code, title, detail, status: _s, type: _t, instance: _i, ...extra } = body as Record<string, unknown>;
  const ra = Number(res.headers.get("Retry-After"));
  return new ApiError({
    status: res.status,
    code: typeof code === "string" ? code : undefined,
    title: typeof title === "string" ? title : undefined,
    detail: typeof detail === "string" ? detail : undefined,
    extra,
    retryAfter: Number.isFinite(ra) && ra > 0 ? ra : undefined,
  });
}
