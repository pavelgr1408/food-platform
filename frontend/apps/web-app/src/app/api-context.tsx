import { createContext, useContext, useMemo, useSyncExternalStore, type ReactNode } from "react";
import { createApi, type Api } from "@fp/api";

const Ctx = createContext<Api | null>(null);

export function ApiProvider({ api, children }: { api?: Api; children: ReactNode }) {
  const value = useMemo(() => api, [api]);
  if (!value) throw new Error("ApiProvider: api is required");
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useApi(): Api {
  const a = useContext(Ctx);
  if (!a) throw new Error("useApi outside ApiProvider");
  return a;
}

export function makeApi(baseUrl: string, storage?: Storage): Api {
  return createApi({ baseUrl, storage });
}

/** Есть ли сессия (access или refresh) — реактивно. */
export function useHasSession(): boolean {
  const { session } = useApi();
  return useSyncExternalStore(
    (cb) => session.subscribe(cb),
    () => session.hasSession,
    () => false,
  );
}
