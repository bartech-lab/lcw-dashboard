# lcw-dashboard

A local crypto dashboard on the Live Coin Watch API. One Go binary with the
frontend embedded, meant to sit open in a background browser tab all day.

- Sortable, configurable coin table: rank, coin, price, market cap, volume,
  liquidity, and six percentage-change windows
- Watchlist that works for coins outside the top 100
- Market overview strip and a per-coin detail view with a real chart
- Light, dark and auto themes; configurable locale; 24-hour clock throughout
- Desktop alert notifications that fire even when no browser is open
- Local price history recorded at zero API cost

## Quick start

```sh
git clone <this repo> && cd lcw-dashboard

# 1. Put your API key where the binary looks for it.
#    Get a free key at https://www.livecoinwatch.com/profile
mkdir -p ~/.config/lcw-dashboard
echo 'LCW_API_KEY=your-key-here' > ~/.config/lcw-dashboard/.env
chmod 600 ~/.config/lcw-dashboard/.env

# 2. Optional: start from the documented config.
cp config.example.yaml ~/.config/lcw-dashboard/config.yaml

# 3. Build and run. Needs Go 1.24+ and nothing else.
make build
./lcw-dashboard
```

Open <http://127.0.0.1:8787>.

The binary starts even with no key: it serves a page naming the exact file to
create, so you can fix it without reading this document.

### Commands

```sh
make build       # bundle the frontend, then build the binary
make test        # go test -race ./...
make check       # gofmt, vet, tests, and the frontend budget check
make dev         # unminified bundle, served from disk (no Go rebuild on CSS edits)
./lcw-dashboard -print-config    # resolved paths, intervals and credit projection
./lcw-dashboard -check-config    # validate the config and exit
```

There is no `node_modules`. esbuild is a Go library here, and Preact is vendored
under `web/vendor`, so `make build` is pure Go. `make typecheck` runs `tsc` if
you want type checking, which esbuild does not do; it is optional and not part of
the build.

## The credit budget, which shapes everything

The free tier allows **10,000 requests per day, one credit per request**, reset at
UTC midnight. Their site advertises "updates every 2 seconds"; that is their
internal feed, not something the public API can serve.

Default projection, with a tab visible all day:

| Loop | Interval | Credits/day |
| --- | --- | --- |
| Coin table | 15s | 5,760 |
| Market overview | 300s | 288 |
| Search index rebuild | daily | 20 |
| `/credits` reconcile | 15m | 96 |
| **Total** | | **~6,164** |

That leaves roughly 3,800 credits for detail views and currency switches.

A 10-second coin interval with a 60-second overview totals **10,196**, which does
not fit. It is still configurable, and `-print-config` plus the startup log will
tell you when your settings do not fit:

```
level=ERROR msg="projected daily credits exceed the API limit" total=10196 apiLimit=10000
```

**Two properties hold regardless of how you use it.** Opening more tabs does not
spend more credits — several views share the poll loop by rotation, so each
refreshes more slowly instead. And a watchlist longer than 100 codes does not
spend more either: it splits into more requests and the interval is multiplied to
match. Both are covered by tests.

## Configuration

Server settings live in `~/.config/lcw-dashboard/config.yaml`. Every key is
documented with its default in `config.example.yaml`.

View settings — visible columns, column order, theme, density, sort, page size,
locale — live in the browser's `localStorage`, so changing a column needs no
restart. Use the **Layout** button above the table.

The API key is never in `config.yaml`; `-check-config` rejects a config that
tries to carry one.

### Where things are stored

Nothing the running program writes goes in the repository.

| Path | Contents |
| --- | --- |
| `~/.config/lcw-dashboard/config.yaml` | your settings |
| `~/.config/lcw-dashboard/.env` | the API key, mode 600 |
| `~/.local/state/lcw-dashboard/` | credit ledger, alert state, watchlist, price history |
| `~/.cache/lcw-dashboard/` | search index, fiat list |

Deleting the cache directory is always safe. Deleting the state directory loses
credit accounting, alert arming and your watchlist. macOS uses the same paths.

### Price history

The poll loop already receives price, volume and market cap for every coin, so
recording it costs nothing. Each coin gets a fixed-size ring buffer:

| Tier | Resolution | Retention |
| --- | --- | --- |
| 1 | 1 minute | 24 hours |
| 2 | 15 minutes | 30 days |
| 3 | 1 hour | 365 days |

That is **204 KB per coin, about 49 MB for 250 coins, and constant** — a ring
buffer overwrites rather than appends, so the files reach their size and stay
there. The detail chart prefers this local data and only calls the API for spans
it does not cover. Turn it off with `history.enabled: false`.

## Alerts

Rules are declared in `config.yaml`, so they are diffable and survive restarts.
`config.example.yaml` has two worked examples. The engine runs server-side, which
means alerts fire whether or not a browser is open.

| Sink | Linux | macOS |
| --- | --- | --- |
| `native` | `notify-send` over D-Bus | `osascript` |
| `browser` | Notification API | Notification API |
| `log` | structured log line | structured log line |

**The browser sink alone is not reliable.** Chrome freezes idle background tabs,
and a frozen tab's `EventSource` stops delivering, which is exactly when an alert
matters. That is why the Go process notifies the desktop directly. Chromium also
buffers server-sent events to hidden tabs and flushes them on focus, so every
notification shows the server's own fired-at time rather than claiming it just
happened.

