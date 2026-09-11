package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/search"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/searchbench"
)

const benchMsgs = 70000

func TestSearchBenchmark70k(t *testing.T) {
	if os.Getenv("GMAIL_BENCH70K") == "" {
		t.Skip("set GMAIL_BENCH70K=1 to run the opt-in 70k bench")
	}
	ctx := context.Background()
	path, provided, err := searchbench.MailboxPath()
	if err != nil {
		t.Fatal(err)
	}
	if !provided {
		path = filepath.Join(dbParent(t), "bench.duckdb")
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	var seedDur time.Duration
	nStart, _, err := searchbench.NextIDRange(ctx, db.SQL(), 0)
	if err != nil {
		t.Fatal(err)
	}
	reused := nStart > 0
	if nStart == 0 {
		t0 := time.Now()
		if err := searchbench.SeedRange(ctx, db.SQL(), 0, benchMsgs, 1); err != nil {
			t.Fatal(err)
		}
		if err := db.SetState(ctx, stateCorpusRevision, "1"); err != nil {
			t.Fatal(err)
		}
		seedDur = time.Since(t0)
	}

	var initialCount int64
	if err := db.SQL().QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&initialCount); err != nil {
		t.Fatal(err)
	}
	corpusBytes, err := searchbench.CorpusBytes(ctx, db.SQL())
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("GMAIL_BENCH_REBUILD") != "" {
		if err := searchbench.RemoveSearchCache(path); err != nil {
			t.Fatal(err)
		}
	}
	openStart := time.Now()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reopenDur := time.Since(openStart)

	var buildDur time.Duration
	ready, err := db.HasFTS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ready || os.Getenv("GMAIL_BENCH_REBUILD") != "" {
		t0 := time.Now()
		if err := db.catchupIndex(ctx, true); err != nil {
			t.Fatal(err)
		}
		buildDur = time.Since(t0)
	}

	cases := []string{"sharedword", "token123", "absentxyz", "sharedword token2", `"invoice leftover"`, "zz"}
	const warm = 10
	for _, qs := range cases {
		parsed, err := search.Parse(qs)
		if err != nil {
			t.Fatal(err)
		}
		f := ListFilter{Query: qs, Limit: 50}
		var cur []Message
		if qs == "zz" {
			cur, err = db.literalSearch(ctx, f, parsed)
		} else {
			cur, err = db.indexedSearch(ctx, f, parsed)
		}
		if err != nil {
			t.Fatalf("%q current: %v", qs, err)
		}
		base, err := db.queryList(ctx, nil, f, parsed, parsed.Terms, false, 50, 0, false, time.Time{}, "")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(ids(cur), ",") != strings.Join(ids(base), ",") {
			t.Fatalf("%q id mismatch current=%d baseline=%d", qs, len(cur), len(base))
		}
		var curD, baseD []time.Duration
		for i := 0; i < warm; i++ {
			t0 := time.Now()
			if qs == "zz" {
				_, err = db.literalSearch(ctx, f, parsed)
			} else {
				_, err = db.indexedSearch(ctx, f, parsed)
			}
			if err != nil {
				t.Fatal(err)
			}
			curD = append(curD, time.Since(t0))
			t1 := time.Now()
			_, err = db.queryList(ctx, nil, f, parsed, parsed.Terms, false, 50, 0, false, time.Time{}, "")
			if err != nil {
				t.Fatal(err)
			}
			baseD = append(baseD, time.Since(t1))
		}
		t.Logf("q=%q hits=%d current_p50_ms=%.3f current_p95_ms=%.3f baseline_uncached_contains_p50_ms=%.3f baseline_uncached_contains_p95_ms=%.3f baseline=uncached_contains_filter newest_first no_fts_rank not_original_app",
			qs, len(cur), searchbench.MS(searchbench.Median(curD)), searchbench.MS(searchbench.P95(curD)),
			searchbench.MS(searchbench.Median(baseD)), searchbench.MS(searchbench.P95(baseD)))
	}

	start, end, err := searchbench.NextIDRange(ctx, db.SQL(), 250)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := searchbench.CorpusRev(ctx, db.SQL())
	if err != nil {
		t.Fatal(err)
	}
	incStart := time.Now()
	if err := searchbench.SeedRange(ctx, db.SQL(), start, end, int(rev+1)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateCorpusRevision, strconv.FormatInt(rev+1, 10)); err != nil {
		t.Fatal(err)
	}
	if err := db.catchupIndex(ctx, false); err != nil {
		t.Fatal(err)
	}
	incDur := time.Since(incStart)

	var finalCount int64
	if err := db.SQL().QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&finalCount); err != nil {
		t.Fatal(err)
	}
	t.Logf("hardware cpus=%d gomaxprocs=%d goos=%s goarch=%s rss_kb=%d fixture_reused=%t",
		runtime.NumCPU(), runtime.GOMAXPROCS(0), runtime.GOOS, runtime.GOARCH, searchbench.RSSKb(), reused)
	t.Logf("initial_corpus_messages=%d initial_corpus_text_bytes=%d final_row_count=%d incremental_added=%d cache_bytes=%d",
		initialCount, corpusBytes, finalCount, finalCount-initialCount, searchbench.DirSize(searchbench.SearchDir(path)))
	t.Logf("seed_bulk_sql_ms=%.3f duckdb_reopen_ms=%.3f index_build_ms=%.3f incremental_batch_ms=%.3f",
		searchbench.MS(seedDur), searchbench.MS(reopenDur), searchbench.MS(buildDur), searchbench.MS(incDur))
	t.Logf("duckdb_reopen_ms is Open only. The timer starts after Close. It is not process-to-listening")
	t.Logf("fixture_repeat_body=true body_variability=limited mac_targets_unvalidated")
}
