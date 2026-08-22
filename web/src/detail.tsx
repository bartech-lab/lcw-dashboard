import { useEffect, useRef, useState } from "preact/hooks";
import type { Detail, Num, Point } from "./types";
import * as fmt from "./format";
import * as prefs from "./prefs";
import * as S from "./stream";
import { back } from "./router";
import { toggleWatch } from "./app";
import { announce } from "./announce";

export function CoinDetail({ code }: { code: string }) {
  const [data, setData] = useState<Detail | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const range = prefs.chartRange.value;
  const currency = prefs.currency.value;
  const h1 = useRef<HTMLHeadingElement>(null);

  useEffect(() => {
    h1.current?.focus();
    announce(`${code} detail view`);
  }, [code]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") back();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => {
    let live = true;
    setErr(null);
    fetch(`/api/coins/${encodeURIComponent(code)}?range=${range}&currency=${currency}`)
      .then((r) => r.ok ? r.json() : r.json().then((e) => Promise.reject(e)))
      .then((d: Detail) => { if (live) setData(d); })
      .catch((e) => { if (live) setErr(e?.detail || e?.error || "lookup failed"); });
    return () => { live = false; };
  }, [code, range, currency]);

  const loc = prefs.locale.value;
  const coin = data?.coin;

  return (
    <>
      <div class="detail-head">
        <button type="button" class="chip" onClick={back}>← Back</button>
        {coin?.icons.png64 && <img src={coin.icons.png64} alt="" width="36" height="36" />}
        <h1 tabIndex={-1} ref={h1}>
          {fmt.displayCode(coin?.code ?? code)}
          {coin?.name && <span class="coin-name"> {coin.name}</span>}
        </h1>
        {coin && coin.rank > 0 && <span class="chip">#{coin.rank}</span>}
        <HeartButton code={code} />
        <span class="detail-price">{fmt.money(coin?.rate ?? null, loc, currency)}</span>
      </div>

      {err && <div class="banner" role="alert"><strong>Could not load {code}.</strong><span>{err}</span></div>}

      {coin && <Deltas coin={coin} />}

      <div class="surface chart-box">
        <RangeTabs />
        <Chart points={data?.history ?? []} currency={currency} label={coin?.name ?? code} />
        <HistoryTable points={data?.history ?? []} currency={currency} />
        {data && (
          <p class="coin-name">
            {data.source === "local"
              ? "Served from local history, no API credits used."
              : data.source === "mixed"
                ? `Local history plus API, ${data.creditsUsed} credit(s) used.`
                : `From the API, ${data.creditsUsed} credit(s) used.`}
            {data.fromCache && " Cached."}
          </p>
        )}
      </div>

      {coin && <Stats coin={coin} currency={currency} />}
    </>
  );
}

function HeartButton({ code }: { code: string }) {
  const wl = S.watchlist.value?.codes ?? prefs.watchlistCache.value;
  const on = wl.includes(code);
  return (
    <button
      type="button" class="chip" aria-pressed={on}
      onClick={() => void toggleWatch(code)}
    >
      {on ? "♥ On watchlist" : "♡ Add to watchlist"}
    </button>
  );
}

function Deltas({ coin }: { coin: { changePct: Record<string, Num> } }) {
  const loc = prefs.locale.value;
  const windows: [string, string][] = [
    ["hour", "1h"], ["day", "24h"], ["week", "7d"],
    ["month", "30d"], ["quarter", "90d"], ["year", "1y"],
  ];
  return (
    <div class="deltas">
      {windows.map(([key, label]) => {
        const v = coin.changePct[key] ?? null;
        return (
          <div key={key}>
            <span>{label}</span>
            <span class={fmt.deltaClass(v)}>
              <span class="sr-only">{fmt.deltaSpoken(v, label, loc)}</span>
              <span aria-hidden="true">{fmt.glyph(v)}&nbsp;{fmt.percent(v, loc)}</span>
            </span>
          </div>
        );
      })}
    </div>
  );
}

function RangeTabs() {
  const ranges = S.serverConfig.value?.chartRanges ?? ["24h", "7d", "30d", "90d", "1y"];
  return (
    <div class="seg" role="radiogroup" aria-label="Chart range">
      {ranges.map((r) => (
        <button
          key={r} type="button" role="radio"
          aria-checked={prefs.chartRange.value === r}
          onClick={() => { prefs.chartRange.value = r; }}
        >
          {r}
        </button>
      ))}
    </div>
  );
}

