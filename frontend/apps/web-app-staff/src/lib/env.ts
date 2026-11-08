export const API_MODE: "mock" | "real" = import.meta.env.VITE_API_MODE === "mock" ? "mock" : "real";
export const API_BASE: string = (import.meta.env.VITE_API_BASE_URL as string | undefined) ?? "/api";
