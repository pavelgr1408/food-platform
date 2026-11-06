import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// globals: false → автоочистка RTL не подключается сама
afterEach(() => cleanup());
