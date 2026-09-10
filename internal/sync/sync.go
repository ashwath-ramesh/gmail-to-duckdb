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
	stateFullStart    = "full_start_history_id"
	stateFullPhase    = "full_phase"

	phaseList     = "list"
	phaseListFull = "list_full"
	phaseCatchup  = "catchup"

	bodyPageSize = 50
	persistBound = 10 * time.Second
)

var errBodyIncomplete = errors.New("body fetch incomplete")

type Options struct {
	Full   bool
	Bodies bool
}

type Progress struct {
	Phase     string
	Processed int
}

type Runner struct {
	DB            *store.DB
	API           gmail.API
	Log           func(string, ...any)
	OnProgress    func(Progress)
	processed     int
	phase         string
	fullRestarted bool
}

func persistContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), persistBound)
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

func (r *Runner) finish(err error) error {
	write, cancel := persistContext()
	defer cancel()
	if err != nil {
		if werr := r.DB.SetState(write, store.StateLastSyncError, err.Error()); werr != nil {
			return errors.Join(err, werr)
		}
		return err
	}
	if werr := r.DB.ClearState(write, store.StateLastSyncError); werr != nil {
		return werr
	}
	if werr := r.DB.SetState(write, store.StateLastSyncOK, time.Now().UTC().Format(time.RFC3339)); werr != nil {
		return werr
	}
	r.progress("idle")
	return nil
}

func (r *Runner) Sync(ctx context.Context, opt Options) error {
	return r.finish(r.sync(ctx, opt))
}

