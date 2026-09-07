package sync

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/gmail"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/parse"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

const (
	stateHistoryID    = "history_id"
	stateListPage     = "list_page_token"
	stateHistoryPage  = "history_page_token"
	stateHistoryStart = "history_start_id"
	stateProfile      = "profile_email"
)

type Options struct {
	Full   bool
	Bodies bool
}

type Progress struct {
	Phase     string
	Processed int
}

type Runner struct {
	DB         *store.DB
	API        gmail.API
	Log        func(string, ...any)
	OnProgress func(Progress)
	wrote      bool
	processed  int
	phase      string
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

func (r *Runner) progress(phase string) {
	r.phase = phase
	if r.OnProgress != nil {
		r.OnProgress(Progress{Phase: phase, Processed: r.processed})
	}
}

func (r *Runner) finish(ctx context.Context, err error) error {
	write := context.WithoutCancel(ctx)
	if err != nil {
		_ = r.DB.SetState(write, store.StateLastSyncError, err.Error())
		return err
	}
	_ = r.DB.ClearState(write, store.StateLastSyncError)
	_ = r.DB.SetState(write, store.StateLastSyncOK, time.Now().UTC().Format(time.RFC3339))
	r.progress("idle")
	return nil
}

func (r *Runner) Sync(ctx context.Context, opt Options) error {
	return r.finish(ctx, r.sync(ctx, opt))
}

func (r *Runner) sync(ctx context.Context, opt Options) error {
	r.wrote = false
	r.processed = 0
	r.progress("profile")
	profile, err := r.API.Profile(ctx)
	if err != nil {
		return fmt.Errorf("profile: %w", err)
	}
	if err := r.DB.SetState(ctx, stateProfile, profile.Email); err != nil {
		return err
	}
	r.progress("labels")
	labels, err := r.API.Labels(ctx)
	if err != nil {
		return fmt.Errorf("labels: %w", err)
	}
	if err := r.DB.UpsertLabels(ctx, labels); err != nil {
		return err
	}

	hist, hasHist, err := r.DB.GetState(ctx, stateHistoryID)
	if err != nil {
		return err
	}
	startHist := profile.HistoryID
	didFull := opt.Full || !hasHist
	if didFull {
		if err := r.clearHistoryResume(ctx); err != nil {
			return err
		}
		if err := r.full(ctx, opt.Full); err != nil {
			return err
		}
		if err := r.afterFull(ctx, startHist); err != nil {
			return err
		}
	} else {
		start, _ := strconv.ParseUint(hist, 10, 64)
		if err := r.incremental(ctx, start); err != nil {
			if !errors.Is(err, gmail.ErrHistoryGone) {
				return err
			}
			r.logf("history id expired; falling back to full list")
			if err := r.clearHistoryResume(ctx); err != nil {
				return err
			}
			if err := r.DB.ClearState(ctx, stateListPage); err != nil {
				return err
			}
			if err := r.DB.ResetSeen(ctx); err != nil {
				return err
			}
			if err := r.full(ctx, true); err != nil {
				return err
			}
			if err := r.afterFull(ctx, startHist); err != nil {
				return err
			}
		}
	}

	if opt.Bodies {
		if err := r.bodies(ctx, profile.Email); err != nil {
			return err
		}
	}
	if r.wrote {
		r.progress("fts")
		r.logf("rebuilding fts")
		if err := r.DB.RebuildFTS(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) clearHistoryResume(ctx context.Context) error {
	if err := r.DB.ClearState(ctx, stateHistoryPage); err != nil {
		return err
	}
	return r.DB.ClearState(ctx, stateHistoryStart)
}

func (r *Runner) afterFull(ctx context.Context, startHist uint64) error {
	if startHist == 0 {
		return nil
	}
	if err := r.DB.SetState(ctx, stateHistoryID, strconv.FormatUint(startHist, 10)); err != nil {
		return err
	}
	if err := r.incremental(ctx, startHist); err != nil && !errors.Is(err, gmail.ErrHistoryGone) {
		return err
	}
	return nil
}

func (r *Runner) full(ctx context.Context, markDeleted bool) error {
	pageTok, hasTok, err := r.DB.GetState(ctx, stateListPage)
	if err != nil {
		return err
	}
	if !hasTok {
		if err := r.DB.ResetSeen(ctx); err != nil {
			return err
		}
		pageTok = ""
	}
	email, _, _ := r.DB.GetState(ctx, stateProfile)
	r.progress("list")
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ids, next, err := r.API.ListMessages(ctx, pageTok)
		if err != nil {
			return err
		}
		write := context.WithoutCancel(ctx)
		if err := r.DB.AddSeen(write, ids); err != nil {
			return err
		}
		if err := r.ingest(write, ids, "metadata", email); err != nil {
			return err
		}
		if next != "" {
			if err := r.DB.SetState(write, stateListPage, next); err != nil {
				return err
			}
			pageTok = next
			continue
		}
		if err := r.DB.ClearState(write, stateListPage); err != nil {
			return err
		}
		if markDeleted {
			n, err := r.DB.MarkMissingDeleted(write)
			if err != nil {
				return err
			}
			r.logf("marked %d deleted", n)
		} else {
			_ = r.DB.ResetSeen(write)
		}
		return nil
	}
}

func (r *Runner) incremental(ctx context.Context, start uint64) error {
	pageTok, _, err := r.DB.GetState(ctx, stateHistoryPage)
	if err != nil {
		return err
	}
	if s, ok, _ := r.DB.GetState(ctx, stateHistoryStart); ok {
		if v, err := strconv.ParseUint(s, 10, 64); err == nil {
			start = v
		}
	}
	email, _, _ := r.DB.GetState(ctx, stateProfile)
	r.progress("history")
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := r.API.History(ctx, start, pageTok)
		if err != nil {
			return err
		}
		write := context.WithoutCancel(ctx)
		if err := r.DB.MarkDeleted(write, page.Deleted); err != nil {
			return err
		}
		need := append([]string{}, page.Added...)
		for _, u := range page.LabelUpdates {
			need = append(need, u.ID)
		}
		if err := r.ingest(write, unique(need), "metadata", email); err != nil {
			return err
		}
		if page.NextPageToken != "" {
			if err := r.DB.SetState(write, stateHistoryPage, page.NextPageToken); err != nil {
				return err
			}
			if err := r.DB.SetState(write, stateHistoryStart, strconv.FormatUint(start, 10)); err != nil {
				return err
			}
			pageTok = page.NextPageToken
			continue
		}
		if page.HistoryID != 0 {
			if err := r.DB.SetState(write, stateHistoryID, strconv.FormatUint(page.HistoryID, 10)); err != nil {
				return err
			}
		}
		if err := r.DB.ClearState(write, stateHistoryPage); err != nil {
			return err
		}
		if err := r.DB.ClearState(write, stateHistoryStart); err != nil {
			return err
		}
		return nil
	}
}

func (r *Runner) bodies(ctx context.Context, email string) error {
	r.progress("bodies")
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ids, err := r.DB.IDsWithoutBody(ctx, 50)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := r.ingest(ctx, ids, "full", email); err != nil {
			return err
		}
	}
}

func (r *Runner) ingest(ctx context.Context, ids []string, format, email string) error {
	if len(ids) == 0 {
		return nil
	}
	raws, err := r.API.BatchGet(ctx, ids, format)
	if err != nil {
		return err
	}
	msgs := make([]store.Message, 0, len(raws))
	for _, raw := range raws {
		msg, err := parse.Message(raw, email)
		if err != nil {
			r.logf("skip parse: %v", err)
			continue
		}
		msgs = append(msgs, msg)
	}
	if err := r.DB.UpsertMessages(ctx, msgs); err != nil {
		return err
	}
	if len(msgs) > 0 {
		r.wrote = true
		r.processed += len(msgs)
		r.progress(r.phase)
	}
	r.logf("upserted %d %s messages", len(msgs), format)
	return nil
}

func unique(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
