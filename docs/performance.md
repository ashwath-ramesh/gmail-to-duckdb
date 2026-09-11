# Search performance

This page records the local search architecture and the latest serial Linux measurements.

The original Apple M5 Mac, 24 GiB RAM, 8.09 GiB mailbox benchmark stays unvalidated. This Linux run does not meet every published target.

## Architecture

- DuckDB is the mailbox and the verify authority.
- A disposable SQLite FTS5 trigram index lives at `<db>.search/index.sqlite`.
- Search collects SQLite candidates, unpins that snapshot, then opens one DuckDB read-only transaction.
- DuckDB snapshots are per database, not per table. The first real table read is freshness (`sync_state`). That read pins the source snapshot for freshness, delta candidates, and verify.
- The index watermark `W` means the index covers `revision <= W`. The same DuckDB snapshot adds `revision > W`.
- Candidate verify wraps the id join in a subquery with `LIMIT` equal to the unique candidate batch. That barrier stops DuckDB from pushing the outer `ILIKE` into a full `TABLE_SCAN`. A verified plan dropped from 1.62 s to about 61 ms after the barrier.
- Verify keeps the candidate order (`internal_date DESC`, `id DESC`). It does not re-rank.
- The compatibility `fts` flag is true only when the index is ready. Ranking stays newest-first.
- Terms shorter than three characters cannot form trigrams. Those queries fall back to one guarded DuckDB `ILIKE` query (`literalSearch`). Fallback pagination stays that one SQL query.
- Label and read-only upserts do not bump `search_revision`. Date, deletion, and canonical-text changes do.
- Repair is a background walk. A new empty v4 database is not marked repair. A legacy database with existing rows is.
- Repair byte clipping sizes the live canonical text expression. Full and delta builds size cached `search_text` only.
- One row larger than the 8 MiB repair page still goes through as a single page.
- `serve` binds the listener first. Index build and repair run in the background. `/api/health` is liveness only and does not wait for the index.
- `/api/status` serves a cached envelope (`status_as_of`). One status worker refreshes it.
- Ready index plus new rows is `pending`, then `applyDelta`, then `ready`. That path is not `building`. Full rebuild is a separate `RebuildFTS` window.

## Limits

- API common-term search p95 on this host is 275.006 ms. That exceeds the 250 ms Mac design goal. Component common p95 is 308.142 ms. Do not treat all targets as met.
- Two-character absent search (`zz`) stays on the literal fallback: p50 1589.904 ms, p95 1644.821 ms.
- The 70k fixture is synthetic. Bodies repeat a small template. Body variability is limited. It is not a Gmail export.
- Baseline timings are uncached DuckDB `ILIKE` contains filters in newest-first order. They are not FTS rank and not the original app.
- Sample p95 is nearest-rank on 10 warm runs (the maximum observed). That small sample is not a production tail limit. Runtime warm caches were not cleared.
- Final runs are serial. An earlier parallel bench plus vuln run was memory-throttled and discarded. Even this serial cgroup cap can stretch tails. Do not compare these numbers to an unthrottled host or to a physical Mac.
- `fixture_reused` is true only when the mailbox already had rows before this run's seed. A new `GMAIL_BENCH_FIXTURE` path is not a reuse.
- A passed fixture is mutated. Store and HTTP append rows. Startup may remove only `<db>.search`. Use synthetic mailboxes only. Never point these benches at a real mailbox.

## Latest Linux results

Host for the serial campaign:

| Field | Value |
| --- | --- |
| OS | Native Linux |
| CPUs | 4 vCPUs, `GOMAXPROCS=4` |
| Host RAM | 7.6 GiB |
| Session cgroup | `memory.high` 1.5 GiB, `memory.max` 2 GiB |
| Store RSS | 855396 KiB (`serial-store-final.log`) |
| Corpus text | 295465408 B (~282 MiB) at 70250 messages |
| Search cache | 10449576 B (~9.97 MiB) |
| Body template | Limited variety; size grows with fixture appends |

Artifact log basenames only. Tests live in:

- `internal/store/search_bench_test.go`
- `internal/web/search_http_bench_test.go`
- `internal/web/browser_test.go`
- `cmd/gmail-to-duckdb/serve_bench_test.go`
- `internal/searchbench/corpus.go`

