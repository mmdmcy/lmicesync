#!/usr/bin/env bash
# Smoke test for lmicesync: auto-save, bundle serve, peer sync, merge into
# auto/merged, conflict surfacing. No public remotes are involved.
set -euo pipefail

LMICESYNC="${LMICESYNC:-$(dirname "$0")/../lmicesync}"
if [ ! -x "$LMICESYNC" ]; then
	LMICESYNC=$(dirname "$0")/../bin/lmicesync
fi
if [ ! -x "$LMICESYNC" ]; then
	echo "build first: go build -o bin/lmicesync ./cmd/lmicesync" >&2
	exit 1
fi

W=$(mktemp -d)
trap 'rm -rf "$W"' EXIT
A_ROOT=$W/root-a
B_ROOT=$W/root-b
CONFIG_A=$W/config-a.json
CONFIG_B=$W/config-b.json
APPROVAL=$W/approval.json
TOKEN=test-token-123
PORT_A=17370
PORT_B=17371

echo "== shared base history for A and B (as if both cloned the same remote) =="
mkdir -p "$W/base"
git -C "$W/base" init -q -b main repo
git -C "$W/base/repo" config user.email "base@test"
git -C "$W/base/repo" config user.name "Base"
echo "base" > "$W/base/repo/base.txt"
git -C "$W/base/repo" add -A
git -C "$W/base/repo" commit -q -m "base"
git clone -q "$W/base/repo" "$A_ROOT/repo"
git clone -q "$W/base/repo" "$B_ROOT/repo"
git -C "$A_ROOT/repo" config user.email "a@test"
git -C "$A_ROOT/repo" config user.name "A"
git -C "$B_ROOT/repo" config user.email "b@test"
git -C "$B_ROOT/repo" config user.name "B"
# the smoke test must never touch public remotes; drop the clone's origin
git -C "$A_ROOT/repo" remote remove origin
git -C "$B_ROOT/repo" remote remove origin

echo "== device A: create dirty repo =="
echo "dirty change" > "$A_ROOT/repo/base.txt"

cat > "$CONFIG_A" <<EOF
{
  "device": "node-a",
  "roots": ["$A_ROOT"],
  "http_addr": ":$PORT_A",
  "beacon_port": 11001,
  "token": "$TOKEN",
  "require_approval": true,
  "approval_file": "$APPROVAL",
  "state_dir": "$W/state-a"
}
EOF

echo "== save: snapshot onto auto/node-a =="
OUT_A=$("$LMICESYNC" --config "$CONFIG_A" save)
echo "$OUT_A" | grep -q "saved $A_ROOT/repo" || { echo "save failed: $OUT_A"; exit 1; }
git -C "$A_ROOT/repo" rev-parse --verify refs/heads/auto/node-a >/dev/null || { echo "no auto branch"; exit 1; }
echo "== save again with clean worktree: no-op =="
git -C "$A_ROOT/repo" checkout -q -- base.txt
"$LMICESYNC" --config "$CONFIG_A" save | grep -q "0/1 repositories saved" || exit 1

echo "== serve on node A =="
"$LMICESYNC" --config "$CONFIG_A" serve &
PID_A=$!
trap 'kill $PID_A 2>/dev/null || true; rm -rf "$W"' EXIT
sleep 1
curl -sf "http://127.0.0.1:$PORT_A/healthz" >/dev/null || { echo "healthz failed"; exit 1; }
curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer wrong" "http://127.0.0.1:$PORT_A/v1/sync/repo" | grep -q 401 || { echo "auth check failed"; exit 1; }
curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $TOKEN" "http://127.0.0.1:$PORT_A/v1/sync/repo?device=node-b" | grep -q 403 || { echo "approval gate failed"; exit 1; }
echo "[]" > "$APPROVAL"
echo '["node-b"]' > "$APPROVAL"
curl -sf -H "Authorization: Bearer $TOKEN" "http://127.0.0.1:$PORT_A/v1/sync/repo?device=node-b" -o "$W/bundle-a.bundle" || { echo "bundle fetch failed"; exit 1; }
git -C "$A_ROOT/repo" bundle verify "$W/bundle-a.bundle" >/dev/null || { echo "bundle verify failed"; exit 1; }

echo "== device B: fetch and merge via beacon discovery =="
cat > "$CONFIG_B" <<EOF
{
  "device": "node-b",
  "roots": ["$B_ROOT"],
  "http_addr": ":$PORT_B",
  "beacon_port": 11001,
  "token": "$TOKEN",
  "require_approval": false,
  "approval_file": "$APPROVAL",
  "state_dir": "$W/state-b"
}
EOF

OUT_B=$("$LMICESYNC" --config "$CONFIG_B" sync)
echo "$OUT_B" | grep -q "merged repo" || { echo "sync failed: $OUT_B"; exit 1; }
git -C "$B_ROOT/repo" rev-parse --verify refs/heads/auto/merged >/dev/null || { echo "no auto/merged"; exit 1; }
git -C "$B_ROOT/repo" ls-tree -r --name-only refs/heads/auto/merged | grep -q base.txt || { echo "merged tree incomplete"; exit 1; }

echo "== conflict surfacing =="
echo "b's own change" > "$B_ROOT/repo/base.txt"
"$LMICESYNC" --config "$CONFIG_B" save | grep -q "saved $B_ROOT/repo" || exit 1
# now A also changes the same line (dirty again)
echo "a's other change" > "$A_ROOT/repo/base.txt"
"$LMICESYNC" --config "$CONFIG_A" save | grep -q "saved $A_ROOT/repo" || exit 1
OUT_C=$("$LMICESYNC" --config "$CONFIG_B" sync --host "127.0.0.1:$PORT_A")
echo "$OUT_C" | grep -q "CONFLICT" || { echo "conflict not surfaced: $OUT_C"; exit 1; }

echo "== no public remote references created =="
if git -C "$B_ROOT/repo" config --get-regexp '^remote\.' 2>/dev/null | grep -q .; then
	echo "remote configured unexpectedly" >&2
	exit 1
fi

echo "lmicesync smoke: PASS"
