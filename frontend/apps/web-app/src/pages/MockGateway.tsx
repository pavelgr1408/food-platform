import { useSearchParams } from "react-router";
import { Button, Surface } from "../components/ui";
import { activeDb, saveDb } from "../mocks/db";
import { settleMockPayment } from "../mocks/handlers";

/** Страница мок-шлюза (только VITE_API_MODE=mock): имитирует форму банка и возврат на returnUrl. */
export function MockGateway() {
  const [sp] = useSearchParams();
  const orderId = sp.get("orderId") ?? "";
  const back = sp.get("return") || `/orders/${orderId}`;
  const finish = (ok: boolean) => { settleMockPayment(activeDb, orderId, ok); saveDb(activeDb); window.location.assign(back); };
  return (
    <Surface className="mx-auto max-w-md">
      <h1 className="text-[length:var(--lt-fs-h2)]">Мок-шлюз оплаты</h1>
      <p className="my-3 text-muted">Имитация страницы банка. Картные данные не вводятся и никуда не отправляются.</p>
      <div className="flex flex-wrap gap-2">
        <Button variant="accent" onClick={() => finish(true)}>Оплатить успешно</Button>
        <Button variant="outline" onClick={() => finish(false)}>Отказ банка</Button>
      </div>
    </Surface>
  );
}
