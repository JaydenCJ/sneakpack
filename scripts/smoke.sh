#!/usr/bin/env bash
# End-to-end smoke test for sneakpack. No network, idempotent, runs from a
# clean tree. This script plus 'go test ./...' is the whole verification
# story — the repository intentionally ships no CI.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

fail() {
  echo "SMOKE FAIL: $*" >&2
  exit 1
}

# expect <pattern> <cmd...>: run cmd, assert exit 0 and that stdout
# contains pattern. Captures output first to avoid grep -q pipe races.
expect() {
  local pattern="$1"
  shift
  local out
  out="$("$@")" || fail "command failed: $*"
  echo "$out" | grep -q "$pattern" || fail "output of '$*' missing '$pattern'"
}

BIN="$WORKDIR/sneakpack"
SRC="$WORKDIR/field"    # the disconnected source machine
DST="$WORKDIR/base"     # the destination back at base
mkdir -p "$SRC/notes" "$DST"

echo "[1/10] build"
(cd "$ROOT" && go build -o "$BIN" ./cmd/sneakpack) || fail "build failed"

echo "[2/10] --version matches the manifest version"
VERSION_OUT="$("$BIN" --version)"
[ "$VERSION_OUT" = "sneakpack 0.1.0" ] || fail "unexpected version output: $VERSION_OUT"

echo "[3/10] pack a full bundle of the source tree"
echo "day one" > "$SRC/notes/day1.md"
printf 'id,temp\n1,20.5\n' > "$SRC/readings.csv"
expect "packed 2 change(s)" \
  "$BIN" pack "$SRC" --full -o "$WORKDIR/full.spk" --cursor-out "$WORKDIR/base.cursor"

echo "[4/10] verify the bundle offline, then inspect it"
expect "verify full.spk: ok" "$BIN" verify "$WORKDIR/full.spk"
expect "A  notes/day1.md" "$BIN" inspect "$WORKDIR/full.spk"

echo "[5/10] apply to a fresh destination and self-verify"
expect "verified: tree matches cursor" "$BIN" apply "$WORKDIR/full.spk" "$DST"
diff -r "$SRC" "$DST" --exclude .sneakpack >/dev/null || fail "trees differ after full apply"

echo "[6/10] status sees changes; pack an incremental bundle"
echo "day two" > "$SRC/notes/day2.md"
printf 'id,temp\n1,20.5\n2,21.0\n' > "$SRC/readings.csv"
rm "$SRC/notes/day1.md"
set +e
STATUS_OUT="$("$BIN" status "$SRC" --since "$WORKDIR/base.cursor")"
STATUS_CODE=$?
set -e
[ "$STATUS_CODE" -eq 1 ] || fail "dirty status should exit 1, got $STATUS_CODE"
echo "$STATUS_OUT" | grep -q "1 added, 1 modified, 1 deleted" || fail "status summary wrong"
expect "packed 3 change(s)" \
  "$BIN" pack "$SRC" --since "$WORKDIR/base.cursor" -o "$WORKDIR/day2.spk"

echo "[7/10] out-of-order protection: a replayed bundle is refused"
expect "1 added, 1 modified, 1 deleted" "$BIN" apply "$WORKDIR/day2.spk" "$DST"
set +e
REPLAY_OUT="$("$BIN" apply "$WORKDIR/day2.spk" "$DST" 2>&1)"
REPLAY_CODE=$?
set -e
[ "$REPLAY_CODE" -eq 1 ] || fail "replay should exit 1, got $REPLAY_CODE"
echo "$REPLAY_OUT" | grep -q "does not chain" || fail "replay error message wrong"

echo "[8/10] local edits block an apply; --force overrides"
echo "gen three" > "$SRC/notes/day3.md"
printf 'id,temp\n1,20.5\n2,21.0\n3,19.8\n' > "$SRC/readings.csv"
expect "cursor" "$BIN" cursor "$DST" -o "$WORKDIR/dst.cursor"
expect "packed 2 change(s)" \
  "$BIN" pack "$SRC" --since "$WORKDIR/dst.cursor" -o "$WORKDIR/day3.spk"
echo "locally edited" > "$DST/readings.csv"
set +e
CONFLICT_OUT="$("$BIN" apply "$WORKDIR/day3.spk" "$DST" 2>&1)"
CONFLICT_CODE=$?
set -e
[ "$CONFLICT_CODE" -eq 1 ] || fail "conflict should exit 1, got $CONFLICT_CODE"
echo "$CONFLICT_OUT" | grep -q "local conflict(s)" || fail "conflict message wrong"
expect "verified: tree matches cursor" "$BIN" apply "$WORKDIR/day3.spk" "$DST" --force
diff -r "$SRC" "$DST" --exclude .sneakpack >/dev/null || fail "trees differ after forced apply"

echo "[9/10] a damaged bundle is detected before it can touch anything"
head -c 200 "$WORKDIR/day3.spk" > "$WORKDIR/damaged.spk"
set +e
"$BIN" verify "$WORKDIR/damaged.spk" >/dev/null 2>&1
DAMAGED_CODE=$?
set -e
[ "$DAMAGED_CODE" -ne 0 ] || fail "damaged bundle verified clean"

echo "[10/10] cursor round trip: a clean tree packs nothing"
"$BIN" cursor "$DST" -o "$WORKDIR/back.cursor" >/dev/null || fail "cursor export failed"
set +e
NOPACK_OUT="$("$BIN" pack "$SRC" --since "$WORKDIR/back.cursor" -o "$WORKDIR/none.spk" 2>&1)"
NOPACK_CODE=$?
set -e
[ "$NOPACK_CODE" -eq 1 ] || fail "clean tree should exit 1 on pack, got $NOPACK_CODE"
echo "$NOPACK_OUT" | grep -q "nothing changed" || fail "empty-pack message wrong"
[ ! -e "$WORKDIR/none.spk" ] || fail "empty pack must not write a bundle"

echo "SMOKE OK"
