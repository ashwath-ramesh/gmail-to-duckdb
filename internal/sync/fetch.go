package sync

import (
	"context"
	"errors"
	"fmt"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/gmail"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/parse"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

var ErrNotFound = errors.New("gmail message not found")

func FetchOnDemand(ctx context.Context, db *store.DB, api gmail.API, id string) error {
	p, err := api.Profile(ctx)
	if err != nil {
		return err
	}
	if err := db.BindAccount(ctx, p.Email); err != nil {
		return err
	}
	res, err := api.Get(ctx, id, "full")
	if err != nil {
		return err
	}
	write, cancel := persistContext()
	defer cancel()
	switch res.Status {
	case gmail.FetchNotFound:
		if err := db.ApplyBodyUpdates(write, nil, []string{id}); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return ErrNotFound
	case gmail.FetchOK:
		if err := gmail.MatchMessageID(res.Raw, id); err != nil {
			return err
		}
		msg, err := parse.Message(res.Raw)
		if err != nil {
			return err
		}
		if err := db.ApplyBodyUpdates(write, []store.BodyUpdate{{
			ID:      id,
			Body:    msg.Body,
			Headers: msg.Headers,
		}}, nil); err != nil {
			return err
		}
		return ctx.Err()
	default:
		if res.Err != nil {
			return res.Err
		}
		return fmt.Errorf("get %s: %s", id, res.Status)
	}
}
