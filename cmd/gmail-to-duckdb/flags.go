package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/config"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

type cmdFlags struct {
	fs        *flag.FlagSet
	db        *string
	creds     *string
	oauthPort *int
}

func globalFlags(name string) cmdFlags {
	cfg := config.Load()
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	return cmdFlags{
		fs:        fs,
		db:        fs.String("db", cfg.DB, "DuckDB file"),
		creds:     fs.String("credentials", cfg.Credentials, "OAuth credentials JSON"),
		oauthPort: fs.Int("oauth-port", cfg.OAuthPort, "OAuth loopback port"),
	}
}

func writeEnv(w io.Writer, env query.Envelope, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(env)
	}
	_, err := io.WriteString(w, query.FormatHuman(env))
	return err
}

func writeSQLTable(w io.Writer, res *query.SQLPayload) error {
	if res == nil {
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, joinTab(res.Columns))
	tmp := store.SQLResult{Rows: res.Rows}
	for _, row := range tmp.StringRows() {
		fmt.Fprintln(tw, joinTab(row))
	}
	return tw.Flush()
}

func joinTab(cols []string) string {
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += "\t"
		}
		out += c
	}
	return out
}
