import { signal, computed } from "@preact/signals";
import type {
  Alert, Coins, CoinRow, Credits, Hello, HelloConfig, Overview, Status, Watchlist,
} from "./types";
import * as prefs from "./prefs";

// Client id lives in sessionStorage, so reloading one tab does not orphan
// another tab's presence record.
export const clientId = (() => {
  const key = "lcwd:client";
  let id = sessionStorage.getItem(key);
  if (!id) {
    id = crypto.randomUUID();
    sessionStorage.setItem(key, id);
  }
  return id;
})();

export type ConnState = "connecting" | "live" | "reconnecting" | "offline";

export const conn = signal<ConnState>("connecting");
export const retryAt = signal<number | null>(null);
export const attempt = signal(0);
export const lastEventAt = signal(0);
export const now = signal(Date.now());

export const serverConfig = signal<HelloConfig | null>(null);
export const coins = signal<Coins | null>(null);
export const overview = signal<Overview | null>(null);
export const status = signal<Status | null>(null);
export const credits = signal<Credits | null>(null);
export const watchlist = signal<Watchlist | null>(null);
export const alerts = signal<Alert[]>([]);

export const rows = computed<CoinRow[]>(() => coins.value?.coins ?? []);

/**
 * The display currency, from the server. The client deliberately does not store
 * one: a value cached here overrode the configured currency and was impossible
 * to clear from the UI once the picker was removed.
 */
export const currency = computed(() => serverConfig.value?.defaultCurrency ?? "USD");

// Age is derived from the newest fetch, and the table is dimmed rather than
// blanked when it grows.
export const dataAgeMs = computed(() => {
  const c = coins.value;
  if (!c) return null;
  const asOf = new Date(c.asOf).getTime();
  if (Number.isNaN(asOf)) return null;
  return Math.max(0, now.value - asOf);
});

export const isStale = computed(() => {
  const c = coins.value;
  if (!c) return false;
  if (c.stale) return true;
  const interval = status.value?.intervalMs ?? 15000;
  const age = dataAgeMs.value;
  return age !== null && age > interval * 1.5;
});

const BACKOFF = [1000, 2000, 4000, 8000, 15000, 30000, 30000];
const jitter = (ms: number) => ms * (0.5 + Math.random() * 0.5);

let es: EventSource | null = null;
let retryTimer: number | undefined;
let notifyHandler: ((a: Alert) => void) | null = null;

export function onAlert(fn: (a: Alert) => void): void {
  notifyHandler = fn;
}

export function connect(): void {
  clearTimeout(retryTimer);
  es?.close();

  const params = new URLSearchParams({
    client_id: clientId,
    view: prefs.view.value,
    visible: document.hidden ? "0" : "1",
    offset: String(prefs.offset.value),
    ...serverSort(),
  });
  es = new EventSource(`/api/stream?${params}`);

  es.addEventListener("open", () => {
    conn.value = "live";
    attempt.value = 0;
    retryAt.value = null;
    touch();
  });

  bind<Hello>("hello", (h) => {
    serverConfig.value = h.config;
    // The server is authoritative on the watchlist; the local cache only exists
    // to paint hearts before this arrives.
    prefs.watchlistCache.value = prefs.watchlistCache.value;
  });
  bind<Coins>("coins", (c) => { coins.value = c; });
  bind<Overview>("overview", (o) => { overview.value = o; });
  bind<Status>("status", (s) => { status.value = s; });
  bind<Credits>("credits", (c) => { credits.value = c; });
  bind<Watchlist>("watchlist", (w) => {
    watchlist.value = w;
    prefs.watchlistCache.value = w.codes;
  });
  bind<Alert>("alert", (a) => {
    if (alerts.value.some((x) => x.eventId === a.eventId)) return;
    alerts.value = [a, ...alerts.value].slice(0, 100);
    notifyHandler?.(a);
  });
  bind<{ reason: string }>("bye", () => {
    // The server said it is going away, so back off instead of hammering it.
    conn.value = "reconnecting";
    scheduleRetry(5000);
  });

  es.addEventListener("error", () => {
    es?.close();
    es = null;
    const d = jitter(BACKOFF[Math.min(attempt.value, BACKOFF.length - 1)]);
    attempt.value = attempt.value + 1;
    conn.value = navigator.onLine ? "reconnecting" : "offline";
    scheduleRetry(d);
  });
}

