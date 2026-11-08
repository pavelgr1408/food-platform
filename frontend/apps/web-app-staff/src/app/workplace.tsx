import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import type { RestaurantDetails } from "@fp/api";
import { useApi } from "@fp/ui";
import { useStaffAuth } from "./auth";
import { localWorkplace } from "../lib/local";

/** Рабочий ресторан смены (UC-ACC-08). Доступ к чужому ресторану всё равно закрыт на сервере. */
interface Workplace {
  loading: boolean;
  restaurants: RestaurantDetails[];
  current: RestaurantDetails | undefined;
  select: (id: string) => void;
}
const Ctx = createContext<Workplace | null>(null);

export function WorkplaceProvider({ children }: { children: ReactNode }) {
  const api = useApi();
  const auth = useStaffAuth();
  const bound = auth.principal?.restaurants ?? [];
  const hq = auth.isHq;

  // HQ не привязан к ресторанам: выбирает из всего каталога. Остальные — из привязки в токене.
  const all = useQuery({ queryKey: ["restaurants", "all"], queryFn: () => api.restaurants.search({ limit: 100 }), enabled: hq, staleTime: 5 * 60_000, retry: false });
  const ids = hq ? (all.data?.items ?? []).map((r) => r.id) : bound;
  const details = useQueries({
    queries: ids.map((id) => ({ queryKey: ["restaurant", id], queryFn: () => api.restaurants.get(id), staleTime: 5 * 60_000, retry: false })),
  });
  const restaurants = useMemo(() => details.flatMap((d) => (d.data ? [d.data] : [])), [details]);
  const loading = (hq && all.isLoading) || details.some((d) => d.isLoading);

  const [picked, setPicked] = useState<string | null>(() => localWorkplace.get());
  const current = restaurants.find((r) => r.id === picked) ?? restaurants[0];
  useEffect(() => { if (current && current.id !== picked) setPicked(current.id); }, [current, picked]);

  const select = useCallback((id: string) => { localWorkplace.set(id); setPicked(id); }, []);
  const value = useMemo(() => ({ loading, restaurants, current, select }), [loading, restaurants, current, select]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useWorkplace(): Workplace {
  const v = useContext(Ctx);
  if (!v) throw new Error("useWorkplace вне WorkplaceProvider");
  return v;
}
