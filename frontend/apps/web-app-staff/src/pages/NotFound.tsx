import { Link } from "react-router";
import { Empty } from "@fp/ui";

export function NotFound() {
  return (
    <Empty page title="Такой страницы нет">
      <Link to="/" className="inline-block rounded-full bg-lemon px-6 py-3 font-semibold text-ink">На кассу</Link>
    </Empty>
  );
}