function Stats({ coin, currency }: { coin: NonNullable<Detail["coin"]>; currency: string }) {
  const loc = prefs.locale.value;
  const supply = coin.circulatingSupply !== null && coin.maxSupply !== null
    ? `${fmt.compactCount(coin.circulatingSupply, loc)} / ${fmt.compactCount(coin.maxSupply, loc)}`
    : fmt.compactCount(coin.circulatingSupply, loc);

  return (
    <dl class="stats">
      <Stat label="Market cap" value={fmt.compactMoney(coin.cap, loc, currency)} />
      <Stat label="Volume 24h" value={fmt.compactMoney(coin.volume, loc, currency)} />
      <Stat label="Liquidity ±2%" value={fmt.compactMoney(coin.liquidity, loc, currency)} />
      <Stat label="All-time high" value={fmt.money(coin.allTimeHighUSD, loc, "USD")} />
      <Stat
        label="From ATH"
        value={coin.fromAthPct === null ? fmt.DASH : fmt.percent(coin.fromAthPct, loc)}
      />
      <Stat label="Circulating / max" value={supply} />
      <Stat label="Age" value={fmt.days(coin.age, loc)} />
      <Stat label="Exchanges" value={fmt.count(coin.exchanges || null, loc)} />
      <Stat label="Markets" value={fmt.count(coin.markets || null, loc)} />
      {coin.categories && coin.categories.length > 0 &&
        <Stat label="Categories" value={coin.categories.join(", ")} />}
    </dl>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return <div class="tile"><dt>{label}</dt><dd>{value}</dd></div>;
}

const M = { top: 10, right: 14, bottom: 24, left: 62 };

/**
 * Hand-rolled SVG. No viewBox and explicit pixel dimensions, so a 2px stroke is
 * exactly 2px and 11px tick text is exactly 11px at any container width.
 *
 * Prices are not zero-baselined: a 2% intraday move on a zero-baselined axis is
 * a flat line. The axis is always labelled and the area fades to transparent at
 * the plot bottom, so the fill is a gradient rather than a claim about zero.
 *
 * Every colour is a CSS variable, so a theme flip needs no re-render.
 */
function Chart({ points, currency, label }: { points: Point[]; currency: string; label: string }) {
  const wrap = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ w: 800, h: 320 });
  const [hover, setHover] = useState<number | null>(null);
  const loc = prefs.locale.value;

  useEffect(() => {
    if (!wrap.current) return;
    const ro = new ResizeObserver((entries) => {
      const w = Math.max(320, Math.round(entries[0].contentRect.width));
      setSize({ w, h: Math.max(220, Math.min(420, Math.round(w * 0.38))) });
    });
    ro.observe(wrap.current);
    return () => ro.disconnect();
  }, []);

  const usable = points.filter((p) => p.rate !== null);
  if (usable.length < 2) {
    return (
      <div ref={wrap}>
        <p class="empty">
          {points.length === 0
            ? "No history for this range yet."
            : "Not enough points to draw a chart."}
        </p>
      </div>
    );
  }

  const { w, h } = size;
  const plotW = w - M.left - M.right;
  const plotH = h - M.top - M.bottom;

  const xs = usable.map((p) => p.date);
  const ys = usable.map((p) => p.rate as number);
  const x0 = xs[0];
  const x1 = xs[xs.length - 1];

  const pad = (Math.max(...ys) - Math.min(...ys)) * 0.06 || Math.max(...ys) * 0.01 || 1;
  const ticks = niceTicks(Math.min(...ys) - pad, Math.max(...ys) + pad, 5);
  const yMin = Math.min(ticks[0], Math.min(...ys));
  const yMax = Math.max(ticks[ticks.length - 1], Math.max(...ys));

  const sx = (v: number) => M.left + ((v - x0) / (x1 - x0 || 1)) * plotW;
  const sy = (v: number) => M.top + plotH - ((v - yMin) / (yMax - yMin || 1)) * plotH;

  // Straight segments only: smoothing would invent prices that never existed.
  const line = usable
    .map((p, i) => `${i ? "L" : "M"}${sx(p.date).toFixed(1)},${sy(p.rate as number).toFixed(1)}`)
    .join("");
  const area = `${line}L${sx(x1).toFixed(1)},${(M.top + plotH).toFixed(1)}` +
    `L${sx(x0).toFixed(1)},${(M.top + plotH).toFixed(1)}Z`;

  const span = x1 - x0;
  const hi = hover !== null ? usable[hover] : null;

  const move = (clientX: number) => {
    const rect = wrap.current?.getBoundingClientRect();
    if (!rect) return;
    const t = x0 + ((clientX - rect.left - M.left) / (plotW || 1)) * (x1 - x0);
    setHover(nearest(xs, t));
  };

  const first = usable[0].rate as number;
  const last = usable[usable.length - 1].rate as number;

  return (
    <div ref={wrap} style={{ position: "relative" }}>
      <svg
        width={w} height={h} role="img" tabIndex={0}
        aria-label={`${label} price, ${prefs.chartRange.value}. Opens at ` +
          `${fmt.money(first, loc, currency)}, low ${fmt.money(Math.min(...ys), loc, currency)}, ` +
          `high ${fmt.money(Math.max(...ys), loc, currency)}, ` +
          `closes at ${fmt.money(last, loc, currency)}. Use arrow keys to read points.`}
        onKeyDown={(e) => {
          const i = hover ?? usable.length - 1;
          const step = Math.max(1, Math.round(usable.length / 10));
          let next: number | null = null;
          switch (e.key) {
            case "ArrowRight": next = Math.min(usable.length - 1, i + 1); break;
            case "ArrowLeft": next = Math.max(0, i - 1); break;
            case "PageDown": next = Math.min(usable.length - 1, i + step); break;
            case "PageUp": next = Math.max(0, i - step); break;
            case "Home": next = 0; break;
            case "End": next = usable.length - 1; break;
            case "Escape": setHover(null); return;
            default: return;
          }
          e.preventDefault();
          setHover(next);
          const p = usable[next];
          announce(`${fmt.fullDateTime(p.date, loc)}: ${fmt.money(p.rate, loc, currency)}`);
        }}
      >
        <defs>
          <linearGradient id="lc-area" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="var(--chart-area-from)" />
            <stop offset="100%" stop-color="var(--chart-area-to)" />
          </linearGradient>
        </defs>

        {ticks.map((t) => {
          // The half-pixel offset is what makes a 1px hairline crisp.
          const y = Math.round(sy(t)) + 0.5;
          return (
            <g key={t}>
              <line x1={M.left} y1={y} x2={w - M.right} y2={y}
                stroke="var(--chart-grid)" stroke-width="1" />
              <text x={M.left - 8} y={y + 3} text-anchor="end"
                font-size="11" fill="var(--chart-tick)"
                style={{ fontVariantNumeric: "tabular-nums" }}>
                {fmt.compactMoney(t, loc, currency)}
              </text>
            </g>
          );
        })}

        <path d={area} fill="url(#lc-area)" />
        <path d={line} fill="none" stroke="var(--chart-line)" stroke-width="2"
          stroke-linejoin="round" stroke-linecap="round" />

        {xTicks(usable, 5).map((p) => (
          <text key={p.date} x={sx(p.date)} y={h - 6} text-anchor="middle"
            font-size="11" fill="var(--chart-tick)">
            {fmt.dateTime(p.date, loc, span)}
          </text>
        ))}

        {hi && (
          <g>
            <line x1={sx(hi.date)} y1={M.top} x2={sx(hi.date)} y2={M.top + plotH}
              stroke="var(--chart-crosshair)" stroke-width="1" />
            <circle cx={sx(hi.date)} cy={sy(hi.rate as number)} r="4"
              fill="var(--chart-line)" stroke="var(--chart-dot-ring)" stroke-width="2" />
          </g>
        )}

        <rect
          x={M.left} y={M.top} width={plotW} height={plotH} fill="transparent"
          style={{ touchAction: "none" }}
          onPointerMove={(e) => move(e.clientX)}
          onPointerLeave={() => setHover(null)}
        />
      </svg>

      {hi && (
        <div
          class="tooltip"
          style={{
            left: `${sx(hi.date) > w / 2 ? sx(hi.date) - 150 : sx(hi.date) + 12}px`,
            top: `${Math.max(0, sy(hi.rate as number) - 40)}px`,
          }}
        >
          <div>{fmt.fullDateTime(hi.date, loc)}</div>
          <div><b>{fmt.money(hi.rate, loc, currency)}</b></div>
          {hi.volume !== null && <div class="coin-name">Vol {fmt.compactMoney(hi.volume, loc, currency)}</div>}
        </div>
      )}
    </div>
  );
}

