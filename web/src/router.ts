import { signal } from "@preact/signals";

export type Route =
  | { name: "list" }
  | { name: "detail"; code: string };

export const route = signal<Route>(parse());

function parse(): Route {
  const h = location.hash.replace(/^#/, "");
  const m = /^\/coin\/([A-Za-z0-9_-]+)$/.exec(h);
  if (m) return { name: "detail", code: m[1].toUpperCase() };
  return { name: "list" };
}

export function startRouter(): void {
  window.addEventListener("hashchange", () => { route.value = parse(); });
}

export function back(): void {
  if (history.length > 1) history.back();
  else location.hash = "";
}
