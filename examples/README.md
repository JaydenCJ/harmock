# harmock examples

## petstore.har

A hand-crafted but realistic DevTools-style capture of a small pet-store
API at `api.example.test`, used by the README, the test suite, and
`scripts/smoke.sh`. It demonstrates every serving feature:

| Entries | Demonstrates |
|---|---|
| `GET /api/pets?limit=2`, `GET /api/pets/1` | query matching, recorded headers (including a `Content-Encoding: gzip` header that harmock correctly strips) |
| `POST /api/pets` | request-body matching (byte-exact and JSON-structural) |
| `GET /api/jobs/42` ×3 (`pending` → `running` → `done`) | sequential replay of duplicate recordings |
| `GET /logo.png` | base64-encoded binary bodies served byte-identical |
| `DELETE /api/pets/2` | bodyless 204 responses |
| `GET cdn.example.test/analytics.js` | a second host, for `--host` filtering |

Try it:

```bash
go build -o harmock ./cmd/harmock
./harmock routes examples/petstore.har
./harmock serve  examples/petstore.har --port 8099
```

## offline-demo.sh

`bash examples/offline-demo.sh` runs the full record-once/replay-forever
story against the bundled capture: it starts the server on a loopback
port, polls the job endpoint through its three recorded states, exercises
body matching and the admin reset, and prints each response. Everything
happens on `127.0.0.1`; no external network is touched.
