import { describe, expect, it, vi } from "vitest";
import { createHttp } from "./http";
import { ApiError } from "./problem";
import { Session } from "./session";

const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json", ...headers } });
const pair = (n: number) => ({ accessToken: `a${n}`, refreshToken: `r${n}`, tokenType: "Bearer" as const, expiresIn: 900 });

describe("Session", () => {
  it("single-flight: параллельные обновления тратят refresh один раз", async () => {
    const refreshFn = vi.fn(async () => { await Promise.resolve(); return pair(2); });
    const s = new Session(refreshFn);
    s.setTokens({ ...pair(1), expiresIn: 30 }); // < SKEW → нужно обновление
    const toks = await Promise.all([s.getAccessToken(), s.getAccessToken(), s.getAccessToken()]);
    expect(toks).toEqual(["a2", "a2", "a2"]);
    expect(refreshFn).toHaveBeenCalledTimes(1);
    expect(refreshFn).toHaveBeenCalledWith("r1");
  });
  it("4xx на refresh очищает сессию, сеть — нет", async () => {
    const dead = new Session(async () => { throw new ApiError({ status: 401, code: "INVALID_REFRESH_TOKEN" }); });
    dead.setTokens(pair(1));
    expect(await dead.refresh()).toBe(false);
    expect(dead.hasSession).toBe(false);

    const offline = new Session(async () => { throw new ApiError({ status: 0, network: true }); });
    offline.setTokens(pair(1));
    expect(await offline.refresh()).toBe(false);
    expect(offline.refreshToken).toBe("r1");
  });
});

describe("http", () => {
  it("разбирает problem+json: code, extra, Retry-After", async () => {
    const fetchImpl = vi.fn(async () => json(409, { code: "ITEMS_UNAVAILABLE", title: "t", unavailableItems: [{ menuItemId: "x" }] }, { "Retry-After": "3" }));
    const http = createHttp({ baseUrl: "", session: new Session(async () => pair(1)), fetchImpl });
    const err = await http.request("PUT", "/carts/1/items", { body: {} }).catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.code).toBe("ITEMS_UNAVAILABLE");
    expect(err.extra.unavailableItems).toHaveLength(1);
    expect(err.retryAfter).toBe(3);
  });
  it("401 с токеном → refresh → повтор запроса", async () => {
    const session = new Session(async () => pair(2));
    session.setTokens(pair(1));
    const seen: (string | null)[] = [];
    const fetchImpl = vi.fn(async (_u: RequestInfo | URL, init?: RequestInit) => {
      const auth = new Headers(init?.headers).get("Authorization");
      seen.push(auth);
      return auth === "Bearer a2" ? json(200, { ok: 1 }) : json(401, { code: "INVALID_TOKEN" });
    });
    const http = createHttp({ baseUrl: "", session, fetchImpl });
    const r = await http.request<{ ok: number }>("GET", "/auth/me", { auth: "required" });
    expect(r.data.ok).toBe(1);
    expect(seen).toEqual(["Bearer a1", "Bearer a2"]);
  });
  it("гость: без токена, Idempotency-Key и query", async () => {
    const fetchImpl = vi.fn(async (_u: RequestInfo | URL, _i?: RequestInit) => json(201, { id: "c" }));
    const http = createHttp({ baseUrl: "http://gw", session: new Session(async () => pair(1)), fetchImpl });
    await http.request("POST", "/orders", { body: { a: 1 }, idempotencyKey: "k1", query: { x: 1, y: undefined, z: false } });
    const [u, init] = fetchImpl.mock.calls[0]!;
    expect(String(u)).toBe("http://gw/orders?x=1&z=false");
    const h = new Headers(init?.headers);
    expect(h.get("Idempotency-Key")).toBe("k1");
    expect(h.get("Authorization")).toBeNull();
  });
  it("сеть → ApiError.network", async () => {
    const http = createHttp({ baseUrl: "", session: new Session(async () => pair(1)), fetchImpl: async () => { throw new TypeError("fail"); } });
    const err = await http.request("GET", "/x").catch((e) => e);
    expect(err.network).toBe(true);
    expect(err.code).toBe("NETWORK_ERROR");
  });
});
