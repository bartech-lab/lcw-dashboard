#!/bin/sh
# macOS helper: launchd does not read .env files, so this exports the key from
# the config directory before starting the binary. Point the plist's
# ProgramArguments at this script instead of the binary if you prefer keeping the
# key out of the plist.
set -e

ENV_FILE="${XDG_CONFIG_HOME:-$HOME/.config}/lcw-dashboard/.env"
if [ -f "$ENV_FILE" ]; then
  set -a
  . "$ENV_FILE"
  set +a
fi

exec "$HOME/.local/bin/lcw-dashboard" "$@"
