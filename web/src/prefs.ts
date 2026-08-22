import { signal, effect } from "@preact/signals";
import type { Density, Theme, View } from "./types";
import { ALL_COLUMNS, DEFAULT_ORDER, type ColumnId, isColumn, columnDef } from "./columns";

export const PREFS_KEY = "lcwd:prefs";
export const PREFS_VERSION = 1;

export interface Preset { name: string; visible: Partial<Record<ColumnId, boolean>>; order: ColumnId[] }

export interface Prefs {
  v: number;
  theme: Theme;
  locale: string;
  currency: string;
  view: View;
  density: Density;
  sortCol: ColumnId;
  sortDir: "asc" | "desc";
  sortScope: "page" | "market";
  pageSize: number;
  columns: { visible: Partial<Record<ColumnId, boolean>>; order: ColumnId[] };
  presets: Preset[];
  hideStablecoins: boolean;
  showAllColumns: boolean;
  chartRange: string;
  // A boot cache only. The server is authoritative and its frame wins on arrival.
  watchlist: string[];
  notifRequested: boolean;
}

export const PAGE_SIZES = [10, 25, 50, 100];

export function defaults(): Prefs {
  const visible: Partial<Record<ColumnId, boolean>> = {};
  for (const id of DEFAULT_ORDER) visible[id] = columnDef(id).defaultVisible;
  return {
    v: PREFS_VERSION,
    theme: "auto",
    locale: "en-US",
    currency: "USD",
    view: "top",
    density: "relaxed",
    sortCol: "rank",
    sortDir: "asc",
    sortScope: "page",
    pageSize: 50,
    columns: { visible, order: [...DEFAULT_ORDER] },
    presets: [],
    hideStablecoins: false,
    showAllColumns: false,
    chartRange: "30d",
    watchlist: [],
    notifRequested: false,
  };
}

type Migration = (o: Record<string, unknown>) => Record<string, unknown>;

// Adding or removing a column needs no migration: reconcile handles it. Bump
// PREFS_VERSION only for a genuine shape change.
const MIGRATIONS: Record<number, Migration> = {};

export function loadPrefs(): Prefs {
  let raw: Record<string, unknown> | null = null;
  try {
    raw = JSON.parse(localStorage.getItem(PREFS_KEY) ?? "null");
  } catch {
    return recover("corrupt", null);
  }
  if (!raw || typeof raw.v !== "number") return defaults();

  if (raw.v > PREFS_VERSION) return recover("newer version", raw);
  for (let v = raw.v as number; v < PREFS_VERSION; v++) {
    const m = MIGRATIONS[v];
    if (!m) return recover("missing migration", raw);
    try {
      raw = m(raw);
    } catch {
      return recover("migration failed", raw);
    }
  }
  return reconcile(raw as unknown as Prefs);
}

function recover(reason: string, raw: Record<string, unknown> | null): Prefs {
  // Never destroy settings silently.
  if (raw) {
    try {
      localStorage.setItem(`${PREFS_KEY}.bak.v${raw.v}`, JSON.stringify(raw));
    } catch { /* quota */ }
  }
  console.warn(`lcw-dashboard: settings reset (${reason}); previous settings backed up`);
  return defaults();
}

/**
 * reconcile is what makes most schema changes free. It drops columns the code no
 * longer has, splices new ones in at their default-order position rather than
 * appending, and clamps every enum to a known value.
 */
export function reconcile(p: Prefs): Prefs {
  const d = defaults();
  const known = new Set<ColumnId>(ALL_COLUMNS.map((c) => c.id));

  let order = (p.columns?.order ?? []).filter((id) => known.has(id));
  for (const id of DEFAULT_ORDER) {
    if (order.includes(id)) continue;
    // Insert beside its default neighbour, so a new column lands where it
    // belongs instead of after the last delta.
    const want = DEFAULT_ORDER.indexOf(id);
    let at = order.length;
    for (let i = 0; i < order.length; i++) {
      if (DEFAULT_ORDER.indexOf(order[i]) > want) { at = i; break; }
    }
    order.splice(at, 0, id);
  }

  const visible: Partial<Record<ColumnId, boolean>> = {};
  for (const id of order) {
    const def = columnDef(id);
    visible[id] = def.locked ? true : (p.columns?.visible?.[id] ?? def.defaultVisible);
  }

  return {
    ...d,
    ...p,
    v: PREFS_VERSION,
    theme: ["auto", "light", "dark"].includes(p.theme) ? p.theme : d.theme,
    locale: validLocale(p.locale) ? p.locale : d.locale,
    view: p.view === "favourites" ? "favourites" : "top",
    density: ["relaxed", "compact", "dense"].includes(p.density) ? p.density : d.density,
    sortCol: isColumn(p.sortCol) ? p.sortCol : d.sortCol,
    sortDir: p.sortDir === "desc" ? "desc" : "asc",
    sortScope: p.sortScope === "market" ? "market" : "page",
    pageSize: PAGE_SIZES.includes(p.pageSize) ? p.pageSize : d.pageSize,
    columns: { visible, order },
    presets: Array.isArray(p.presets) ? p.presets.slice(0, 20) : [],
    watchlist: Array.isArray(p.watchlist)
      ? p.watchlist.filter((s) => /^[A-Z0-9_-]{1,16}$/.test(s)).slice(0, 500)
      : [],
  };
}

