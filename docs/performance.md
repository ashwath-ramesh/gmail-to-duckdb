# Performance

Copy the machine record below for each additional computer or VM, replacing its specifications and measurements.

Search times are medians of 10 warm runs, returning up to 50 results, newest first. Initial index build is one measurement. These measure local database operations on synthetic messages with repeated bodies; Gmail downloads and browser rendering are excluded. Two-character queries use a literal-search fallback.

## Ubuntu VM

| Field | Value |
| --- | --- |
| Tested | 2026-09-15; commit `2dab808`; Go 1.26.8 |
| Environment | Ubuntu 26.04, amd64, KVM virtual machine |
| CPU | AMD EPYC Rome; 4 vCPUs; `GOMAXPROCS=4` |
| RAM | 7.6 GiB; benchmark throttled above 1.5 GiB, capped at 2 GiB |
| Storage | QEMU virtual disk, ext4; `TMPDIR=/var/tmp` |
| Dataset | 70,000 synthetic messages; 280.9 MiB searchable text |
| Initial index build | 25.0 s |
| Common term: `sharedword` | 135.6 ms |
| Selective term: `token123` | 24.4 ms |
| No matches: `absentxyz` | 3.8 ms |
| Multiple terms: `sharedword token2` | 42.2 ms |
| Phrase: `"invoice leftover"` | 63.3 ms |
| Two-character fallback: `zz` | 1,583.9 ms |

## Reproduce

Use the same commit and Go version when comparing machines. From the repository root, set up the benchmark environment:

**Shell (Linux/macOS)**

```sh
export GMAIL_BENCH70K=1
unset GMAIL_BENCH_FIXTURE GMAIL_BENCH_REBUILD
```

**PowerShell (Windows)**

```powershell
$env:GMAIL_BENCH70K = '1'
Remove-Item Env:GMAIL_BENCH_FIXTURE, Env:GMAIL_BENCH_REBUILD -ErrorAction SilentlyContinue
```

Then run:

```sh
go test -mod=readonly -p 1 -count 1 -timeout 30m -v -run '^TestSearchBenchmark70k$' ./internal/store
```

Each run creates and removes a fresh temporary database. Run benchmarks one at a time. This Linux run used `TMPDIR=/var/tmp` to keep the database on disk; record the storage and any resource limits on each machine.

Copy `current_p50_ms` for each query and convert `index_build_ms` to seconds. Require `PASS`, `fixture_reused=false`, `initial_corpus_messages=70000`, and a nonzero index build time. The final count is 70,250 because the benchmark appends 250 messages after measuring search.

[Search architecture and historical measurements](performance-history.md).
