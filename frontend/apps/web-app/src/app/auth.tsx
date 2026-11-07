import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useApi, useHasSession } from "./api-context";
import type { Principal } from "@fp/api";
import { clearDeviceData } from "../lib/local";
import { resetCartStore } from "../lib/cart";

export interface AuthState {
  /** loading — токен есть, профиль грузится; guest — токена нет. */
  status: "loading" | "guest" | "user";
  principal?: Principal;
  isStaff: boolean;
  logout: () => Promise<void>;
}

const STAFF_ROLES = ["STAFF", "FRANCHISE_OWNER", "HQ_STAFF"];

export function useAuth(): AuthState {
  const api = useApi();
  const qc = useQueryClient();
  const has = useHasSession();
  const me = useQuery({ queryKey: ["me"], queryFn: () => api.auth.me(), enabled: has, retry: false, staleTime: 60_000 });
  const status: AuthState["status"] = !has ? "guest" : me.data ? "user" : me.isError ? "guest" : "loading";
  return {
    status,
    principal: has ? me.data : undefined,
    isStaff: !!me.data?.roles.some((r) => STAFF_ROLES.includes(r)),
    async logout() {
      await api.auth.logout();
      clearDeviceData();
      resetCartStore();
      qc.removeQueries({ queryKey: ["order"] });
      qc.removeQueries({ queryKey: ["cart"] });
      qc.removeQueries({ queryKey: ["me"] });
      qc.removeQueries({ queryKey: ["devices"] });
    },
  };
}
