# Contributing to harmock

Issues, discussions and pull requests are all welcome.

## Getting started

You need Go ≥1.22; nothing else. The smoke script additionally uses `curl`.

```bash
git clone https://github.com/JaydenCJ/harmock && cd harmock
go build ./...
go test ./...
bash scripts/smoke.sh
```

`scripts/smoke.sh` builds the binary, serves the bundled example capture on
a loopback port, and asserts on real HTTP responses across every subcommand
(replay, sequential state, admin reset, exit codes); it must finish by
printing `SMOKE OK`.

## Before you open a pull request

1. `gofmt -l .` reports nothing (formatting is enforced).
2. `go vet ./...` passes with no findings.
3. `go test ./...` passes (90 deterministic tests, no network).
4. `bash scripts/smoke.sh` prints `SMOKE OK`.
5. Add tests for behavior changes; keep logic in pure, unit-testable
   modules (parsing, matching, and rendering never touch sockets — only
   `cmdServe` opens a listener).

## Ground rules

- Keep dependencies at zero — harmock is standard library only, and that is
  the headline feature. Adding one needs strong justification in the PR.
- The server binds `127.0.0.1` by default and must keep doing so; exposing
  it further is an explicit user decision (`--addr`). No telemetry, ever.
- Determinism first: the same capture plus the same request sequence must
  produce byte-identical responses, including all orderings. Anything
  time-based must go through an injectable hook (see `Config.Sleep`).
- Matching behavior is contract: score weights live in one place
  (`internal/match/matcher.go`) and every change needs a test showing the
  request shape it fixes.
- Code comments and doc comments are written in English.

## Reporting bugs

Include the output of `harmock version`, the full command you ran, the
per-request log lines from stderr, and — for matching problems — the output
of `harmock routes <capture.har>` plus the exact request you sent (method,
path, query, body), since that is exactly what the matcher sees. HAR files
often contain cookies and tokens; strip them before attaching a capture.

## Security

Please do not open public issues for security problems; use GitHub's
private vulnerability reporting on this repository instead.
