import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { Principal, Role } from "@fp/api";
import { useApi, useHasSession } from "@fp/ui";
import { clearStaffDeviceData } from "../lib/local";

export interface StaffAuth {
  /** loading — токен есть, профиль грузится; guest — токена нет или профиль недоступен. */
  status: "loading" | "guest" | "user";
  principal?: Principal;
  /** Роль персонала: касса и заказы по факсу. */
  isStaff: boolean;
  /** Управление акциями: владелец франшизы (свой ресторан) и HQ (включая национальные). */
  canPromotions: boolean;
  isHq: boolean;
  logout: () => Promise<void>;
}

const STAFF: Role[] = ["STAFF", "FRANCHISE_OWNER", "HQ_STAFF"];

export function useStaffAuth(): StaffAuth {
  const api = useApi();
  const qc = useQueryClient();
  const has = useHasSession();
  const me = useQuery({ queryKey: ["me"], queryFn: () => api.auth.me(), enabled: has, retry: false, staleTime: 60_000 });
  const status: StaffAuth["status"] = !has ? "guest" : me.data ? "user" : me.isError ? "guest" : "loading";
  const roles = (has ? me.data?.roles : undefined) ?? [];
  return {
    status,
    principal: has ? me.data : undefined,
    isStaff: roles.some((r) => STAFF.includes(r)),
    canPromotions: roles.includes("FRANCHISE_OWNER") || roles.includes("HQ_STAFF"),
    isHq: roles.includes("HQ_STAFF"),
    async logout() {
      await api.auth.logout();
      clearStaffDeviceData();
      qc.clear(); // общий терминал: ни заказов клиентов, ни акций в кэше после выхода
    },
  };
}
