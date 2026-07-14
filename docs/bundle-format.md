# The sneakpack formats: cursors and bundles

Two artifacts cross the airgap: **cursor files** (where a tree is) and
**bundle files** (how to move a tree forward). Both are versioned; this
document describes format 1, as written and read by sneakpack 0.1.0.

## Cursors

A cursor is a JSON snapshot of a directory tree:

```json
{
  "format": 1,
  "id": "9a1ef7189f18…",
  "files": [
    { "path": "notes/day1.md", "size": 8, "sha256": "1052…" },
    { "path": "tool.sh", "size": 10, "exec": true, "sha256": "ab61…" }
  ]
}
```

- `path` is slash-separated, relative, and must be *clean*: no leading
  `/`, no `.`/`..` segments, no backslashes, and never inside
  `.sneakpack/`. Entries are sorted by path; unsorted files are rejected.
- Only regular files appear. Directories are implied by paths (empty
  directories are not tracked); symlinks and special files are skipped
  with a warning at snapshot time.
- `exec` records whether any executable bit was set; it is part of the
  tree's identity.

### The cursor ID

`id` is the SHA-256 of a canonical serialization of the file list — one
`path NUL size NUL x|- NUL sha256 LF` record per file in path order. This
makes cursors:

- **content-addressed** — identical trees produce identical IDs on any
  machine, with no shared state and no clocks involved;
- **tamper-evident** — `Load` recomputes the ID and refuses a cursor
  whose stored ID no longer matches its own file list.

The empty tree has a well-defined ID (the hash of zero records), which is
the implicit starting cursor of every destination.

## Bundles (`.spk`)

A bundle is a gzip-compressed tar stream with a fixed layout:

| Entry | Content |
| --- | --- |
| `sneakpack.json` | the manifest — always the first entry |
| `data/<path>` | one entry per added or modified file, canonical path order |

The manifest carries: the format version, the writing tool, the
**base cursor** (what the destination must be at), the **target cursor**
(what it will be at afterwards), the change set (`added`, `modified`,
`deleted` — each modified/deleted entry pins the `old_sha256` the
destination is expected to hold), and the **complete target snapshot**,
so the receiving side can rebuild its cursor without trusting its own
filesystem walk.

### Determinism

All tar and gzip timestamps are zeroed, no owner names or ids are
recorded, and entries are written in canonical order — so packing the
same tree against the same cursor twice yields **byte-identical files**.
Couriers and sync scripts can deduplicate bundles by plain content hash.

### Verification

`sneakpack verify` (also the first step of every `apply`) checks, fully
offline:

1. the manifest is internally consistent — the target snapshot's ID
   recomputes, the target cursor equals it, every change agrees with the
   target snapshot, no duplicate change paths;
2. every path passes the same safety rules as cursors (this is the
   zip-slip guard — a bundle can never write outside the destination
   root, nor into `.sneakpack/` to forge a cursor);
3. the payload is complete and exact — every `data/` entry matches its
   manifest size and SHA-256, nothing is missing, duplicated, or smuggled
   in outside `data/`.

Hashes are checked again as payloads are extracted during apply, so even
a bundle damaged *after* verification cannot land bad bytes.

## Compatibility

Format numbers are checked on read; an unknown cursor or bundle format is
refused by name rather than misparsed. Any future change to either format
bumps the version and keeps format-1 decoding intact (see
CONTRIBUTING.md, "Ground rules").
