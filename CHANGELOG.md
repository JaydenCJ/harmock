# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-07-13

### Added

- HAR 1.2 parsing tolerant of real-world exports: base64 response bodies
  (including whitespace-wrapped), HTTP/2 pseudo-header removal, aborted
  entries (status 0), non-HTTP schemes, and unparsable URLs are skipped
  with per-entry reasons instead of failing the file.
- Score-based request matching: exact method + path required, then query
  parameters (with `--ignore-query` for cache busters), request bodies
  (byte-exact or structurally-equal JSON via `--match-body auto|always|never`),
  and opt-in headers (`--match-header`) rank the candidates.
- Replay strategies for duplicate recordings: `sequential` (default —
  walks recordings in captured order and sticks at the last one), `first`,
  and `last`; sequential cursors are per request key and rewindable.
- `serve` subcommand binding `127.0.0.1` with recorded status/header/body
  replay, hop-by-hop and `Content-Encoding`/`Content-Length` hygiene,
  `--host` filtering, `--strip-prefix`, `--cors` override with preflight
  answers, `--latency none|record|<ms>` simulation (record capped at 3 s),
  and a configurable `--fallback-status` for unmatched requests.
- Diagnosable 404s: unmatched requests receive a JSON payload naming the
  request plus up to three near-miss suggestions (same path with another
  method, then most-similar paths).
- Admin surface under `/__harmock__/`: `health`, `routes`, and POST
  `reset` (rewinds sequential replay between test runs); removable with
  `--no-admin`.
- `routes` (aligned table or JSON, with replay-position annotations),
  `show` (full entry detail by index or request line), and `check`
  (serve-readiness lint gating exit code 1 on error-level findings).
- Stable exit codes: 0 ok, 1 check findings, 2 usage, 3 runtime.
- Bundled example capture (`examples/petstore.har`), an offline demo
  script, and a matching reference (`docs/matching.md`).
- 90 deterministic offline tests (unit + in-process CLI integration, no
  sockets) and `scripts/smoke.sh` exercising the served API end-to-end.

[0.1.0]: https://github.com/JaydenCJ/harmock/releases/tag/v0.1.0
