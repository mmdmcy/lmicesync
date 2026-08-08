#!/usr/bin/env bash
# Install lmicesync for the current user: binary, config, approval file, and
# systemd user units. Does not require root.
set -euo pipefail

SRC=$(cd "$(dirname "$0")/.." && pwd)
BIN_DIR="$HOME/.local/bin"
SYS_UNIT_DIR="$HOME/.config/systemd/user"
CONFIG_DIR="$HOME/.config/lmicesync"
STATE_DIR="$HOME/.local/state/lmicesync"

mkdir -p "$BIN_DIR" "$SYS_UNIT_DIR" "$CONFIG_DIR" "$STATE_DIR"

echo "== build =="
(cd "$SRC" && go build -o bin/lmicesync ./cmd/lmicesync)

echo "== install binary =="
install -m 0755 "$SRC/bin/lmicesync" "$BIN_DIR/lmicesync"

echo "== config =="
if [ ! -f "$CONFIG_DIR/config.json" ]; then
	"$BIN_DIR/lmicesync" init
	echo "  wrote $CONFIG_DIR/config.json - set a token:"
	echo "    edit $CONFIG_DIR/config.json (token) or export LMICESYNC_TOKEN"
fi

if [ ! -f "$CONFIG_DIR/approval.json" ]; then
	echo '[]' > "$CONFIG_DIR/approval.json"
	echo "  wrote $CONFIG_DIR/approval.json - approve peers:"
	echo "    echo '[\"<peer-device>\"]' > $CONFIG_DIR/approval.json"
fi

echo "== systemd user units =="
install -m 0644 "$SRC/systemd/"lmicesync-*.service "$SRC/systemd/"lmicesync-*.timer "$SYS_UNIT_DIR/"
systemctl --user daemon-reload
systemctl --user enable --now lmicesync-save.timer lmicesync-sync.timer 2>/dev/null || true

echo "== done =="
echo "  enable the LAN agent when ready:  systemctl --user enable --now lmicesync-serve.service"
echo "  preview:                          lmicesync peers"
echo "  manual run:                       lmicesync save && lmicesync sync"
