import { Link } from "react-router";
import { Empty } from "../components/ui";
export const NotFound = () => <Empty title="Страница не найдена"><Link to="/" className="font-semibold underline">На главную</Link></Empty>;
