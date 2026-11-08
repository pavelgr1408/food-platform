import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { isApiError, type Cart, type CartItemInput } from "@fp/api";
import { useApi } from "@fp/ui";
import { localFax } from "./local";

export const lineKey = (l: Pick<CartItemInput, "menuItemId" | "modifierIds">) => `${l.menuItemId}|${[...(l.modifierIds ?? [])].sort().join(",")}`;
export const toInputs = (cart: Cart | undefined): CartItemInput[] =>
  (cart?.items ?? []).map((i) => ({ menuItemId: i.menuItemId, quantity: i.quantity, modifierIds: i.modifierIds }));

/**
 * Корзина заказа по факсу (UC-STF-02). Серверная, гостевая (`guest:true`): сотрудник владеет ею по знанию cartId,
 * который помним на устройстве, чтобы перезагрузка страницы посреди звонка не теряла заказ.
 * PUT /items заменяет состав целиком, поэтому изменения идут строго по очереди (иначе быстрые клики теряют позиции).
 */
export function useFaxCart(restaurantId: string) {
  const api = useApi();
  const qc = useQueryClient();
  const read = () => { const d = localFax.get(); return d && d.restaurantId === restaurantId ? d.cartId : null; };
  const [cartId, setCartId] = useState<string | null>(read);
  // смена рабочего ресторана — другая корзина
  useEffect(() => { setCartId(read()); }, [restaurantId]); // eslint-disable-line react-hooks/exhaustive-deps

  const q = useQuery({ queryKey: ["fax-cart", cartId], queryFn: () => api.carts.get(cartId!), enabled: !!cartId, retry: false });
  const forget = useCallback(() => { localFax.clear(); setCartId(null); }, []);
  // 404 (TTL 7 суток) или уже оформлена — корзина больше не рабочая
  useEffect(() => {
    if ((q.error && isApiError(q.error) && q.error.status === 404) || q.data?.status === "ordered") forget();
  }, [q.error, q.data?.status, forget]);

  const chain = useRef<Promise<unknown>>(Promise.resolve());
  const creating = useRef<Promise<string> | null>(null);
  const enqueue = useCallback(<T,>(task: () => Promise<T>): Promise<T> => {
    const run = chain.current.then(task, task);
    chain.current = run.catch(() => undefined);
    return run;
  }, []);

  const ensure = useCallback(async (): Promise<string> => {
    const cur = read();
    if (cur) return cur;
    if (!creating.current) {
      creating.current = api.carts.create(restaurantId, { guest: true }).then((c) => {
        localFax.set({ cartId: c.id, restaurantId });
        setCartId(c.id);
        qc.setQueryData(["fax-cart", c.id], c);
        return c.id;
      }).finally(() => { creating.current = null; });
    }
    return creating.current;
  }, [api, qc, restaurantId]); // eslint-disable-line react-hooks/exhaustive-deps

  /** fn получает актуальные строки (с учётом предыдущих кликов) и возвращает новые. */
  const update = useCallback((fn: (cur: CartItemInput[]) => CartItemInput[]) => enqueue(async () => {
    const id = await ensure();
    try {
      const cart = await api.carts.putItems(id, fn(toInputs(qc.getQueryData<Cart>(["fax-cart", id]))));
      qc.setQueryData(["fax-cart", id], cart);
      return cart;
    } catch (e) {
      if (isApiError(e)) {
        // корзина уже сохранена без недоступных позиций — перечитываем
        if (e.code === "ITEMS_UNAVAILABLE") await qc.invalidateQueries({ queryKey: ["fax-cart", id] });
        if (e.code === "CART_CLOSED" || e.code === "CART_NOT_FOUND") forget();
      }
      throw e;
    }
  }), [api, enqueue, ensure, forget, qc]);

  const add = useCallback((line: CartItemInput) => update((cur) => {
    const key = lineKey(line);
    return cur.some((l) => lineKey(l) === key)
      ? cur.map((l) => (lineKey(l) === key ? { ...l, quantity: Math.min(99, l.quantity + line.quantity) } : l))
      : [...cur, line];
  }), [update]);

  /** Сдвиг количества строки по ключу, а не по индексу: пока летит прошлый запрос, индексы устарели. Ноль убирает строку. */
  const bump = useCallback((key: string, delta: number) => update((cur) => cur.flatMap((l) => {
    if (lineKey(l) !== key) return [l];
    const n = Math.min(99, l.quantity + delta);
    return n > 0 ? [{ ...l, quantity: n }] : [];
  })), [update]);

  return { cartId, cart: q.data, isLoading: !!cartId && q.isLoading, add, bump, update, forget, refetch: q.refetch };
}
