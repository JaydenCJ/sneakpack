# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-07-13

### Added

- `sneakpack pack <dir> --since <cursor> | --full`: compute the change set
  between a directory and a cursor and seal it into a single verifiable
  `.spk` bundle file; `--cursor-out` also writes the resulting cursor.
- `sneakpack apply <bundle> <dir>`: land a bundle with full pre-flight
  verification, chain-continuity checks, local-conflict detection, staged
  extraction (a failed apply leaves the tree untouched), and post-apply
  verification that the tree now matches the promised cursor byte for byte.
- Content-addressed cursors: a cursor is the SHA-256 identity of a full
  file manifest, so identical trees hash identically on any machine and a
  tampered or hand-edited cursor file is refused on load.
- Deterministic bundle format (gzip + tar, zeroed timestamps, canonical
  entry order): packing the same tree against the same cursor twice
  produces byte-identical bundles.
- `sneakpack verify <bundle>`: full offline validation — manifest
  cross-checks, payload completeness, per-file SHA-256 — reporting every
  problem found, not just the first.
- `sneakpack status`, `inspect`, `snapshot` and `cursor` subcommands for
  inspecting trees, bundles and destinations without changing anything.
- Conflict safety: modified and deleted files carry the hash the
  destination is expected to hold; local edits block the apply unless
  `--force` is given, and `--dry-run` previews the plan and conflicts.
- Zip-slip hardening: every path in a cursor or bundle is validated
  against absolute paths, `..` escapes and writes into the `.sneakpack`
  state directory.
- `.sneakpackignore` support (gitignore-style subset: basename and
  anchored globs, `**`, dir-only patterns, `!` negation, last match wins).
- Executable-bit tracking, symlink skip-with-warning, empty-dir pruning
  after deletes, and streaming SHA-256 so large files never load into memory.
- 90 deterministic offline tests (`go test ./...`) and an end-to-end
  `scripts/smoke.sh` that prints `SMOKE OK`.

[0.1.0]: https://github.com/JaydenCJ/sneakpack/releases/tag/v0.1.0
