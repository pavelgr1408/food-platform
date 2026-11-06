
import type { TokenPair } from "./types";
export type { TokenPair };

export interface KeyValueStorage {
  getItem(k: string): string | null;
  setItem(k: string, v: string): void;
  removeItem(k: string): void;
}

/** localStorage может бросать (приватный режим) — тогда сессия живёт только в памяти. */
export function safeStorage(s: KeyValueStorage | undefined): KeyValueStorage {
  const mem = new Map<string, string>();
  const memory: KeyValueStorage = {
    getItem: (k) => mem.get(k) ?? null,
    setItem: (k, v) => void mem.set(k, v),
    removeItem: (k) => void mem.delete(k),
  };
  if (!s) return memory;
  return {
    getItem: (k) => { try { return s.getItem(k); } catch { return memory.getItem(k); } },
    setItem: (k, v) => { try { s.setItem(k, v); } catch { memory.setItem(k, v); } },
    removeItem: (k) => { try { s.removeItem(k); } catch { memory.removeItem(k); } },
  };
}

const REFRESH_KEY = "fp.refreshToken";
/** Обновляем access за минуту до истечения (UC-ACC-05). */
const SKEW_MS = 60_000;

/**
 * Хранит пару токенов: access — только в памяти (UC-ACC-04), refresh — в storage.
 * Refresh одноразовый, поэтому обновление — single-flight: параллельные запросы ждут один и тот же промис,
 * иначе второй получит INVALID_REFRESH_TOKEN.
 */
export class Session {
  private access?: string;
  private expiresAt = 0;
  private inflight?: Promise<boolean>;
  private readonly listeners = new Set<() => void>();
  private readonly storage: KeyValueStorage;

  constructor(
    private readonly refreshFn: (refreshToken: string) => Promise<TokenPair>,
    storage?: KeyValueStorage,
    private readonly now: () => number = Date.now,
  ) {
    this.storage = safeStorage(storage);
  }

  get refreshToken(): string | null { return this.storage.getItem(REFRESH_KEY); }
  get hasSession(): boolean { return !!this.access || !!this.refreshToken; }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => void this.listeners.delete(fn);
  }
  private emit() { this.listeners.forEach((l) => l()); }

  setTokens(p: TokenPair): void {
    this.access = p.accessToken;
    this.expiresAt = this.now() + p.expiresIn * 1000;
    this.storage.setItem(REFRESH_KEY, p.refreshToken);
    this.emit();
  }

  clear(): void {
    this.access = undefined;
    this.expiresAt = 0;
    this.storage.removeItem(REFRESH_KEY);
    this.emit();
  }

  private fresh(): boolean { return !!this.access && this.now() < this.expiresAt - SKEW_MS; }

  /** Актуальный access; при необходимости тихо обновляет. undefined — гость или сессия умерла. */
  async getAccessToken(): Promise<string | undefined> {
    if (this.fresh()) return this.access;
    if (this.refreshToken) {
      await this.refresh();
      return this.access;
    }
    return this.access && this.now() < this.expiresAt ? this.access : undefined;
  }

  /** true — получена новая пара; false — сессия очищена (refresh отвергнут). */
  refresh(): Promise<boolean> {
    if (this.inflight) return this.inflight;
    const rt = this.refreshToken;
    if (!rt) return Promise.resolve(false);
    this.inflight = (async () => {
      try {
        this.setTokens(await this.refreshFn(rt));
        return true;
      } catch (e) {
        const status = (e as { status?: number; network?: boolean }) ?? {};
        // сеть/5xx — сессию не трогаем (refresh мог не быть потрачен); 4xx — токен мёртв
        if (!status.network && (status.status ?? 0) < 500) this.clear();
        return false;
      } finally {
        this.inflight = undefined;
      }
    })();
    return this.inflight;
  }
}
