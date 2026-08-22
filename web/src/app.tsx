import { computed } from "@preact/signals";
import { useEffect, useErrorBoundary, useRef, useState } from "preact/hooks";
import type { CoinRow, SearchResult, Status } from "./types";
import * as fmt from "./format";
import * as prefs from "./prefs";
import * as S from "./stream";
import { ALL_COLUMNS, GROUP_LABELS, HIDE_BELOW, columnDef, type ColumnId, type Group } from "./columns";
import { announce } from "./announce";
import { Heart } from "./heart";

const STABLE = new Set(["USDT", "USDC", "DAI", "TUSD", "USDE", "FDUSD", "USDS", "PYUSD", "BUSD"]);

export const visibleColumns = computed<ColumnId[]>(() =>
  prefs.columnOrder.value.filter((id) => prefs.columnVisible.value[id]),
);

const orderedRows = computed<CoinRow[]>(() => {
  let list = [...S.rows.value];
  if (prefs.hideStablecoins.value) {
    list = list.filter((c) => !STABLE.has(c.code));
  }
  // In market scope the server did the sorting; re-sorting a stale local
  // snapshot causes visible thrash.
  if (prefs.sortScope.value === "market") return list;

  const def = columnDef(prefs.sortCol.value);
  const dir = prefs.sortDir.value === "asc" ? 1 : -1;
  list.sort((a, b) => {
    if (def.id === "coin") return a.code.localeCompare(b.code) * dir;
    const av = def.value(a);
    const bv = def.value(b);
    // Nulls always last: a coin with no market cap must not top a "lowest cap"
    // sort.
    if (av === null && bv === null) return 0;
    if (av === null) return 1;
    if (bv === null) return -1;
    return (av - bv) * dir;
  });
  return list;
});

const pagedRows = computed<CoinRow[]>(() => {
  const size = prefs.pageSize.value;
  const start = (prefs.page.value - 1) * size;
  return orderedRows.value.slice(start, start + size);
});

const pageCount = computed(() =>
  Math.max(1, Math.ceil(orderedRows.value.length / prefs.pageSize.value)),
);

export function App() {
  const [error, reset] = useErrorBoundary();
  useEffect(() => { installHotkeys(); }, []);

  if (error) {
    return (
      <div class="app">
        <Header />
        <div class="banner" role="alert">
          <strong>Something failed to render.</strong>
          <span>
            {String((error as Error)?.message ?? error)}
            <br />
            Stored view settings are the usual cause. Resetting them keeps your
            watchlist, which lives on the server.
            <br />
            <button
              type="button" class="chip"
              onClick={() => { localStorage.removeItem("lcwd:prefs"); location.reload(); }}
            >
              Reset view settings and reload
            </button>
            <button type="button" class="chip" onClick={reset}>Try again</button>
          </span>
        </div>
      </div>
    );
  }

  return (
    <div class="app">
      <Header />
      <Dashboard />
    </div>
  );
}

function Dashboard() {
  const st = S.status.value;
  return (
    <>
      <div class="titles">
        <h1>Cryptocurrency Prices Live</h1>
        <p>{prefs.view.value === "favourites" ? "Your watchlist" : "Top coins by market cap"}</p>
      </div>
      {st?.setupHint && <SetupBanner hint={st.setupHint} />}
      <UnknownCodesBanner />
      <OverviewStrip />
      <FilterBar />
      <CoinTable />
    </>
  );
}

function SetupBanner({ hint }: { hint: string }) {
  const [file, ...rest] = hint.replace("Create ", "").split(" containing ");
  return (
    <div class="banner" role="status">
      <strong>No API key yet.</strong>
      <span>
        Create <code>{file}</code> containing <code>{rest.join(" ") || "LCW_API_KEY=<your key>"}</code>,
        then restart. Get a free key at livecoinwatch.com/profile.
      </span>
    </div>
  );
}

function UnknownCodesBanner() {
  const unknown = S.coins.value?.unknownCodes;
  if (!unknown || unknown.length === 0) return null;
  return (
    <div class="banner" role="status">
      <strong>Unknown codes.</strong>
      <span>
        Live Coin Watch returned no data for {unknown.join(", ")}. They stay on your
        watchlist but will not appear in the table.
      </span>
    </div>
  );
}