Sensitive message text is not logged. Query labels only.

### Phase row counts

The measured campaign reused a synthetic fixture that already had prior appends. Counts below are the artifact numbers.

| Phase | Artifact | Initial rows | After this phase |
| --- | --- | --- | --- |
| Startup, missing index | `serial-startup.log` | 70000 | 70000 (removes `<db>.search` only) |
| Initial index build | `serial-store.log` | 70000 (294543413 B cached text) | 70250 |
| Store queries | `serial-store-final.log` | 70250 (295465408 B) | 70500 |
| HTTP | `serial-http-final.log` | 70500 | 70750 |
| Browser | `serial-browser-final.log` | 70750 | 70750 plus 110 `alphatoken` and 20 `betatoken` markers |

A clean new fixture accumulates in this order if you follow the new-fixture commands below:

| Phase | Starts at | Appends |
| --- | --- | --- |
| Store | 0, then seeds 70000 | +250 → 70250 |
| HTTP | 70250 | +250 → 70500 |
| Browser | 70500 | +110 alpha, +20 beta |
| Startup last | browser final count | cache removal only |

### Index build versus query run

`serial-store.log` measured the initial full index build: 23757.751 ms on 70000 rows and 294543413 B cached text. Maintenance code did not change after that.

`serial-store-final.log` reused the ready index. That query run reported build 0. Its +250 incremental apply was 156.777 ms.

Do not treat 23757.751 ms as the final query-run build time.

### Store component search

Warm runs = 10. Current path uses the index except `short_absent` (`zz`), which uses `literalSearch`. Baseline is uncached contains filter, same corpus and newest-first order.

| Kind | Query | Current p50 (ms) | Current p95 (ms) | Baseline p50 (ms) | Baseline p95 (ms) |
| --- | --- | --- | --- | --- | --- |
| common | `sharedword` | 145.414 | 308.142 | 2097.649 | 2184.572 |
| selective | `token123` | 24.335 | 29.785 | 3359.169 | 3563.623 |
| absent | `absentxyz` | 4.225 | 5.531 | 3256.991 | 3342.087 |
| multi | `sharedword token2` | 41.129 | 50.777 | 4651.629 | 4727.180 |
| phrase | `"invoice leftover"` | 63.344 | 74.475 | 3263.785 | 3460.188 |
| short_absent | `zz` | 1589.904 | 1644.821 | 3303.314 | 3399.530 |

Median speedup versus that uncached baseline on the same corpus: about 14.4× common, 138× selective, 51.5× phrase. No FTS ranking.

### HTTP loopback

`serial-http-final.log`. Real loopback socket. Count SQL is `SELECT count(*) FROM messages`. All fixture rows were active. Search overlap uses start and end timestamps.

| Metric | p50 (ms) | p95 (ms) |
| --- | --- | --- |
| Search `sharedword` | 142.545 | 275.006 |
| Health | 0.155 | 0.275 |
| Count | 5.278 | 6.738 |
| Count during search | | 10.261 |
| Health during search | | 0.441 |
| Count during rebuild | | 4.766 |
| Health during rebuild | | 0.582 |

- Search overlap probes: 10. Rebuild overlap probes: 10. All succeeded.
- Incremental +250 apply: 144.082 ms (`pending`, then ready; not `building`).

### Browser UI

`serial-browser-final.log`. Same page, alternating selective marker queries. Input clock is `performance.now`. End clock is a `MutationObserver` on the result DOM plus exactly two `requestAnimationFrame` callbacks. 10 warm runs. No real mail. No screenshots.

| Metric | Value |
| --- | --- |
| Runs | 10 |
| Search p50 | 131.45 ms |
| Search p95 | 178.4 ms |
| Enter delay | 7.6 ms |
| `alphatoken` rows | 110 |
| Unread `betatoken` rows | 10 |

Typical UI time stays above 100 ms because of the 100 ms debounce. Stale-success, error, Enter, unread, and pagination assertions passed.

### Startup

`serial-startup.log`. Original 70000-row fixture. Each populated launch removed only `<db>.search`. Index built in the background after `listening on`.

| Metric | Value |
| --- | --- |
| Populated launches | 10 |
| Process-to-listening median | 35.180 ms |
| Process-to-listening p95 | 41.035 ms |
| Empty mailbox | 33.505 ms |
| Health and count pairs during `building` | 10 / 10 passed |