func (r *Runner) sync(ctx context.Context, opt Options) error {
	r.processed = 0
	r.fullRestarted = false
	r.progress("profile")
	profile, err := r.API.Profile(ctx)
	if err != nil {
		return fmt.Errorf("profile: %w", err)
	}
	if err := r.DB.BindAccount(ctx, profile.Email); err != nil {
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
	resume, markDeleted, err := r.inspectFullResume(ctx, opt, hasHist, profile)
	if err != nil {
		return err
	}
	if resume {
		if err := r.runFull(ctx, profile, markDeleted); err != nil {
			return err
		}
	} else if opt.Full || !hasHist {
		if err := r.prepareFull(ctx, profile.HistoryID, opt.Full, true); err != nil {
			return err
		}
		if err := r.runFull(ctx, profile, opt.Full); err != nil {
			return err
		}
	} else {
		start, _ := strconv.ParseUint(hist, 10, 64)
		if err := r.incremental(ctx, start); err != nil {
			if !expiredHistory(err) {
				return err
			}
			r.logf("history expired; falling back to full list")
			if err := r.restartFull(ctx, profile.HistoryID, true, true); err != nil {
				return err
			}
			if err := r.runFull(ctx, profile, true); err != nil {
				return err
			}
		}
	}

	var bodyErr error
	if opt.Bodies {
		bodyErr = r.bodies(ctx)
	}
	if err := ctx.Err(); err != nil {
		if bodyErr != nil {
			return errors.Join(err, bodyErr)
		}
		return err
	}
	r.progress("fts")
	if err := r.DB.EnsureFTS(ctx); err != nil {
		if bodyErr != nil {
			return errors.Join(err, bodyErr)
		}
		return err
	}
	return bodyErr
}

func expiredHistory(err error) bool {
	return errors.Is(err, gmail.ErrHistoryGone) || errors.Is(err, gmail.ErrPageTokenExpired)
}

func (r *Runner) inspectFullResume(ctx context.Context, opt Options, hasHist bool, profile gmail.Profile) (bool, bool, error) {
	phase, hasPhase, err := r.DB.GetState(ctx, stateFullPhase)
	if err != nil {
		return false, false, err
	}
	_, hasList, err := r.DB.GetState(ctx, stateListPage)
	if err != nil {
		return false, false, err
	}
	anchor, hasAnchor, err := r.DB.GetState(ctx, stateFullStart)
	if err != nil {
		return false, false, err
	}
	trustworthy := hasAnchor && parseUint(anchor) > 0
	if hasPhase {
		if !knownPhase(phase) {
			mark := opt.Full || hasHist
			if err := r.restartFull(ctx, profile.HistoryID, mark, !trustworthy); err != nil {
				return false, false, err
			}
			return true, mark, nil
		}
		mark := phase == phaseListFull || opt.Full
		if !trustworthy && (phase == phaseList || phase == phaseListFull) {
			if err := r.restartFull(ctx, profile.HistoryID, mark, true); err != nil {
				return false, false, err
			}
			return true, mark, nil
		}
		if opt.Full && phase == phaseList {
			write, cancel := persistContext()
			err := r.DB.SetState(write, stateFullPhase, phaseListFull)
			cancel()
			if err != nil {
				return false, false, err
			}
			mark = true
		}
		return true, mark, nil
	}
	if hasList && !trustworthy {
		mark := opt.Full || hasHist
		if err := r.restartFull(ctx, profile.HistoryID, mark, true); err != nil {
			return false, false, err
		}
		return true, mark, nil
	}
	if hasList && trustworthy {
		return true, opt.Full, nil
	}
	return false, false, nil
}

func (r *Runner) prepareFull(ctx context.Context, anchor uint64, markDeleted, replaceAnchor bool) error {
	write, cancel := persistContext()
	defer cancel()
	if !replaceAnchor {
		cur, ok, err := r.DB.GetState(write, stateFullStart)
		if err != nil {
			return err
		}
		if !ok || parseUint(cur) == 0 {
			replaceAnchor = true
		}
	}
	phase := phaseList
	if markDeleted {
		phase = phaseListFull
	}
	p := store.PageCommit{
		ResetSeen:    true,
		ListPage:     store.StateClear(),
		HistoryPage:  store.StateClear(),
		HistoryStart: store.StateClear(),
		FullPhase:    store.StateValue(phase),
	}
	if replaceAnchor {
		p.FullStartID = store.StateValue(strconv.FormatUint(anchor, 10))
	}
	return r.DB.CommitSyncPage(write, p)
}

func (r *Runner) restartFull(ctx context.Context, anchor uint64, markDeleted, replaceAnchor bool) error {
	already := r.fullRestarted
	r.fullRestarted = true
	if err := r.prepareFull(ctx, anchor, markDeleted, replaceAnchor); err != nil {
		return err
	}
	if already {
		return gmail.ErrPageTokenExpired
	}
	return nil
}

func (r *Runner) runFull(ctx context.Context, profile gmail.Profile, markDeleted bool) error {
	phase, _, err := r.DB.GetState(ctx, stateFullPhase)
	if err != nil {
		return err
	}
	if phase != "" && !knownPhase(phase) {
		return fmt.Errorf("unknown full_phase %q", phase)
	}
	startedCatchup := phase == phaseCatchup
	if phase == "" || phase == phaseList || phase == phaseListFull {
		if err := r.list(ctx, markDeleted || phase == phaseListFull); err != nil {
			return err
		}
		phase = phaseCatchup
	}
	if phase == phaseCatchup {
		err := r.afterFull(ctx)
		if err == nil {
			return nil
		}
		if startedCatchup && expiredHistory(err) && !r.fullRestarted {
			r.logf("catchup history expired; starting a new full list")
			if err := r.restartFull(ctx, profile.HistoryID, true, true); err != nil {
				return err
			}
			return r.runFull(ctx, profile, true)
		}
		return err
	}
	return nil
}

func (r *Runner) afterFull(ctx context.Context) error {
	anchor, ok, err := r.DB.GetState(ctx, stateFullStart)
	if err != nil {
		return err
	}
	start := parseUint(anchor)
	if !ok || start == 0 {
		return fmt.Errorf("full catchup missing history anchor")
	}
	return r.incremental(ctx, start)
}

func (r *Runner) list(ctx context.Context, markDeleted bool) error {
	pageTok, _, err := r.DB.GetState(ctx, stateListPage)
	if err != nil {
		return err
	}
	r.progress("list")
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ids, next, err := r.API.ListMessages(ctx, pageTok)
		if errors.Is(err, gmail.ErrPageTokenExpired) {
			prof, perr := r.API.Profile(ctx)
			if perr != nil {
				return perr
			}
			if err := r.restartFull(ctx, prof.HistoryID, markDeleted, false); err != nil {
				return err
			}
			pageTok = ""
			continue
		}
		if err != nil {
			return err
		}
		msgs, tombs, unresolved := r.fetchParsed(ctx, ids, "metadata")
		if unresolved != nil {
			return unresolved
		}
		write, cancel := persistContext()
		p := store.PageCommit{Messages: msgs, Tombstones: tombs, Seen: ids}
		if next != "" {
			p.ListPage = store.StateValue(next)
			err = r.DB.CommitSyncPage(write, p)
			cancel()
			if err != nil {
				return err
			}
			r.noteWrite(len(msgs))
			pageTok = next
			continue
		}
		p.ListPage = store.StateClear()
		p.MarkMissing = markDeleted
		p.ResetSeen = !markDeleted
		p.FullPhase = store.StateValue(phaseCatchup)
		err = r.DB.CommitSyncPage(write, p)
		cancel()
		if err != nil {
			return err
		}
		r.noteWrite(len(msgs))
		if markDeleted {
			r.logf("marked missing deleted")
		}
		if err := ctx.Err(); err != nil {
			return err
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
		if v := parseUint(s); v > 0 {
			start = v
		}
	}
	catchup := false
	if phase, ok, _ := r.DB.GetState(ctx, stateFullPhase); ok && phase == phaseCatchup {
		catchup = true
	}
	r.progress("history")
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := r.API.History(ctx, start, pageTok)
		if err != nil {
			return err
		}
		need := append([]string{}, page.Added...)
		for _, u := range page.LabelUpdates {
			need = append(need, u.ID)
		}
		need = unique(need)
		msgs, tombs, unresolved := r.fetchParsed(ctx, need, "metadata")
		if unresolved != nil {
			return unresolved
		}
		okIDs := map[string]struct{}{}
		for _, m := range msgs {
			okIDs[m.ID] = struct{}{}
		}
		for _, id := range unique(page.Deleted) {
			if _, ok := okIDs[id]; !ok {
				tombs = append(tombs, id)
			}
		}
		write, cancel := persistContext()
		p := store.PageCommit{Messages: msgs, Tombstones: unique(tombs)}
		if page.NextPageToken != "" {
			p.HistoryPage = store.StateValue(page.NextPageToken)
			p.HistoryStart = store.StateValue(strconv.FormatUint(start, 10))
			err = r.DB.CommitSyncPage(write, p)
			cancel()
			if err != nil {
				return err
			}
			r.noteWrite(len(msgs))
			pageTok = page.NextPageToken
			continue
		}
		if page.HistoryID == 0 {
			cancel()
			return fmt.Errorf("history page missing history id")
		}
		p.HistoryPage = store.StateClear()
		p.HistoryStart = store.StateClear()
		p.HistoryID = store.StateValue(strconv.FormatUint(page.HistoryID, 10))
		if catchup {
			p.FullPhase = store.StateClear()
			p.FullStartID = store.StateClear()
		}
		err = r.DB.CommitSyncPage(write, p)
		cancel()
		if err != nil {
			return err
		}
		r.noteWrite(len(msgs))
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
}

func (r *Runner) bodies(ctx context.Context) error {
	r.progress("bodies")
	after := ""
	var incomplete error
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, incomplete)
		}
		ids, err := r.DB.IDsNeedingFetch(ctx, after, bodyPageSize)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return incomplete
		}
		if err := r.ingestBodies(ctx, ids); err != nil {
			if errors.Is(err, errBodyIncomplete) {
				incomplete = err
			} else {
				return errors.Join(err, incomplete)
			}
		}
		after = ids[len(ids)-1]
	}
}

