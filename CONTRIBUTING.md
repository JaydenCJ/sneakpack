# Contributing to sneakpack

Issues, discussions and pull requests are all welcome.

## Getting started

You need Go 1.22 or newer; there are no other dependencies of any kind.

```bash
git clone https://github.com/JaydenCJ/sneakpack.git
cd sneakpack
go build ./...
go test ./...
bash scripts/smoke.sh
```

`scripts/smoke.sh` builds the binary and drives the full courier loop —
full pack → verify → apply → incremental → conflict → forced recovery →
damaged-bundle detection; it must finish by printing `SMOKE OK`.

## Before you open a pull request

1. `gofmt -l .` reports nothing (formatting is enforced).
2. `go vet ./...` passes with no findings.
3. `go test ./...` passes (all 90 tests).
4. `bash scripts/smoke.sh` prints `SMOKE OK`.
5. Add tests for behavior changes; keep logic in pure, unit-testable
   packages (`snapshot`, `diff`, `bundle`, `apply`) rather than in the
   CLI layer.

## Ground rules

- Zero runtime dependencies is a core feature: the `go.mod` require list
  stays empty. Adding a dependency needs strong justification in the PR.
- No network calls, ever — sneakpack exists precisely because there is no
  connection. No telemetry.
- Safety before convenience: apply must never leave a tree half-changed,
  never overwrite a local edit without `--force`, and never write outside
  the destination root. Any change to these paths needs adversarial tests.
- The cursor and bundle formats are versioned; any change to either bumps
  the format version and keeps format-1 decoding intact.
- Determinism is part of the contract: identical inputs must keep
  producing byte-identical bundles.
- Code comments and doc comments are written in English.

## Reporting bugs

Please include the output of `sneakpack --version`, the exact command
line, `sneakpack inspect` output for any bundle involved, and — if you
can share it — a minimal pair of directories that reproduces the issue.

## Security

Please do not open public issues for security problems; use GitHub's
private vulnerability reporting on this repository instead.
