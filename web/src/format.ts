import type { Num } from "./types";

// Every formatter takes the locale explicitly rather than reading a global, so
// changing it re-renders through the normal signal path.

const cache = new Map<string, Intl.NumberFormat>();

function nf(locale: string, opts: Intl.NumberFormatOptions): Intl.NumberFormat {
  const key = locale + JSON.stringify(opts);
  let f = cache.get(key);
  if (!f) {
    f = new Intl.NumberFormat(locale, opts);
    cache.set(key, f);
  }
  return f;
}

export const DASH = "-";

/** Money with a price-appropriate number of decimals. */
export function money(v: Num, locale: string, currency: string): string {
  if (v === null || !Number.isFinite(v)) return DASH;
  const abs = Math.abs(v);
  // A sub-cent coin needs more decimals than a five-figure one.
  const digits = abs >= 1000 ? 2 : abs >= 1 ? 2 : abs >= 0.01 ? 4 : abs >= 0.000001 ? 6 : 8;
  try {
    return nf(locale, {
      style: "currency", currency,
      minimumFractionDigits: digits, maximumFractionDigits: digits,
    }).format(v);
  } catch {
    // A crypto code such as BTC is a valid API currency but not a valid ISO
    // currency for Intl, so fall back to a plain number plus the code.
    return nf(locale, { minimumFractionDigits: digits, maximumFractionDigits: digits })
      .format(v) + " " + currency;
  }
}

/** Abbreviated money for cap and volume columns: $1.5494 T. */
export function compactMoney(v: Num, locale: string, currency: string): string {
  if (v === null || !Number.isFinite(v)) return DASH;
  const abs = Math.abs(v);
  const units: [number, string][] = [
    [1e12, "T"], [1e9, "B"], [1e6, "M"], [1e3, "K"],
  ];
  for (const [size, suffix] of units) {
    if (abs >= size) {
      const scaled = v / size;
      const digits = Math.abs(scaled) >= 100 ? 2 : 4;
      return symbol(locale, currency) +
        nf(locale, { minimumFractionDigits: 0, maximumFractionDigits: digits }).format(scaled) +
        " " + suffix;
    }
  }
  return money(v, locale, currency);
}

const symbolCache = new Map<string, string>();

function symbol(locale: string, currency: string): string {
  const key = locale + currency;
  let s = symbolCache.get(key);
  if (s !== undefined) return s;
  try {
    const parts = nf(locale, { style: "currency", currency, minimumFractionDigits: 0 })
      .formatToParts(1);
    s = parts.find((p) => p.type === "currency")?.value ?? currency + " ";
  } catch {
    s = currency + " ";
  }
  symbolCache.set(key, s);
  return s;
}

/**
 * Percent with an explicit sign. LiveCoinWatch ships unsigned magnitude with
 * direction only in a CSS class; the sign here is a free extra channel, which
 * matters because up-green and down-red are indistinguishable under protanopia.
 *
 * Above 1000% the value is abbreviated: real year deltas reach +1593.99% and
 * would otherwise clip the column.
 */
export function percent(v: Num, locale: string): string {
  if (v === null || !Number.isFinite(v)) return DASH;
  if (Math.abs(v) >= 1000) {
    return nf(locale, {
      signDisplay: "exceptZero", minimumFractionDigits: 2, maximumFractionDigits: 2,
    }).format(v / 1000) + "k%";
  }
  return nf(locale, {
    signDisplay: "exceptZero", minimumFractionDigits: 2, maximumFractionDigits: 2,
  }).format(v) + "%";
}

/** Up, down, or none. The glyph is the encoding, so it goes in the DOM. */
export function glyph(v: Num): string {
  if (v === null || !Number.isFinite(v) || v === 0) return DASH;
  return v > 0 ? "▴" : "▾";
}

export function deltaClass(v: Num): string {
  if (v === null || !Number.isFinite(v) || v === 0) return "nodata";
  return v > 0 ? "delta-up" : "delta-down";
}

/** Spoken form, so a screen reader hears the direction their table omits. */
export function deltaSpoken(v: Num, label: string, locale: string): string {
  if (v === null || !Number.isFinite(v)) return `${label}: no data`;
  if (v === 0) return `${label}: unchanged`;
  const dir = v > 0 ? "up" : "down";
  const mag = nf(locale, { minimumFractionDigits: 2, maximumFractionDigits: 2 })
    .format(Math.abs(v));
  return `${label}: ${dir} ${mag} percent`;
}

export function count(v: Num, locale: string): string {
  if (v === null || !Number.isFinite(v)) return DASH;
  return nf(locale, { maximumFractionDigits: 0 }).format(v);
}

export function compactCount(v: Num, locale: string): string {
  if (v === null || !Number.isFinite(v)) return DASH;
  return nf(locale, { notation: "compact", maximumFractionDigits: 2 }).format(v);
}

const timeCache = new Map<string, Intl.DateTimeFormat>();

function dtf(locale: string, opts: Intl.DateTimeFormatOptions): Intl.DateTimeFormat {
  const key = locale + JSON.stringify(opts);
  let f = timeCache.get(key);
  if (!f) {
    // hourCycle h23 everywhere: times are always 24-hour, never AM/PM.
    f = new Intl.DateTimeFormat(locale, { hourCycle: "h23", ...opts });
    timeCache.set(key, f);
  }
  return f;
}

export function clockTime(iso: string | number | null, locale: string): string {
  if (!iso) return DASH;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return DASH;
  return dtf(locale, { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(d);
}

export function dateTime(ms: number, locale: string, span: number): string {
  const d = new Date(ms);
  if (span <= 48 * 3600 * 1000) {
    return dtf(locale, { hour: "2-digit", minute: "2-digit" }).format(d);
  }
  if (span <= 366 * 24 * 3600 * 1000) {
    return dtf(locale, { day: "2-digit", month: "short" }).format(d);
  }
  return dtf(locale, { month: "short", year: "numeric" }).format(d);
}

export function fullDateTime(ms: number, locale: string): string {
  return dtf(locale, {
    day: "2-digit", month: "short", year: "numeric",
    hour: "2-digit", minute: "2-digit",
  }).format(new Date(ms));
}

/** Time until an instant, for the refresh countdown. Past due reads as "now". */
export function countdown(toIso: string | null, nowMs: number): string {
  if (!toIso) return DASH;
  const at = new Date(toIso).getTime();
  if (Number.isNaN(at)) return DASH;
  const s = Math.round((at - nowMs) / 1000);
  if (s <= 0) return "now";
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  return `${m}m ${String(s - m * 60).padStart(2, "0")}s`;
}

/** Exact age, never "a few minutes ago". */
export function age(fromIso: string | null, nowMs: number): string {
  if (!fromIso) return DASH;
  const then = new Date(fromIso).getTime();
  if (Number.isNaN(then)) return DASH;
  let s = Math.max(0, Math.round((nowMs - then) / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  s -= m * 60;
  if (m < 60) return `${m}m ${s}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m - h * 60}m`;
}

/**
 * Live Coin Watch prefixes underscores to disambiguate duplicate tickers:
 * Hyperliquid is "______HYPE" because "HYPE" is an older token. Their own site
 * shows the trimmed form, and the coin name disambiguates, so this only affects
 * display. Never send this to the API.
 */
export function displayCode(code: string): string {
  const trimmed = code.replace(/^_+/, "");
  return trimmed || code;
}

export function days(v: number, locale: string): string {
  if (!Number.isFinite(v) || v <= 0) return DASH;
  return count(v, locale) + "d";
}