function Header() {
  return (
    <header class="header">
      <div class="logo">Live<span>·</span>Coin<span>·</span>Watch</div>
      <SearchBox />
      <ThemeToggle />
      <ConnectionPill />
    </header>
  );
}

function ThemeToggle() {
  const options: [prefs.Prefs["theme"], string][] = [
    ["auto", "Auto"], ["light", "Light"], ["dark", "Dark"],
  ];
  return (
    <div class="seg" role="radiogroup" aria-label="Colour theme">
      {options.map(([value, label]) => (
        <button
          key={value}
          type="button"
          role="radio"
          aria-checked={prefs.theme.value === value}
          onClick={() => { prefs.theme.value = value; }}
        >
          {label}
        </button>
      ))}
    </div>
  );
}

function ConnectionPill() {
  const c = S.conn.value;
  const st = S.status.value;
  const stale = S.isStale.value;
  const loc = prefs.locale.value;
  const now = S.now.value;

  if (c === "offline" || c === "reconnecting") {
    const secs = S.retryAt.value ? Math.max(0, Math.round((S.retryAt.value - now) / 1000)) : 0;
    return (
      <span class="pill">
        <span class={`dot ${c === "offline" ? "dot-error" : "dot-warn"}`} />
        <span aria-live="polite">
          {c === "offline" ? "Disconnected" : "Reconnecting"}
          {secs > 0 ? `, retry in ${secs}s` : ""}
          {S.coins.value ? `, data ${fmt.age(S.coins.value.asOf, now)} old` : ""}
        </span>
        <button type="button" onClick={S.retryNow}>Retry now</button>
      </span>
    );
  }

  const asOf = S.coins.value?.asOf ?? null;
  const throttled = st != null &&
    (st.pollState === "conserve" || st.pollState === "critical" || st.pollState === "exhausted");
  const cr = S.credits.value;

  let dot = "dot-live";
  let label = "Live";
  if (throttled) {
    dot = "dot-info";
    label = "Throttled";
  } else if (stale) {
    dot = "dot-warn";
    label = "Stale";
  }

  return (
    <span
      class="pill"
      title={cr ? `${cr.remainingEstimate} of ${cr.apiLimit} credits left today` : ""}
    >
      <span class={`dot ${dot}`} />
      <span aria-live="polite">{label}</span>
      <RefreshTimer asOf={asOf} status={st} now={now} locale={loc} />
    </span>
  );
}

/**
 * The refresh timer. Age answers "is this current" and the countdown answers
 * "when will it change", so both are shown; a timestamp alone leaves you doing
 * the subtraction.
 */
function RefreshTimer({
  asOf, status, now, locale,
}: {
  asOf: string | null;
  status: Status | null;
  now: number;
  locale: string;
}) {
  const interval = status?.intervalMs ?? 0;
  const next = status?.nextTickAt ?? null;
  const ageMs = asOf ? Math.max(0, now - new Date(asOf).getTime()) : null;

  // A fetch in flight leaves nextTickAt in the past, which reads better as an
  // explicit "updating" than as a stuck "now".
  const dueIn = next ? new Date(next).getTime() - now : null;
  const updating = dueIn !== null && dueIn <= 0;

  const pct = interval > 0 && ageMs !== null
    ? Math.min(100, Math.round((ageMs / interval) * 100))
    : 0;

  return (
    <span class="timer">
      <span class="timer-bar" aria-hidden="true">
        <i style={{ width: `${pct}%` }} />
      </span>
      <span class="timer-text">
        {fmt.clockTime(asOf, locale)}
        {ageMs !== null && `, ${fmt.age(asOf, now)} ago`}
      </span>
      <span class="timer-next">
        {updating ? "updating" : `next ${fmt.countdown(next, now)}`}
      </span>
    </span>
  );
}

