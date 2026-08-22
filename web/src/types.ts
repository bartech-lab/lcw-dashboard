// Mirrors the Go DTOs in internal/snapshot. Raw deltas do not exist here
// because they never cross the wire.

export type View = "top" | "favourites";
export type Theme = "auto" | "light" | "dark";
export type Density = "relaxed" | "compact" | "dense";
export type DeltaWindow = "hour" | "day" | "week" | "month" | "quarter" | "year";

export type Num = number | null;

export interface ChangePct {
  hour: Num; day: Num; week: Num; month: Num; quarter: Num; year: Num;
}

export interface CoinRow {
  code: string;
  name: string;
  symbol: string;
  rank: number;
  age: number;
  color: string;
  icons: { png32: string; png64: string; webp32: string; webp64: string };
  rate: Num; volume: Num; cap: Num; liquidity: Num; totalCap: Num;
  allTimeHighUSD: Num; circulatingSupply: Num; totalSupply: Num; maxSupply: Num;
  exchanges: number; markets: number; pairs: number;
  categories?: string[];
  changePct: ChangePct;
  fromAthPct: Num;
}

export interface WireError {
  code: number; status: string; description: string; at: string;
}

export interface Coins {
  view: View; currency: string; sort: string; order: string;
  asOf: string; ageMs: number;
  stale: boolean; staleSince: string | null;
  error: WireError | null;
  creditsUsed: number;
  rotating: boolean;
  unknownCodes?: string[];
  coins: CoinRow[];
}

export interface Overview {
  currency: string; asOf: string; stale: boolean; error: WireError | null;
  cap: Num; volume: Num; liquidity: Num; btcDominance: Num;
}

export type PollState =
  | "initializing" | "active" | "idle" | "conserve"
  | "critical" | "exhausted" | "auth_failed" | "no_key";

export interface Status {
  pollState: PollState;
  activeViewKey: string;
  rotating: boolean;
  rotationKeys: string[];
  intervalMs: number;
  nextTickAt: string | null;
  lastSuccessAt: string | null;
  consecutiveFailures: number;
  lastError: WireError | null;
  visibleClients: number;
  totalClients: number;
  chunkPenalty: number;
  searchIndex: { ready: boolean; coins: number; builtAt: string; building: boolean };
  revision: number;
  degradedReason?: string;
  setupHint?: string;
}

export interface Credits {
  utcDay: string; localSpend: number; inFlight: number;
  byKind: Record<string, number>;
  apiRemaining: number; apiLimit: number;
  reconciledAt: string; drift: number; resetsAt: string;
  budgetState: string; dailyCeiling: number;
}

export interface Watchlist {
  codes: string[]; hash: string; updatedAt: string; max: number;
}


export interface HelloConfig {
  activeIntervalMs: number;
  idleIntervalMs: number;
  overviewIntervalMs: number;
  focusRefreshThresholdMs: number;
  presenceHeartbeatMs: number;
  sseHeartbeatMs: number;
  coinLimit: number;
  defaultCurrency: string;
  defaultView: View;
  watchlistMax: number;
  watchlistSource: string;
  projectedDailyCredits: number;
  dailyCeiling: number;
  alertsEnabled: boolean;
  historyEnabled: boolean;
  // The API cannot sort by a delta window, so market-scope sorting is only
  // offered for columns in this list.
  sortableFields: string[];
  deltaWindows: DeltaWindow[];
  chartRanges: string[];
}

export interface Hello {
  clientId: string; serverVersion: string; startedAt: string; config: HelloConfig;
}

export interface Point { date: number; rate: Num; volume: Num; cap: Num }

export interface Alert {
  eventId: string; ruleId: string; ruleName: string; severity: string;
  code: string; name: string; currency: string;
  metric: string; window?: string; op: string;
  threshold: number; value: number; previousValue: Num;
  firedAt: string; message: string; cooldownUntil: string | null;
}

export interface SearchResult {
  code: string; name: string; symbol: string; rank: number;
  png32: string; score: number; inWatchlist: boolean;
}
