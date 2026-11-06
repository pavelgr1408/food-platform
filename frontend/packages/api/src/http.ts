import { ApiError, problemFromResponse } from "./problem";
import type { Session } from "./session";

export type AuthMode = "none" | "optional" | "required";

export interface HttpOptions {
  query?: Record<string, string | number | boolean | undefined | null>;
  body?: unknown;
  headers?: Record<string, string>;
  /** none — без токена; optional — токен, если есть (гость допускается); required — токен нужен. */
  auth?: AuthMode;
  idempotencyKey?: string;
  signal?: AbortSignal;
}

export interface HttpResult<T> { data: T; status: number; headers: Headers }

export interface Http {
  request<T>(method: string, path: string, o?: HttpOptions): Promise<HttpResult<T>>;
}

export interface HttpConfig {
  /** Пусто — относительные пути (dev-proxy Vite / тот же origin). Иначе абсолютный URL gateway. */
  baseUrl: string;
  session: Session;
  fetchImpl?: typeof fetch;
}

export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}

export function createHttp(cfg: HttpConfig): Http {
  const doFetch = (...a: Parameters<typeof fetch>) => (cfg.fetchImpl ?? fetch)(...a);

  function url(path: string, query?: HttpOptions["query"]): string {
    const qs = new URLSearchParams();
    for (const [k, v] of Object.entries(query ?? {})) if (v !== undefined && v !== null && v !== "") qs.set(k, String(v));
    const s = qs.toString();
    return `${cfg.baseUrl}${path}${s ? `?${s}` : ""}`;
  }

  async function once(method: string, path: string, o: HttpOptions, token?: string): Promise<Response> {
    const headers: Record<string, string> = { Accept: "application/json, application/problem+json", ...o.headers };
    if (o.body !== undefined) headers["Content-Type"] = "application/json";
    if (o.idempotencyKey) headers["Idempotency-Key"] = o.idempotencyKey;
    if (token) headers.Authorization = `Bearer ${token}`;
    try {
      return await doFetch(url(path, o.query), {
        method,
        headers,
        body: o.body !== undefined ? JSON.stringify(o.body) : undefined,
        signal: o.signal,
      });
    } catch (e) {
      if ((e as Error).name === "AbortError") throw e;
      throw new ApiError({ status: 0, network: true, detail: "Нет связи с сервером" });
    }
  }

  async function request<T>(method: string, path: string, o: HttpOptions = {}): Promise<HttpResult<T>> {
    const mode = o.auth ?? "optional";
    let token = mode === "none" ? undefined : await cfg.session.getAccessToken();
    let res = await once(method, path, o, token);

    // 401 при отправленном токене: access мог протухнуть раньше часов (UC-ACC-05) — один тихий повтор
    if (res.status === 401 && token && mode !== "none") {
      const ok = await cfg.session.refresh();
      if (ok) {
        token = await cfg.session.getAccessToken();
        res = await once(method, path, o, token);
      }
    }

    if (!res.ok) throw await problemFromResponse(res);
    if (res.status === 204) return { data: undefined as T, status: 204, headers: res.headers };
    const text = await res.text();
    return { data: (text ? JSON.parse(text) : undefined) as T, status: res.status, headers: res.headers };
  }

  return { request };
}