function bind<T>(name: string, fn: (v: T) => void): void {
  es?.addEventListener(name, (e) => {
    touch();
    try {
      fn(JSON.parse((e as MessageEvent).data) as T);
    } catch (err) {
      console.warn(`lcw-dashboard: bad ${name} frame`, err);
    }
  });
}

function touch(): void {
  lastEventAt.value = Date.now();
}

function scheduleRetry(delayMs: number): void {
  retryAt.value = Date.now() + delayMs;
  clearTimeout(retryTimer);
  retryTimer = setTimeout(connect, delayMs) as unknown as number;
}

export function retryNow(): void {
  attempt.value = 0;
  connect();
}

/**
 * Supervision covers what EventSource misses. Its retry interval is not
 * controllable and it retries forever, and it never fires error on a
 * dead-but-open connection, which is why the server heartbeat is mandatory.
 */
export function startSupervisor(): void {
  setInterval(() => {
    now.value = Date.now();

    if (conn.value === "live") {
      const interval = serverConfig.value?.activeIntervalMs ?? 15000;
      const limit = Math.max(45000, interval * 2.5);
      if (lastEventAt.value > 0 && Date.now() - lastEventAt.value > limit) {
        // Silent half-open connection: take control rather than wait forever.
        es?.close();
        es = null;
        conn.value = "reconnecting";
        scheduleRetry(1000);
      }
    }
  }, 1000);

  window.addEventListener("online", retryNow);
  window.addEventListener("offline", () => { conn.value = "offline"; });

  document.addEventListener("visibilitychange", () => {
    postVisibility(!document.hidden);
    if (!document.hidden) {
      // The first thing a returning user does is read the numbers, so a pending
      // retry fires now.
      if (!es || conn.value !== "live") retryNow();
    }
  });

  // Heartbeat so the server can expire a frozen tab rather than holding the
  // fast cadence for it.
  const beat = serverConfig.value?.presenceHeartbeatMs ?? 20000;
  setInterval(() => {
    if (!document.hidden) postVisibility(true);
  }, beat);
}

export interface ControlReply {
  accepted: boolean;
  reason?: string;
  viewKey: string;
  intervalMs: number;
  revision: number;
  retryAfterMs?: number;
}

/** pendingRevision marks values in flight until the server acknowledges. */
export const pendingRevision = signal(0);

/**
 * The sort the server should apply. In page scope the server keeps the canonical
 * rank-ordered page and the client sorts locally; in market scope the server
 * fetches a genuinely market-wide page by that field.
 */
export function serverSort(): { sort: string; order: string } {
  if (prefs.sortScope.value !== "market") {
    return { sort: "rank", order: "ascending" };
  }
  const api = prefs.apiSortField();
  if (!api) return { sort: "rank", order: "ascending" };
  return { sort: api, order: prefs.sortDir.value === "asc" ? "ascending" : "descending" };
}

export async function control(patch: {
  visible?: boolean; view?: string; sort?: string; order?: string; offset?: number;
}): Promise<ControlReply | null> {
  try {
    const res = await fetch("/api/control", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ clientId, ...patch }),
    });
    if (!res.ok) return null;
    const reply = (await res.json()) as ControlReply;
    pendingRevision.value = reply.revision;
    return reply;
  } catch {
    return null;
  }
}

function postVisibility(visible: boolean): void {
  const body = JSON.stringify({
    clientId, visible, view: prefs.view.value, offset: prefs.offset.value,
  });
  // sendBeacon because a regular fetch may be cancelled as the tab freezes.
  if (!visible && navigator.sendBeacon) {
    navigator.sendBeacon("/api/control", new Blob([body], { type: "application/json" }));
    return;
  }
  // Always send the full state. Sending visibility alone once let the server
  // fall back to its configured default view and discard the user's choice.
  void control({
    visible, view: prefs.view.value, offset: prefs.offset.value, ...serverSort(),
  });
}

/** Ask the server to change what it fetches, after a sort, scope or page change. */
export function pushSort(): void {
  void control({ view: prefs.view.value, offset: prefs.offset.value, ...serverSort() });
}

export async function refresh(what = "coins"): Promise<ControlReply | null> {
  try {
    const res = await fetch("/api/refresh", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ what }),
    });
    return (await res.json()) as ControlReply;
  } catch {
    return null;
  }
}
