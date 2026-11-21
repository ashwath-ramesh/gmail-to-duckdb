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
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/termtext"
)

type cmdFlags struct {
	fs        *flag.FlagSet
	cfg       config.Config
	cfgErr    error
	db        *string
	creds     *string
	oauthPort *int
}

func globalFlags(name string) cmdFlags {
	cfg, err := config.Load()
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	return cmdFlags{
		fs:        fs,
		cfg:       cfg,
		cfgErr:    err,
		db:        fs.String("db", cfg.DB, "DuckDB file"),
		creds:     fs.String("credentials", cfg.Credentials, "OAuth credentials JSON"),
		oauthPort: fs.Int("oauth-port", cfg.OAuthPort, "OAuth loopback port"),
	}
}

// parse parses args and reports a config file that could not be read.
func (cf cmdFlags) parse(args []string) error {
	if err := parseFlags(cf.fs, args); err != nil {
		return err
	}
	return cf.cfgErr
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

func writeDiag(w io.Writer, s string) {
	fmt.Fprintln(w, termtext.SingleLine(s))
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

// joinTab escapes each cell for the terminal and joins the cells with tabs.
func joinTab(cols []string) string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = termtext.SingleLine(c)
	}
	return strings.Join(out, "\t")
}