function validLocale(l: unknown): l is string {
  if (typeof l !== "string" || !l) return false;
  try {
    new Intl.NumberFormat(l);
    return true;
  } catch {
    return false;
  }
}

const initial = loadPrefs();

export const theme = signal<Theme>(initial.theme);
export const locale = signal(initial.locale);
export const currency = signal(initial.currency);
export const view = signal<View>(initial.view);
export const density = signal<Density>(initial.density);
export const sortCol = signal<ColumnId>(initial.sortCol);
export const sortDir = signal<"asc" | "desc">(initial.sortDir);
export const sortScope = signal<"page" | "market">(initial.sortScope);
export const pageSize = signal(initial.pageSize);
export const page = signal(1);
export const columnVisible = signal(initial.columns.visible);
export const columnOrder = signal(initial.columns.order);
export const presets = signal(initial.presets);
export const hideStablecoins = signal(initial.hideStablecoins);
export const showAllColumns = signal(initial.showAllColumns);
export const chartRange = signal(initial.chartRange);
export const watchlistCache = signal(initial.watchlist);
export const notifRequested = signal(initial.notifRequested);

function current(): Prefs {
  return {
    v: PREFS_VERSION,
    theme: theme.value,
    locale: locale.value,
    currency: currency.value,
    view: view.value,
    density: density.value,
    sortCol: sortCol.value,
    sortDir: sortDir.value,
    sortScope: sortScope.value,
    pageSize: pageSize.value,
    columns: { visible: columnVisible.value, order: columnOrder.value },
    presets: presets.value,
    hideStablecoins: hideStablecoins.value,
    showAllColumns: showAllColumns.value,
    chartRange: chartRange.value,
    watchlist: watchlistCache.value,
    notifRequested: notifRequested.value,
  };
}

let saveTimer: number | undefined;

export function startPersistence(): void {
  effect(() => {
    const snapshot = JSON.stringify(current());
    clearTimeout(saveTimer);
    saveTimer = setTimeout(() => {
      try {
        localStorage.setItem(PREFS_KEY, snapshot);
      } catch { /* private mode quota */ }
    }, 250) as unknown as number;
  });

  // Without this, two open tabs silently fight over the blob.
  window.addEventListener("storage", (e) => {
    if (e.key !== PREFS_KEY || !e.newValue) return;
    try {
      apply(reconcile(JSON.parse(e.newValue)));
    } catch { /* ignore a bad write from another tab */ }
  });

  applyTheme();
  effect(() => { applyTheme(); });
  effect(() => { document.documentElement.dataset.density = density.value; });
}

function apply(p: Prefs): void {
  theme.value = p.theme;
  locale.value = p.locale;
  currency.value = p.currency;
  view.value = p.view;
  density.value = p.density;
  sortCol.value = p.sortCol;
  sortDir.value = p.sortDir;
  sortScope.value = p.sortScope;
  pageSize.value = p.pageSize;
  columnVisible.value = p.columns.visible;
  columnOrder.value = p.columns.order;
  presets.value = p.presets;
  hideStablecoins.value = p.hideStablecoins;
  showAllColumns.value = p.showAllColumns;
  chartRange.value = p.chartRange;
}

function applyTheme(): void {
  const t = theme.value;
  if (t === "auto") {
    // Auto means no attribute, so the media query governs with no JS involved.
    delete document.documentElement.dataset.theme;
  } else {
    document.documentElement.dataset.theme = t;
  }
}

export function resolvedTheme(): "light" | "dark" {
  if (theme.value !== "auto") return theme.value;
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

export function toggleColumn(id: ColumnId): void {
  const def = columnDef(id);
  if (def.locked) return;
  columnVisible.value = { ...columnVisible.value, [id]: !columnVisible.value[id] };
}

/**
 * moveColumn reorders within a group only. Interleaving a supply column between
 * two deltas produces an unreadable table and multiplies the responsive-priority
 * problem, and group-scoped reorder covers every layout anyone actually wants.
 */
export function moveColumn(id: ColumnId, delta: number): void {
  const order = [...columnOrder.value];
  const from = order.indexOf(id);
  if (from < 0) return;
  const group = columnDef(id).group;

  let to = from + delta;
  while (to >= 0 && to < order.length && columnDef(order[to]).group !== group) {
    to += delta;
  }
  if (to < 0 || to >= order.length) return;

  order.splice(from, 1);
  order.splice(to, 0, id);
  columnOrder.value = order;
}

export function resetColumns(): void {
  const d = defaults();
  columnVisible.value = d.columns.visible;
  columnOrder.value = d.columns.order;
}

export function savePreset(name: string): void {
  const next = presets.value.filter((p) => p.name !== name);
  next.push({ name, visible: columnVisible.value, order: columnOrder.value });
  presets.value = next.slice(0, 20);
}

export function loadPreset(name: string): void {
  const p = presets.value.find((x) => x.name === name);
  if (!p) return;
  columnOrder.value = [...p.order];
  columnVisible.value = { ...p.visible };
}

export function deletePreset(name: string): void {
  presets.value = presets.value.filter((p) => p.name !== name);
}
