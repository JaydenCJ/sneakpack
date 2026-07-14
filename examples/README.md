# sneakpack examples

Everything here runs offline in a throwaway temp directory — nothing in
your working tree is touched.

## 1. The full courier loop, scripted

`courier-loop.sh` plays both machines: a disconnected "field" laptop and
the "base" workstation it can never reach directly. It walks the whole
cycle twice — full bundle, then an incremental one — and shows the
out-of-order protection firing when a bundle is replayed.

```bash
go build -o /tmp/sneakpack ./cmd/sneakpack   # from the repo root
PATH="/tmp:$PATH" examples/courier-loop.sh
```

Watch the cursor IDs in the output: the incremental bundle's `base` line
matches the destination's cursor exactly, which is why it is allowed to
land — and why the replay in the last step is not.

## 2. A realistic ignore file

`sneakpackignore.sample` is a starting point for a field-research tree:
scratch files, caches and equipment logs stay local, while an exception
re-includes the one log that matters. Copy it to the root of your source
directory as `.sneakpackignore` — it syncs with the tree, like
`.gitignore` does in git, so both sides always agree on what is tracked.

## 3. Things worth trying by hand

```bash
sneakpack inspect bundle.spk        # what would this bundle do?
sneakpack verify bundle.spk         # did it survive the USB stick?
sneakpack apply bundle.spk d --dry-run   # what would happen here?
sneakpack status ~/project          # what changed since the last hand-off?
```

`status` exits 1 when there are changes, so it works in scripts:
`sneakpack status src >/dev/null || sneakpack pack src --since last.cursor -o next.spk`.