`process_to_listening_*` is the serve binary writing `listening on` after the listener is already bound. `duckdb_reopen_ms` in the store bench is `Open` only. That timer starts after `Close`. It is not process-to-listening.

## Mac targets (unvalidated)

| Surface | Target | This Linux serial run |
| --- | --- | --- |
| API search p95 | <= 250 ms | 275.006 ms (miss) |
| UI search p95 | <= 400 ms | 178.4 ms (Linux only) |
| Process to listening | <= 150 ms | 41.035 ms p95 (Linux only) |
| Health p95 | <= 25 ms | 0.275 ms idle; 0.582 ms during rebuild (Linux only) |

This Linux run does not accept or reject the Mac targets. The original Apple M5 Mac, 24 GiB RAM, 8.09 GiB mailbox benchmark is still unvalidated.

After the last SQL barrier, native Linux amd64:

- `go test -race -p 1 -count 1 -timeout 10m ./...` PASS
- `go vet -p 1 ./...` PASS
- `go build ./cmd/gmail-to-duckdb` PASS
- `govulncheck@v1.8.0 ./...` exit 0: no reachable vulnerabilities. The scan reported 0 vulnerabilities in imported packages and 3 advisories in required modules outside imported packages. That is not a vulnerability-free claim.

Other OS runtime checks remain CI.

## Reproduce

Use isolated temp dirs. Do not delete shared Go caches. Run these commands one at a time. Do not overlap benches or `govulncheck`.

Opt-in benches skip by default.

`GMAIL_BENCH_FIXTURE` may be:

- a synthetic directory (resolves to `bench.duckdb` even when that file is absent), or
- an explicit `.duckdb` path (the file may be absent; `store.Open` and the seeder create it).

The helper never resets an existing file. Other `stat` errors fail the run.

`GMAIL_BENCH_REBUILD=1` drops and rebuilds the ready index. Incremental IDs start after `COUNT(*)` and the max `id-NNNNNN` suffix.

HTTP benches use a real loopback socket. Count uses `POST /api/sql` with `SELECT count(*) FROM messages` and checks the decoded count. Do not label a probe concurrent unless the search and probe time windows overlap.

Browser timings record `performance.now()` from the input event to committed DOM plus exactly two animation frames.

A passed fixture is synthetic-only. Benches append rows. Startup may remove only `<db>.search`.

**New fixture:** run store first so it seeds. Then HTTP. Then browser. Startup last, because it removes only the derived cache.

**Existing fixture:** startup may run first. Then store (rebuilds if the cache is gone). Then HTTP. Then browser.

```bash
export GOTMPDIR="${GOTMPDIR:-$(mktemp -d)}"
export TMPDIR="${TMPDIR:-$(mktemp -d)}"
export GMAIL_BENCH70K=1
export GMAIL_BENCH_SERVE=1

# New synthetic mailbox. Parent exists. File may be absent.
export GMAIL_BENCH_FIXTURE="$(mktemp -d)/bench.duckdb"
# Existing synthetic directory or file:
# export GMAIL_BENCH_FIXTURE="$EXISTING_SYNTHETIC_DIR"

# optional: export GMAIL_BENCH_BIN="$HOME/bin/gmail-to-duckdb"
# optional: export GMAIL_BENCH_REBUILD=1

: "${GMAIL_PLAYWRIGHT_CHROME:?set to your Chromium binary}"
: "${GMAIL_PLAYWRIGHT_PKG:?set to your Playwright package directory}"

# New fixture order:
go test -p 1 -count 1 -timeout 60m -v -run TestSearchBenchmark70k ./internal/store
go test -p 1 -count 1 -timeout 60m -v -run TestSearchHTTPBenchmark70k ./internal/web
go test -p 1 -count 1 -timeout 10m -v -run TestBrowserSearchHarness ./internal/web
go test -p 1 -count 1 -timeout 10m -v -run TestServeProcessToListening ./cmd/gmail-to-duckdb

# Existing fixture may run TestServeProcessToListening first, then the three tests above.
```

Store logs `initial_corpus_messages` and `initial_corpus_text_bytes` after any first-time seed and before the +250 append. `final_row_count` is after the append. `fixture_reused` is `initial_rows > 0` before that seed.
