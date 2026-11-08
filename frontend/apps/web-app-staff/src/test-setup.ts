import "@testing-library/jest-dom/vitest";
import { cleanup, configure } from "@testing-library/react";
import { afterEach } from "vitest";

// findBy*/waitFor по умолчанию ждут 1 с: на загруженном CI страница с запросами не успевает отрисоваться
configure({ asyncUtilTimeout: 8000 });

// globals: false → автоочистка RTL не подключается сама
afterEach(() => cleanup());
window.scrollTo = () => undefined;
