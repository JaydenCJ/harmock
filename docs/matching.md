# How harmock picks an entry

Every incoming request is matched against the capture in two phases:
a hard filter, then a score ranking. The result is fully deterministic —
the same capture plus the same request sequence always yields the same
entries in the same order.

## Phase 1 — hard filter

An entry is a candidate only if **method** (case-insensitive) and **path**
(exact string) both match. Host is not compared at match time; use
`--host` to restrict which entries are loaded, and `--strip-prefix` to
rewrite recorded paths before serving.

If no candidate survives, the request is unmatched: harmock answers with
`--fallback-status` (default 404) and a JSON payload listing up to three
near-miss suggestions — the same path under a different method first, then
same-method paths sharing the longest prefix.

## Phase 2 — scoring

Candidates are ranked by the sum of three components:

| Component | Signal | Score |
|---|---|---|
| Query | identical parameter set (after `--ignore-query` filtering) | +100 |
| Query | per-key: equal values / different values / present on one side only | +8 / −4 / −2 |
| Body | byte-identical request body | +50 |
| Body | structurally equal JSON (key order and whitespace ignored) | +40 |
| Body | both sides have a body but they differ | −10 |
| Body | one side has a body, the other does not (`--match-body always` only) | −25 |
| Header | per opted-in header (`--match-header`): equal / different | +10 / −5 |

Weights are ordered so that an exact body beats query differences, and an
exact query beats partial overlap — a `POST` with the recorded payload
always wins over a `POST` with a different one.

Body scoring modes (`--match-body`):

- `auto` (default) — bodies are compared only when both the live request
  and the recording carry one.
- `always` — a body on one side but not the other is a strong negative.
- `never` — bodies are ignored entirely.

## Phase 3 — strategy (tie-breaking)

All top-scoring candidates form a group ordered by recorded time
(`startedDateTime`, stable for ties). `--strategy` decides which one serves:

| Strategy | Behavior |
|---|---|
| `sequential` (default) | Walk the group in recorded order, one entry per request, then stick at the last. Cursors are independent per request key and rewind on `POST /__harmock__/reset`. |
| `first` | Always the earliest recording. |
| `last` | Always the latest recording. |

Sequential is what makes polling flows replayable: a job endpoint captured
as `pending → running → done` replays exactly that way, then keeps
answering `done` forever.

## What harmock rewrites on the way out

Recorded response headers are replayed verbatim except:

- `Content-Encoding`, `Content-Length` — HAR stores the *decoded* body, so
  the recorded values would corrupt the response; length is recomputed.
- Hop-by-hop headers (`Connection`, `Transfer-Encoding`, `Keep-Alive`,
  `Upgrade`, `TE`, `Trailer`, proxy headers) — connection management
  belongs to the live server.
- With `--cors`, all `Access-Control-*` decisions are replaced by a
  permissive set reflecting the live `Origin`, and unrecorded `OPTIONS`
  preflights are answered with 204.

Every reply carries `X-Harmock-Entry: <index>` naming the entry that
served it — the same index printed by `harmock routes`.
