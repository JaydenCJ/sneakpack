#!/usr/bin/env bash
# The complete sneakpack courier loop, played out between two directories
# standing in for two machines that never share a network. Requires the
# sneakpack binary on PATH (see examples/README.md).
set -euo pipefail

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
FIELD="$WORK/field"   # the disconnected source
BASE="$WORK/base"     # the destination back home
USB="$WORK/usb"       # the removable media in between
mkdir -p "$FIELD/notes" "$BASE" "$USB"

banner() { printf '\n=== %s ===\n' "$*"; }

banner "day 1 on the field laptop: collect data"
echo "site A, calm sea"            > "$FIELD/notes/day1.md"
printf 'id,temp\n1,20.5\n'         > "$FIELD/readings.csv"

banner "first hand-off: pack everything (--full), keep the cursor"
sneakpack pack "$FIELD" --full -o "$USB/full.spk" --cursor-out "$WORK/sent.cursor"

banner "back at base: verify the stick survived the trip, then apply"
sneakpack verify "$USB/full.spk"
sneakpack apply "$USB/full.spk" "$BASE"

banner "day 2 in the field: new notes, corrected data, old file removed"
echo "site B, heavy swell"         > "$FIELD/notes/day2.md"
printf 'id,temp\n1,20.4\n2,21.0\n' > "$FIELD/readings.csv"
rm "$FIELD/notes/day1.md"

banner "what would travel? (status is free — nothing is written)"
sneakpack status "$FIELD" --since "$WORK/sent.cursor" || true

banner "second hand-off: only the changes travel this time"
sneakpack pack "$FIELD" --since "$WORK/sent.cursor" -o "$USB/day2.spk" \
  --cursor-out "$WORK/sent.cursor"
sneakpack apply "$USB/day2.spk" "$BASE"

banner "the mirror is now byte-identical"
diff -r "$FIELD" "$BASE" --exclude .sneakpack \
  && echo "trees match"

banner "replaying the same bundle is refused (chain protection)"
sneakpack apply "$USB/day2.spk" "$BASE" || echo "refused, as it should be"
