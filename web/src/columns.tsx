import type { CoinRow, DeltaWindow, Num } from "./types";
import * as fmt from "./format";

// A delta window IS a column. The requirements listed "which columns are
// visible" and "which delta windows shown" as two settings; treating them as one
// gives a single visible-map, a single order array and a single reorder UI.

// No liquidity column. Verified against the live API: neither /coins/list nor
// /coins/map returns the field at all, and only /coins/single does — one credit
// per coin per refresh. It is shown in the coin detail view instead, where that
// endpoint is already being called.
export type ColumnId =
  | "rank" | "coin" | "price" | "cap" | "volume"
  | "d_hour" | "d_day" | "d_week" | "d_month" | "d_quarter" | "d_year"
  | "volCap" | "supply" | "ath" | "fromAth" | "age" | "exchanges";

export type Group = "identity" | "market" | "change" | "supply";

export interface RenderCtx { locale: string; currency: string }

export interface ColumnDef {
  id: ColumnId;
  label: string;
  short: string;
  group: Group;
  align: "left" | "right";
  width: number;
  locked?: boolean;
  defaultVisible: boolean;
  sortable: boolean;
  /** Sort key. null always sorts last, whichever direction. */
  value(c: CoinRow): Num;
  /** Rendered text, or a node for the composite cells. */
  text?(c: CoinRow, ctx: RenderCtx): string;
  /** The API sort field this column maps to, if any. Absent means market-scope
   *  sorting is impossible: the API cannot sort by a delta window. */
  apiSort?: string;
  deltaWindow?: DeltaWindow;
}

function deltaColumn(id: ColumnId, w: DeltaWindow, label: string, visible: boolean): ColumnDef {
  return {
    id, label, short: label, group: "change", align: "right",
    // 104px, not their 90px: a real year delta reads +1593.99% and would clip.
    width: 104,
    defaultVisible: visible,
    sortable: true,
    deltaWindow: w,
    value: (c) => c.changePct[w],
  };
}

export const ALL_COLUMNS: ColumnDef[] = [
  {
    id: "rank", label: "#", short: "#", group: "identity", align: "left",
    width: 68, locked: true, defaultVisible: true, sortable: true,
    apiSort: "rank",
    value: (c) => (c.rank > 0 ? c.rank : null),
  },
  {
    id: "coin", label: "Coin", short: "Coin", group: "identity", align: "left",
    width: 150, locked: true, defaultVisible: true, sortable: true,
    apiSort: "name",
    value: () => null,
  },
  {
    id: "price", label: "Price", short: "Price", group: "market", align: "right",
    width: 120, locked: true, defaultVisible: true, sortable: true,
    apiSort: "price",
    value: (c) => c.rate,
    text: (c, x) => fmt.money(c.rate, x.locale, x.currency),
  },
  {
    id: "cap", label: "Market Cap", short: "Cap", group: "market", align: "right",
    width: 118, defaultVisible: true, sortable: true,
    value: (c) => c.cap,
    text: (c, x) => fmt.compactMoney(c.cap, x.locale, x.currency),
  },
  {
    id: "volume", label: "Volume 24h", short: "Vol 24h", group: "market", align: "right",
    width: 112, defaultVisible: true, sortable: true,
    apiSort: "volume",
    value: (c) => c.volume,
    text: (c, x) => fmt.compactMoney(c.volume, x.locale, x.currency),
  },

  deltaColumn("d_hour", "hour", "1h", true),
  { ...deltaColumn("d_day", "day", "24h", true), locked: true },
  deltaColumn("d_week", "week", "7d", true),
  deltaColumn("d_month", "month", "30d", true),
  deltaColumn("d_quarter", "quarter", "90d", true),
  deltaColumn("d_year", "year", "1y", true),

  {
    id: "volCap", label: "Volume / Cap", short: "Vol/Cap", group: "market", align: "right",
    width: 110, defaultVisible: false, sortable: true,
    value: (c) => (c.volume !== null && c.cap !== null && c.cap > 0 ? c.volume / c.cap : null),
    text: (c, x) => {
      const v = c.volume !== null && c.cap !== null && c.cap > 0 ? (c.volume / c.cap) * 100 : null;
      return v === null ? fmt.DASH : fmt.percent(v, x.locale).replace("+", "");
    },
  },
  {
    id: "supply", label: "Circulating supply", short: "Supply", group: "supply", align: "right",
    width: 130, defaultVisible: false, sortable: true,
    value: (c) => c.circulatingSupply,
    text: (c, x) => fmt.compactCount(c.circulatingSupply, x.locale),
  },
  {
    id: "ath", label: "All-time high", short: "ATH", group: "supply", align: "right",
    width: 120, defaultVisible: false, sortable: true,
    value: (c) => c.allTimeHighUSD,
    // allTimeHighUSD is always USD regardless of the display currency.
    text: (c, x) => fmt.money(c.allTimeHighUSD, x.locale, "USD"),
  },
  {
    id: "fromAth", label: "% from ATH", short: "From ATH", group: "supply", align: "right",
    width: 110, defaultVisible: false, sortable: true,
    value: (c) => c.fromAthPct,
  },
  {
    id: "age", label: "Age", short: "Age", group: "supply", align: "right",
    width: 90, defaultVisible: false, sortable: true,
    apiSort: "age",
    value: (c) => (c.age > 0 ? c.age : null),
    text: (c, x) => fmt.days(c.age, x.locale),
  },
  {
    id: "exchanges", label: "Exchanges", short: "Exch", group: "supply", align: "right",
    width: 100, defaultVisible: false, sortable: true,
    value: (c) => (c.exchanges > 0 ? c.exchanges : null),
    text: (c, x) => fmt.count(c.exchanges > 0 ? c.exchanges : null, x.locale),
  },
];

export const DEFAULT_ORDER: ColumnId[] = ALL_COLUMNS.map((c) => c.id);

const byId = new Map<ColumnId, ColumnDef>(ALL_COLUMNS.map((c) => [c.id, c]));

export function columnDef(id: ColumnId): ColumnDef {
  const d = byId.get(id);
  if (!d) throw new Error(`unknown column ${id}`);
  return d;
}

export function isColumn(id: unknown): id is ColumnId {
  return typeof id === "string" && byId.has(id as ColumnId);
}

export const GROUP_LABELS: Record<Group, string> = {
  identity: "Identity",
  market: "Market",
  change: "Change",
  supply: "Supply and history",
};

/** The container-query width below which a column is hidden by the overlay. */
export const HIDE_BELOW: Partial<Record<ColumnId, number>> = {
  d_year: 1420,
  d_quarter: 1330,
  d_month: 1160,
  volume: 1080,
  d_week: 1000,
  d_hour: 920,
  age: 860,
  ath: 860,
  fromAth: 860,
  supply: 860,
};