Rules use `crosses_above` for edges and `hysteresis_pct` to stop a price
oscillating around a threshold from alerting repeatedly. A test asserts that 50
oscillations of 99,999 to 100,001 produce exactly one alert.

## Two things the API cannot do

**It cannot sort by a percentage change.** The `sort` parameter accepts only
`rank`, `price`, `volume`, `code`, `name` and `age`. So the **This page /
Market** control beside the sort header is disabled on the six change columns:
sorting them reorders the coins already loaded, which cannot surface the coin
ranked #340 that gained 90% this week. The control exists to make that
distinction visible rather than misleading.

**It has no search endpoint.** Search runs against a local index of the top 2,000
coins, built by walking `/coins/list` once a day for 20 credits and cached to
disk.

Their table's Categories, Exchanges and Platforms filters have no public
endpoint either, so they are absent. Category filtering client-side is possible
and may come later.

## Run it at login

### Linux (systemd user unit)

```sh
make install-linux
systemctl --user enable --now lcw-dashboard

# Without this the service stops at logout and does not survive a reboot.
loginctl enable-linger "$USER"
```

Check it and read logs:

```sh
systemctl --user status lcw-dashboard
journalctl --user -u lcw-dashboard -f
systemctl --user restart lcw-dashboard
```

The unit reads the key from `~/.config/lcw-dashboard/.env` and sets
`DBUS_SESSION_BUS_ADDRESS` so `notify-send` can reach your notification daemon.

### macOS (launchd agent)

```sh
make install-macos
launchctl load -w ~/Library/LaunchAgents/com.lcw-dashboard.plist
```

`RunAtLoad` plus `KeepAlive` means it starts at login and restarts on crash.

launchd does not read `.env` files. Either uncomment `LCW_API_KEY` in the plist,
or point `ProgramArguments` at `packaging/lcw-dashboard-wrapper.sh`, which
sources `.env` first and keeps the key out of the plist.

```sh
launchctl list | grep lcw-dashboard
tail -f ~/Library/Logs/lcw-dashboard.log
launchctl kickstart -k "gui/$(id -u)/com.lcw-dashboard"    # restart
launchctl unload -w ~/Library/LaunchAgents/com.lcw-dashboard.plist
```

On first run macOS will ask to allow notifications for `osascript`.

### A second machine

See [docs/second-machine.md](docs/second-machine.md). The short version: the
10,000 daily credits belong to the **API key**, not the install, so two machines
on one key at the default 15s interval spend ~12,300/day and will not fit. Either
use a second key, or set `active_interval: 30s` on both.

### Opening it automatically

Set `server.open_browser: true` to open a tab at startup. For an always-open tab,
pinning `http://127.0.0.1:8787` in your browser is more reliable than launching
one from a background service.

## Verifying against the live API

```sh
LCW_API_KEY=... go run -tags smoke ./scripts/smoke
```

Costs about 15 credits. It calls every endpoint once, prints per-endpoint latency
and cost, probes every sort field, and measures the actual delta range across the
top 100 — which is how the documentation error below was found.

`go test ./...` needs no network and no key.

## Notes on the API

Worth knowing if you read the code:

- Every endpoint is `POST` with a JSON body. `GET` returns 405.
- Errors can arrive **under HTTP 200**, so the client checks for the error key
  before trusting the status code.
- `delta` is a multiplier where `1.0` means no change. Percent is
  `(delta - 1) * 100`. The docs claim the range is 0 to 2; **it is not.** Their
  own live data carries `delta.year = 16.9399` for ZEC, which is +1,593.99%.
  Nothing clamps it, the change columns are sized for it, and values above 1000%
  abbreviate to `+1.59k%`.
- An exact `delta` of `0` means "no data", not −100%, so it renders as a dash.
- `/coins/map` ignores rank entirely. Verified live: a coin at rank 736 arrives
  in the same one-credit request as Bitcoin at rank 1.
- **Duplicate tickers are disambiguated with leading underscores.** Hyperliquid
  is `______HYPE`, because `HYPE` is a dead 2016 token at rank 32,152 with no
  price at all. bittensor is `____TAO`; Solayer is `__LAYER`. Their own site
  displays the trimmed form, and so does this dashboard, but the real code is
  what goes to the API. **Add coins with the search box rather than typing a
  ticker** — it resolves the actual code for you.
- Codes the API does not recognise are omitted from the response rather than
  reported, so the dashboard diffs requested against returned and tells you.

Public-facing use requires attribution to Live Coin Watch. This runs on
localhost, so that does not apply, but the terms are worth reading if you expose
it.

## Troubleshooting

**Table is empty, banner mentions an API key.** The `.env` file is missing or
unreadable. `./lcw-dashboard -print-config` prints where it looks and whether it
found a key.

**Status reads `auth_failed`.** The key was rejected. Polling stops deliberately
rather than retrying a bad key. Fix `.env` and restart.

**Status reads `throttled`.** The daily budget is running low, so the interval
widened. `curl -s localhost:8787/api/state | jq .credits` shows the numbers. If
this happens daily, your intervals are too fast — see the budget table above.

**Credit drift warnings in the log.** Something else is using the same API key,
which halves your effective allowance.

**Seven tabs and one shows nothing.** HTTP/1.1 caps server-sent events at six
connections per origin. Close a tab.

**Notifications do not appear on Linux.** Check `notify-send --version` exists,
and that the service has the session bus:
`systemctl --user show-environment | grep DBUS`.

**Frontend changes not showing.** `make build` rebuilds the bundle; a plain
`go build` does not. `make dev` serves the bundle from disk instead.