function SearchBox() {
  const [q, setQ] = useState("");
  const [results, setResults] = useState<SearchResult[]>([]);
  const [open, setOpen] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    (window as unknown as { __lcwFocusSearch?: () => void }).__lcwFocusSearch =
      () => inputRef.current?.focus();
  }, []);

  useEffect(() => {
    if (q.trim().length < 1) {
      setResults([]);
      return;
    }
    const ctrl = new AbortController();
    const t = setTimeout(async () => {
      try {
        const res = await fetch(`/api/search?q=${encodeURIComponent(q)}`, { signal: ctrl.signal });
        const data = await res.json();
        setResults(data.results ?? []);
        setOpen(true);
      } catch { /* aborted or offline */ }
    }, 150);
    return () => { clearTimeout(t); ctrl.abort(); };
  }, [q]);

  return (
    <div class="search" onBlur={(e) => {
      if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) setOpen(false);
    }}>
      <label>
        <span class="sr-only">Search coins</span>
        <input
          ref={inputRef}
          type="search"
          placeholder="Search…"
          value={q}
          onInput={(e) => setQ((e.target as HTMLInputElement).value)}
          onFocus={() => results.length > 0 && setOpen(true)}
        />
      </label>
      <kbd aria-hidden="true">/</kbd>
      {open && results.length > 0 && (
        <ul class="search-results">
          {results.map((r) => (
            <li key={r.code}>
              <button type="button" onClick={() => { void toggleWatch(r.code); setOpen(false); }}>
                {r.png32 && <img src={r.png32} alt="" width="18" height="18" />}
                <b>{fmt.displayCode(r.code)}</b>
                <span class="coin-name">{r.name}</span>
                <span class="rank">
                  {r.inWatchlist ? "remove" : "add"} &middot; #{r.rank}
                </span>
              </button>
              <a
                class="search-ext"
                href={fmt.lcwCoinUrl(r.code, r.name)}
                target="_blank"
                rel="noopener noreferrer"
                title={`Open ${r.name} on livecoinwatch.com`}
              >
                <span aria-hidden="true">↗</span>
                <span class="sr-only">open {r.name} on livecoinwatch.com</span>
              </a>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function OverviewStrip() {
  const o = S.overview.value;
  if (!o) return null;
  const loc = prefs.locale.value;
  const cur = o.currency;
  return (
    <dl class="strip">
      <Tile label="Market cap" value={fmt.compactMoney(o.cap, loc, cur)} />
      <Tile label="Volume 24h" value={fmt.compactMoney(o.volume, loc, cur)} />
      <Tile label="Liquidity ±2%" value={fmt.compactMoney(o.liquidity, loc, cur)} />
      <Tile
        label="BTC dominance"
        // The API sends a fraction, so it is multiplied here.
        value={o.btcDominance === null ? fmt.DASH
          : fmt.percent(o.btcDominance * 100, loc).replace("+", "")}
      />
    </dl>
  );
}

function Tile({ label, value }: { label: string; value: string }) {
  return <div class="tile"><dt>{label}</dt><dd>{value}</dd></div>;
}

function FilterBar() {
  const wl = S.watchlist.value?.codes ?? prefs.watchlistCache.value;
  return (
    <div class="filters">
      <button
        type="button" class="chip"
        aria-current={prefs.view.value === "top"}
        onClick={() => setView("top")}
      >
        Top {S.serverConfig.value?.coinLimit ?? 100}
      </button>
      <button
        type="button" class="chip"
        aria-current={prefs.view.value === "favourites"}
        onClick={() => setView("favourites")}
      >
        Favourites ({wl.length})
      </button>
      <button
        type="button" class="chip"
        aria-pressed={prefs.hideStablecoins.value}
        onClick={() => { prefs.hideStablecoins.value = !prefs.hideStablecoins.value; }}
      >
        Hide stablecoins
      </button>
      <div class="spacer" />
      <SortScopeChip />
      <LayoutButton />
    </div>
  );
}

function setView(v: "top" | "favourites"): void {
  prefs.view.value = v;
  prefs.page.value = 1;
  prefs.offset.value = 0;
  void S.control({ view: v, offset: 0 });
}

/**
 * Sorting locally reorders only the 100 rows already loaded, so it can never
 * surface the coin ranked #340 that leads on volume. Asking the server does.
 *
 * The control is hidden unless it can change something. Sorted by rank, a
 * market-wide page and the local page are the same 100 coins, so offering the
 * choice there just looks broken.
 */
function SortScopeChip() {
  const def = columnDef(prefs.sortCol.value);
  const api = def.apiSort;
  if (!api || api === "rank") return null;

  const scope = prefs.sortScope.value;
  return (
    <>
      <span class="seg" role="radiogroup" aria-label={`Sort scope for ${def.label}`}>
        <button
          type="button" role="radio" aria-checked={scope === "page"}
          onClick={() => { prefs.sortScope.value = "page"; S.pushSort(); }}
          title={`Reorder the ${S.rows.value.length} coins already loaded. Instant, no API credits.`}
        >
          This page
        </button>
        <button
          type="button" role="radio" aria-checked={scope === "market"}
          onClick={() => { prefs.sortScope.value = "market"; S.pushSort(); }}
          title={`Fetch the market-wide top 100 by ${def.label} (1 credit).`}
        >
          Whole market
        </button>
      </span>
      {scope === "market" && (
        <span class="scope-note">
          top 100 by {def.label}, market-wide
        </span>
      )}
    </>
  );
}

function LayoutButton() {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") setOpen(false); };
    const onClick = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("mousedown", onClick);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onClick);
    };
  }, [open]);

  return (
    <div class="pop-anchor" ref={ref}>
      <button
        type="button" class="chip chip-icon" aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        <span aria-hidden="true">☰</span> Columns &amp; options
      </button>
      {open && <LayoutPopover />}
    </div>
  );
}

function LayoutPopover() {
  const [width, setWidth] = useState(window.innerWidth);
  useEffect(() => {
    const on = () => setWidth(window.innerWidth);
    window.addEventListener("resize", on);
    return () => window.removeEventListener("resize", on);
  }, []);

  const groups: Group[] = ["identity", "market", "change", "supply"];
  const order = prefs.columnOrder.value;

  // Only enabled columns that the container query is currently hiding count.
  const hiddenCount = order.filter((id) => {
    const at = HIDE_BELOW[id];
    return prefs.columnVisible.value[id] && at !== undefined && width < at;
  }).length;

  return (
    <div class="pop" role="dialog" aria-label="Table layout">
      <h3>Density</h3>
      <div class="seg" role="radiogroup" aria-label="Row density">
        {(["relaxed", "compact", "dense"] as const).map((d) => (
          <button
            key={d} type="button" role="radio"
            aria-checked={prefs.density.value === d}
            onClick={() => { prefs.density.value = d; }}
          >
            {d[0].toUpperCase() + d.slice(1)}
          </button>
        ))}
      </div>

      {groups.map((g) => {
        const ids = order.filter((id) => columnDef(id).group === g);
        return (
          <div key={g}>
            <h3>{GROUP_LABELS[g]}</h3>
            {ids.map((id, i) => {
              const def = columnDef(id);
              const hideAt = HIDE_BELOW[id];
              const hidden = !prefs.showAllColumns.value && hideAt !== undefined && width < hideAt;
              return (
                <div class="col-row" key={id}>
                  <button
                    type="button" class="move" disabled={i === 0}
                    aria-label={`Move ${def.label} up`}
                    onClick={() => { prefs.moveColumn(id, -1); announce(`${def.label} moved up`); }}
                  >↑</button>
                  <button
                    type="button" class="move" disabled={i === ids.length - 1}
                    aria-label={`Move ${def.label} down`}
                    onClick={() => { prefs.moveColumn(id, 1); announce(`${def.label} moved down`); }}
                  >↓</button>
                  <label>
                    <input
                      type="checkbox"
                      checked={Boolean(prefs.columnVisible.value[id])}
                      disabled={def.locked}
                      aria-disabled={def.locked}
                      onChange={() => prefs.toggleColumn(id)}
                    />
                    {def.label}
                    {def.locked && <span class="warn"> always shown</span>}
                  </label>
                  {/* Without this advisory a user checks a box, sees nothing
                      change, and concludes the feature is broken. */}
                  {hidden && prefs.columnVisible.value[id] &&
                    <span class="warn">hidden below {hideAt}px</span>}
                </div>
              );
            })}
          </div>
        );
      })}

      <h3>Presets</h3>
      <div class="filters">
        <button type="button" class="chip" onClick={() => prefs.resetColumns()}>Reset</button>
        <button
          type="button" class="chip"
          onClick={() => {
            const name = prompt("Save this layout as:");
            if (name) prefs.savePreset(name.trim());
          }}
        >Save as…</button>
        {prefs.presets.value.map((p) => (
          <span key={p.name} class="chip">
            <button type="button" onClick={() => prefs.loadPreset(p.name)}>{p.name}</button>
            <button
              type="button" aria-label={`Delete preset ${p.name}`}
              onClick={() => prefs.deletePreset(p.name)}
            > ×</button>
          </span>
        ))}
      </div>

      <h3>Columns overlay</h3>
      <label class="col-row">
        <input
          type="checkbox"
          checked={prefs.showAllColumns.value}
          disabled={hiddenCount === 0}
          onChange={() => { prefs.showAllColumns.value = !prefs.showAllColumns.value; }}
        />
        Show all columns, scrolling sideways
      </label>
      <p class="hint">
        {hiddenCount === 0
          ? `Nothing is hidden at ${width}px, so this changes nothing right now.`
          : `${hiddenCount} column${hiddenCount === 1 ? " is" : "s are"} hidden at ` +
            `${width}px because they do not fit.`}
      </p>
    </div>
  );
}

function CoinTable() {
  const cols = visibleColumns.value;
  const list = pagedRows.value;
  const stale = S.isStale.value;

  return (
    <div class="surface">
      <div class={`table-wrap ${prefs.showAllColumns.value ? "wide" : ""}`} data-stale={stale}>
        <table>
          <caption class="sr-only">
            {prefs.view.value === "favourites" ? "Watchlist coins" : "Top coins by market cap"},
            {" "}{list.length} rows
            {stale ? ", data is stale" : ""}
          </caption>
          <colgroup>
            {cols.map((id) => <col key={id} style={{ width: `${columnDef(id).width}px` }} />)}
          </colgroup>
          <thead>
            <tr>{cols.map((id) => <HeadCell key={id} id={id} />)}</tr>
          </thead>
          <tbody>
            {list.map((c) => <Row key={c.code} coin={c} cols={cols} />)}
          </tbody>
        </table>
      </div>
      {list.length === 0 && <EmptyState />}
      <Footer count={orderedRows.value.length} />
    </div>
  );
}

function EmptyState() {
  if (prefs.view.value === "favourites") {
    return (
      <p class="empty">
        No favourites yet. Search for a coin above, or press <kbd>f</kbd> on any row.
      </p>
    );
  }
  const st = S.status.value;
  if (st?.pollState === "no_key") return <p class="empty">Waiting for an API key.</p>;
  return <p class="empty">Waiting for the first update…</p>;
}

function HeadCell({ id }: { id: ColumnId }) {
  const def = columnDef(id);
  const active = prefs.sortCol.value === id;
  const dir = prefs.sortDir.value;
  const ariaSort = active ? (dir === "asc" ? "ascending" : "descending") : undefined;

  return (
    <th
      scope="col"
      data-col={id}
      class={def.align === "left" ? "left" : ""}
      aria-sort={ariaSort}
    >
      {def.sortable ? (
        <button
          type="button"
          onClick={() => {
            if (active) {
              prefs.sortDir.value = dir === "asc" ? "desc" : "asc";
            } else {
              prefs.sortCol.value = id;
              // Rank reads naturally ascending; everything else descending.
              prefs.sortDir.value = id === "rank" || id === "coin" ? "asc" : "desc";
            }
            // Market scope cannot serve a column the API cannot sort by.
            if (!columnDef(prefs.sortCol.value).apiSort) prefs.sortScope.value = "page";
            if (prefs.sortScope.value === "market") S.pushSort();
            announce(`Sorted by ${def.label}, ${prefs.sortDir.value === "asc" ? "ascending" : "descending"}`);
          }}
        >
          {def.label}
          <span aria-hidden="true">{active ? (dir === "asc" ? "↑" : "↓") : "↕"}</span>
        </button>
      ) : def.label}
    </th>
  );
}

function Row({ coin, cols }: { coin: CoinRow; cols: ColumnId[] }) {
  return (
    <tr data-code={coin.code}>
      {cols.map((id) => <Cell key={id} id={id} coin={coin} />)}
    </tr>
  );
}

function Cell({ id, coin }: { id: ColumnId; coin: CoinRow }) {
  const def = columnDef(id);
  const ctx = { locale: prefs.locale.value, currency: S.currency.value };
  const cls = def.align === "left" ? "left" : "";

  if (id === "rank") {
    const wl = S.watchlist.value?.codes ?? prefs.watchlistCache.value;
    const on = wl.includes(coin.code);
    return (
      <td data-col={id} class={cls}>
        <span class="cell-rank">
          <button
            type="button" class="heart" aria-pressed={on}
            aria-label={`${coin.name}, ${on ? "remove from" : "add to"} watchlist`}
            onClick={() => void toggleWatch(coin.code)}
          >
            <Heart on={on} />
          </button>
          {/* Global rank, not a row index: the sequence can read 1, 2, 4, 7. */}
          <span class="rank-num">{coin.rank > 0 ? coin.rank : fmt.DASH}</span>
        </span>
      </td>
    );
  }

  if (id === "coin") {
    return (
      <td data-col={id} class={cls}>
        <a
          class="coin"
          href={fmt.lcwCoinUrl(coin.code, coin.name)}
          target="_blank"
          rel="noopener noreferrer"
        >
          <img src={coin.icons.png32 || coin.icons.png64} alt="" width="24" height="24" loading="lazy" />
          <span class="coin-labels">
            <span class="coin-code">
              {fmt.displayCode(coin.code)}
              <span class="ext" aria-hidden="true">↗</span>
            </span>
            <span class="coin-name">{coin.name}</span>
          </span>
          <span class="sr-only">opens livecoinwatch.com in a new tab</span>
        </a>
      </td>
    );
  }

  if (def.deltaWindow || id === "fromAth") {
    const v = def.value(coin);
    return (
      <td data-col={id} class={`${cls} ${fmt.deltaClass(v)}`}>
        <span class="sr-only">{fmt.deltaSpoken(v, def.label, ctx.locale)}</span>
        {/* The glyph is in the DOM, not a pseudo-element: it is the encoding,
            because up-green and down-red are indistinguishable under
            protanopia. */}
        <span aria-hidden="true">
          {fmt.glyph(v)}&nbsp;{fmt.percent(v, ctx.locale)}
        </span>
      </td>
    );
  }

  return <td data-col={id} class={cls}>{def.text ? def.text(coin, ctx) : fmt.DASH}</td>;
}

/**
 * Pagination walks the ranking, not just the loaded rows.
 *
 * The server fetches one block of `limit` coins at a time. Next moves within
 * that block first, and only asks the server for the following block once you
 * run off the end, so most page turns are free and only crossing a block
 * boundary costs a credit. The favourites view has no offset: the whole
 * watchlist arrives in one request.
 */
function Footer({ count }: { count: number }) {
  const size = prefs.pageSize.value;
  const p = prefs.page.value;
  const blockPages = Math.max(1, Math.ceil(count / size));
  const favourites = prefs.view.value === "favourites";

  const coins = S.coins.value;
  const offset = coins?.offset ?? 0;
  const limit = coins?.limit ?? S.serverConfig.value?.coinLimit ?? 100;
  // A short block means the ranking ran out, so there is nothing after it.
  const blockFull = count >= limit;

  const canPrev = p > 1 || (!favourites && offset > 0);
  const canNext = p < blockPages || (!favourites && blockFull);

  const goPrev = () => {
    if (p > 1) { prefs.page.value = p - 1; return; }
    if (favourites || offset <= 0) return;
    prefs.offset.value = Math.max(0, offset - limit);
    prefs.page.value = Math.max(1, Math.ceil(limit / size));
    S.pushSort();
  };
  const goNext = () => {
    if (p < blockPages) { prefs.page.value = p + 1; return; }
    if (favourites || !blockFull) return;
    prefs.offset.value = offset + limit;
    prefs.page.value = 1;
    S.pushSort();
  };

  // Rank range is more useful than a page number when the block can move.
  const from = offset + (p - 1) * size + 1;
  const to = offset + Math.min(count, p * size);

  return (
    <div class="foot">
      <button type="button" class="chip" disabled={!canPrev} onClick={goPrev}>
        Previous
      </button>
      <span>
        {favourites
          ? `${count} coin${count === 1 ? "" : "s"}`
          : `${from} to ${to} by ${columnDef(prefs.sortCol.value).label}`}
        {blockPages > 1 && `  ·  page ${p} of ${blockPages}`}
      </span>
      <button type="button" class="chip" disabled={!canNext} onClick={goNext}>
        Next
      </button>
      <div class="spacer" />
      <label>
        <span class="sr-only">Rows per page</span>
        <select
          class="select" value={String(size)}
          onChange={(e) => {
            prefs.pageSize.value = Number((e.target as HTMLSelectElement).value);
            prefs.page.value = 1;
          }}
        >
          {prefs.PAGE_SIZES.map((n) => <option key={n} value={n}>{n} per page</option>)}
        </select>
      </label>
    </div>
  );
}

export async function toggleWatch(code: string): Promise<void> {
  // Optimistic, then reconciled by the server's watchlist frame.
  const cur = S.watchlist.value?.codes ?? prefs.watchlistCache.value;
  const next = cur.includes(code) ? cur.filter((c) => c !== code) : [...cur, code];
  prefs.watchlistCache.value = next;

  try {
    const res = await fetch("/api/watchlist/toggle", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ code }),
    });
    const data = await res.json();
    if (data.watchlist) {
      S.watchlist.value = data.watchlist;
      prefs.watchlistCache.value = data.watchlist.codes;
      announce(`${code} ${data.added ? "added to" : "removed from"} watchlist. ${data.watchlist.codes.length} favourites.`);
    }
  } catch {
    prefs.watchlistCache.value = cur;
    announce(`Could not update the watchlist`);
  }
}

