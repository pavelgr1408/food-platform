import { useCallback, useEffect, useSyncExternalStore } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { isApiError, type Cart, type CartItemInput, type ContactInfo } from "@fp/api";
import { useApi, useHasSession } from "../app/api-context";
import { localCart, type LocalCart } from "./local";

/* Мини-стор: cartId живёт в localStorage (гость владеет корзиной по знанию cartId, UC-CRT-01/09) */
const listeners = new Set<() => void>();
let snapshot: LocalCart | null = typeof window === "undefined" ? null : localCart.get();
function setLocal(c: LocalCart | null) {
  snapshot = c;
  if (c) localCart.set(c); else localCart.clear();
  listeners.forEach((l) => l());
}
const subscribe = (cb: () => void) => { listeners.add(cb); return () => void listeners.delete(cb); };
export const getLocalCart = () => snapshot;
/** Для тестов: сбросить стор из localStorage. */
export function resetCartStore() { snapshot = localCart.get(); listeners.forEach((l) => l()); }

export const lineKey = (l: Pick<CartItemInput, "menuItemId" | "modifierIds">) =>
  `${l.menuItemId}|${[...(l.modifierIds ?? [])].sort().join(",")}`;

export const toInputs = (cart: Cart | undefined): CartItemInput[] =>
  (cart?.items ?? []).map((i) => ({ menuItemId: i.menuItemId, quantity: i.quantity, modifierIds: i.modifierIds }));

export function useCart() {
  const api = useApi();
  const qc = useQueryClient();
  const has = useHasSession();
  const local = useSyncExternalStore(subscribe, () => snapshot, () => null);

  const q = useQuery({
    queryKey: ["cart", local?.cartId],
    queryFn: () => api.carts.get(local!.cartId),
    enabled: !!local,
    retry: false,
  });

  // UC-CRT-09: 404 — корзина удалена (TTL 7 суток); status=ordered — корзина больше не рабочая
  useEffect(() => {
    if (q.error && isApiError(q.error) && q.error.status === 404) setLocal(null);
  }, [q.error]);

  const put = useCallback((cart: Cart) => qc.setQueryData(["cart", cart.id], cart), [qc]);

  /** Гарантирует открытую корзину этого ресторана. Другой ресторан = новая корзина (подтверждение — на вызывающем). */
  const ensure = useCallback(async (restaurantId: string, contact?: ContactInfo): Promise<string> => {
    const cur = snapshot;
    if (cur && cur.restaurantId === restaurantId) return cur.cartId;
    const cart = await api.carts.create(restaurantId, { guest: !has, contact });
    setLocal({ cartId: cart.id, restaurantId });
    put(cart);
    return cart.id;
  }, [api, has, put]);

  /** Замена полного состава. ITEMS_UNAVAILABLE: корзина уже сохранена без недоступных — перечитываем и пробрасываем. */
  const setLines = useCallback(async (cartId: string, lines: CartItemInput[]): Promise<Cart> => {
    try {
      const cart = await api.carts.putItems(cartId, lines);
      put(cart);
      return cart;
    } catch (e) {
      if (isApiError(e)) {
        if (e.code === "ITEMS_UNAVAILABLE") await qc.invalidateQueries({ queryKey: ["cart", cartId] });
        if (e.code === "CART_CLOSED" || e.code === "CART_NOT_FOUND") setLocal(null);
      }
      throw e;
    }
  }, [api, put, qc]);

  const addItem = useCallback(async (restaurantId: string, add: CartItemInput) => {
    const cartId = await ensure(restaurantId);
    const current = toInputs(qc.getQueryData<Cart>(["cart", cartId]));
    const key = lineKey(add);
    const merged = current.some((l) => lineKey(l) === key)
      ? current.map((l) => (lineKey(l) === key ? { ...l, quantity: Math.min(99, l.quantity + add.quantity) } : l))
      : [...current, add];
    return setLines(cartId, merged);
  }, [ensure, qc, setLines]);

  const count = (q.data?.items ?? []).reduce((n, i) => n + i.quantity, 0);

  return {
    cartId: local?.cartId, restaurantId: local?.restaurantId,
    cart: q.data, isLoading: !!local && q.isLoading, error: q.error,
    count, ensure, setLines, addItem,
    startNew: () => setLocal(null),
    forget: () => setLocal(null),
    refetch: q.refetch,
  };
}