/** The table view is both an accessibility requirement and the easiest way to
 *  read exact numbers. */
function HistoryTable({ points, currency }: { points: Point[]; currency: string }) {
  const loc = prefs.locale.value;
  if (points.length === 0) return null;
  return (
    <details>
      <summary>Table view ({points.length} points)</summary>
      <table>
        <thead>
          <tr><th scope="col" class="left">Time</th><th scope="col">Price</th><th scope="col">Volume</th></tr>
        </thead>
        <tbody>
          {points.map((p) => (
            <tr key={p.date}>
              <td class="left">{fmt.fullDateTime(p.date, loc)}</td>
              <td>{fmt.money(p.rate, loc, currency)}</td>
              <td>{fmt.compactMoney(p.volume, loc, currency)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </details>
  );
}

/** 1-2-5 nice ticks, so the top and bottom gridlines are labelled values. */
function niceTicks(min: number, max: number, count: number): number[] {
  if (!Number.isFinite(min) || !Number.isFinite(max) || max <= min) return [min];
  const raw = (max - min) / count;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const step = ([1, 2, 5, 10].find((m) => m * mag >= raw) ?? 10) * mag;
  const out: number[] = [];
  for (let t = Math.ceil(min / step) * step; t <= max + step / 2; t += step) {
    out.push(Number(t.toFixed(10)));
  }
  return out.length > 0 ? out : [min, max];
}

function xTicks(points: Point[], n: number): Point[] {
  if (points.length <= n) return points;
  const step = (points.length - 1) / (n - 1);
  return Array.from({ length: n }, (_, i) => points[Math.round(i * step)]);
}

/** Binary search for the nearest point, so a pointer move is O(log n). */
function nearest(xs: number[], target: number): number {
  let lo = 0;
  let hi = xs.length - 1;
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1;
    if (xs[mid] < target) lo = mid;
    else hi = mid;
  }
  return Math.abs(xs[lo] - target) <= Math.abs(xs[hi] - target) ? lo : hi;
}
