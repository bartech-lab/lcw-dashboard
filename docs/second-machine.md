# Setting up a second machine

Read the credit warning first. It is the one thing that bites.

## Credits are per API key, not per machine

The 10,000 daily credits belong to the **key**, not the install. Two machines
running the defaults share one allowance:

| Setup | Coin loop | Total/day | Fits in 10,000? |
| --- | --- | --- | --- |
| One machine, 15s | 5,760 | ~6,164 | yes |
| Two machines, 15s each | 11,520 | ~12,300 | **no** |
| Two machines, 30s each | 5,760 | ~6,300 | yes |
| Two machines, separate keys | 5,760 each | ~6,164 each | yes |

Pick one of these before you start:

**A. A second API key** (recommended). Live Coin Watch issues one key per
account, so this needs a second account. Each machine then has its own full
allowance and nothing changes in the config.

**B. Share the key and halve the rate.** Set this on *both* machines:

```yaml
poll:
  active_interval: 30s
overview:
  interval: 600s
search_index:
  enabled: false      # only one machine needs to build the index
```

Leave the index enabled on whichever machine you use more.

**C. Share the key and only run one at a time.** Fine if the second machine is a
laptop you use occasionally. Just do not enable the service at login on both.

You will know if you get this wrong. The server reconciles against `/credits`
every 15 minutes and logs a warning when the API's count exceeds its own:

```
level=WARN msg="credit drift detected; another client may share this API key" drift=1840
```

The dashboard also shows a `Throttled` pill once the budget tightens.

## Install

Needs Go 1.24 or newer. Nothing else: no Node, no `node_modules`.

```sh
git clone https://github.com/bartech-lab/lcw-dashboard.git
cd lcw-dashboard

mkdir -p ~/.config/lcw-dashboard
printf 'LCW_API_KEY=%s\n' 'your-key-here' > ~/.config/lcw-dashboard/.env
chmod 600 ~/.config/lcw-dashboard/.env

cp config.example.yaml ~/.config/lcw-dashboard/config.yaml
# Apply option B above now if you are sharing a key.

make build
./lcw-dashboard -print-config     # sanity check: paths, intervals, credit projection
```

`-print-config` prints the projected daily spend and warns if it will not fit.
Check that line before going further.

Then confirm the key works, for about 15 credits:

```sh
go run -tags smoke ./scripts/smoke
```

Every endpoint should report `ok`. If `/credits` fails, the key is wrong.

## Run it at login

### Linux

```sh
make install-linux
systemctl --user enable --now lcw-dashboard
loginctl enable-linger "$USER"     # required, or it dies at logout
```

```sh
systemctl --user status lcw-dashboard
journalctl --user -u lcw-dashboard -f
systemctl --user restart lcw-dashboard
```

The unit reads the key from `~/.config/lcw-dashboard/.env` and sets
`DBUS_SESSION_BUS_ADDRESS` so `notify-send` can reach the notification daemon.

### macOS

```sh
make install-macos
launchctl load -w ~/Library/LaunchAgents/com.lcw-dashboard.plist
```

launchd does not read `.env` files. Two options:

1. Uncomment `LCW_API_KEY` in
   `~/Library/LaunchAgents/com.lcw-dashboard.plist`, or
2. Point `ProgramArguments` at `packaging/lcw-dashboard-wrapper.sh`, which
   sources `.env` first and keeps the key out of the plist.

```sh
launchctl list | grep lcw-dashboard
tail -f ~/Library/Logs/lcw-dashboard.log
launchctl kickstart -k "gui/$(id -u)/com.lcw-dashboard"
launchctl unload -w ~/Library/LaunchAgents/com.lcw-dashboard.plist
```

macOS asks once to allow notifications for `osascript`. Approve it or the
`native` alert sink stays silent. Check which sinks came up:

```
level=INFO msg="alert sinks" active="[native log]"
```

## Moving settings across machines

The two halves live in different places on purpose.

**Server settings** are `~/.config/lcw-dashboard/config.yaml`. Copy it over, then
re-apply any per-machine interval change from option B.

**Your watchlist** is `~/.local/state/lcw-dashboard/watchlist.json`. Copy it while
the service is stopped, or set it through the API:

```sh
curl -X PUT -H 'content-type: application/json' \
  -d '{"codes":["BTC","ETH","SOL","______HYPE","____TAO"]}' \
  http://127.0.0.1:8787/api/watchlist
```

**View settings** (visible columns, order, theme, density, sort, locale) live in
the browser's `localStorage` under `lcwd:prefs`, so they are per browser profile
and do not transfer. Set them once through the **Layout** button; it takes a
minute. Save a named preset if you want the same layout twice.

**Price history and the credit ledger** do not transfer and should not. History is
recorded from what each install polls, and the ledger tracks that machine's own
spend.

## Coin codes are not the tickers you expect

Live Coin Watch pads duplicated tickers with leading underscores. The dashboard
displays the trimmed form, as their own site does, but the API wants the real
code:

| Coin | API code | Displayed |
| --- | --- | --- |
| Hyperliquid | `______HYPE` | HYPE |
| bittensor | `____TAO` | TAO |
| Solayer | `__LAYER` | LAYER |
| OFFICIAL TRUMP | `_______________________________TRUMP` | TRUMP |

Plain `HYPE` is a dead 2016 token at rank 32,152 with no price at all. **Add coins
with the search box**, which resolves the real code, rather than typing a ticker.

## Troubleshooting a fresh install

**`make build` fails on the Go version.** Needs 1.24+. `go version`.

**Table stays empty and a banner mentions the API key.** The `.env` is missing or
unreadable. `./lcw-dashboard -print-config` shows where it looked and whether it
found one.

**Status reads `auth_failed`.** The key was rejected, and polling stops
deliberately rather than retrying a bad key. Fix `.env` and restart.

**`Throttled` in the header on day one.** Another install is using the same key.
See the credit section above.

**No desktop notifications on Linux.** Check `notify-send --version` exists and
the service has the bus: `systemctl --user show-environment | grep DBUS`.

**Port already taken.** `./lcw-dashboard -listen 127.0.0.1:8788`, or set
`server.listen` in the config.
