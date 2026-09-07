package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
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

func parseFlags(fs *flag.FlagSet, args []string) error {
	return fs.Parse(flagsFirst(fs, args))
}

func flagsFirst(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		if strings.Contains(a, "=") || isBoolFlag(fs, a) {
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, pos...)
}

type boolFlag interface {
	IsBoolFlag() bool
}

func isBoolFlag(fs *flag.FlagSet, arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if i := strings.IndexByte(name, '='); i >= 0 {
		name = name[:i]
	}
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	bf, ok := f.Value.(boolFlag)
	return ok && bf.IsBoolFlag()
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