function installHotkeys(): void {
  document.addEventListener("keydown", (e) => {
    const el = e.target as HTMLElement | null;
    const typing = el && (el.tagName === "INPUT" || el.tagName === "SELECT" ||
      el.tagName === "TEXTAREA" || el.isContentEditable);

    if (e.key === "/" && !typing) {
      e.preventDefault();
      (window as unknown as { __lcwFocusSearch?: () => void }).__lcwFocusSearch?.();
      return;
    }
    if (typing) return;

    const rows = Array.from(document.querySelectorAll<HTMLElement>("tbody tr[data-code]"));
    if (rows.length === 0) return;
    const active = document.activeElement?.closest("tr[data-code]") as HTMLElement | null;
    let idx = active ? rows.indexOf(active) : -1;

    const focus = (i: number) => {
      const target = rows[Math.max(0, Math.min(rows.length - 1, i))];
      target?.querySelector<HTMLElement>("a.coin")?.focus();
      target?.scrollIntoView({ block: "nearest" });
    };

    switch (e.key) {
      case "j": case "ArrowDown": e.preventDefault(); focus(idx + 1); break;
      case "k": case "ArrowUp": e.preventDefault(); focus(idx - 1); break;
      case "Home": e.preventDefault(); focus(0); break;
      case "End": e.preventDefault(); focus(rows.length - 1); break;
      case "PageDown": e.preventDefault(); focus(idx + 10); break;
      case "PageUp": e.preventDefault(); focus(idx - 10); break;
      case "f":
        if (active) {
          e.preventDefault();
          void toggleWatch(active.dataset.code as string);
        }
        break;
      case "?":
        e.preventDefault();
        announce("Shortcuts: j and k move between rows, Enter opens the coin on " +
          "livecoinwatch.com, f toggles favourite, slash focuses search.");
        break;
    }
  });
}