func (r *Runner) ingestBodies(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	updates, tombs, unresolved := r.fetchBodies(ctx, ids)
	write, cancel := persistContext()
	defer cancel()
	if err := r.DB.ApplyBodyUpdates(write, updates, tombs); err != nil {
		return err
	}
	r.noteWrite(len(updates) + len(tombs))
	r.logf("saved %d bodies", len(updates))
	if unresolved != nil {
		return unresolved
	}
	return nil
}

func (r *Runner) fetchBodies(ctx context.Context, ids []string) ([]store.BodyUpdate, []string, error) {
	_, tombs, unresolved, updates := r.collectFetch(ctx, ids, "full")
	return updates, tombs, unresolved
}

func (r *Runner) fetchParsed(ctx context.Context, ids []string, format string) ([]store.Message, []string, error) {
	msgs, tombs, unresolved, _ := r.collectFetch(ctx, ids, format)
	return msgs, tombs, unresolved
}

func (r *Runner) collectFetch(ctx context.Context, ids []string, format string) ([]store.Message, []string, error, []store.BodyUpdate) {
	ids = unique(ids)
	if len(ids) == 0 {
		return nil, nil, nil, nil
	}
	results, fetchErr := r.API.BatchGet(ctx, ids, format)
	full := format == "full"
	if fetchErr != nil && !full {
		return nil, nil, fetchErr, nil
	}
	want := map[string]struct{}{}
	for _, id := range ids {
		want[id] = struct{}{}
	}
	seen := map[string]gmail.FetchResult{}
	var unexpected error
	for _, res := range results {
		if res.ID == "" {
			if full {
				continue
			}
			unexpected = errors.Join(unexpected, fmt.Errorf("unexpected batch response"))
			continue
		}
		if _, ok := want[res.ID]; !ok {
			if !full {
				unexpected = errors.Join(unexpected, fmt.Errorf("unexpected id %s", res.ID))
			}
			continue
		}
		if _, ok := seen[res.ID]; ok {
			if !full {
				unexpected = errors.Join(unexpected, fmt.Errorf("duplicate response for %s", res.ID))
			}
			continue
		}
		seen[res.ID] = res
	}
	var msgs []store.Message
	var updates []store.BodyUpdate
	var tombs []string
	var unresolved error
	if unexpected != nil && !full {
		unresolved = unexpected
	}
	for _, id := range ids {
		res, ok := seen[id]
		if !ok {
			if full {
				unresolved = errors.Join(unresolved, errBodyIncomplete)
			} else {
				unresolved = errors.Join(unresolved, fmt.Errorf("%s: missing response", id))
			}
			continue
		}
		switch res.Status {
		case gmail.FetchOK:
			if err := gmail.MatchMessageID(res.Raw, id); err != nil {
				unresolved = errors.Join(unresolved, err)
				continue
			}
			msg, err := parse.Message(res.Raw)
			if err != nil {
				r.logf("skip parse: %v", err)
				if full {
					unresolved = errors.Join(unresolved, errBodyIncomplete, err)
				} else {
					unresolved = errors.Join(unresolved, err)
				}
				continue
			}
			if full {
				updates = append(updates, store.BodyUpdate{ID: id, Body: msg.Body, Headers: msg.Headers})
			} else {
				msgs = append(msgs, msg)
			}
		case gmail.FetchNotFound:
			tombs = append(tombs, id)
		default:
			cause := res.Err
			if cause == nil {
				cause = fmt.Errorf("%s: %s", id, res.Status)
			}
			if full {
				unresolved = errors.Join(unresolved, errBodyIncomplete, cause)
			} else {
				unresolved = errors.Join(unresolved, cause)
			}
		}
	}
	if fetchErr != nil {
		return msgs, tombs, errors.Join(unresolved, fetchErr), updates
	}
	return msgs, tombs, unresolved, updates
}

func knownPhase(phase string) bool {
	return phase == phaseList || phase == phaseListFull || phase == phaseCatchup
}

func (r *Runner) noteWrite(n int) {
	if n <= 0 {
		return
	}
	r.processed += n
	r.progress(r.phase)
}

func parseUint(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
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
