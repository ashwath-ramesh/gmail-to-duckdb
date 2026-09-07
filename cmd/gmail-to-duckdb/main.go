package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"text/tabwriter"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/auth"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/gmail"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/parse"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	mailsync "github.com/ashwath-ramesh/gmail-to-duckdb/internal/sync"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/web"
)

const usage = `gmail-to-duckdb — sync Gmail into a local DuckDB file

Commands:
  sync [--full] [--bodies]   Incremental metadata sync
  sql <query>                Run one SQL statement
  ui [--port 8080]           Local mail + stats browser

Flags (all commands):
  --db PATH            DuckDB file (default mail.duckdb)
  --credentials PATH   OAuth client JSON (default credentials.json)
  --oauth-port N       OAuth callback port (default 41807)
`

func main() {
	if err := run(os.Args, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("%s", usage)
	}
	switch args[1] {
	case "sync":
		return cmdSync(args[2:], stdout, stderr)
	case "sql":
		return cmdSQL(args[2:], stdout)
	case "ui":
		return cmdUI(args[2:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n%s", args[1], usage)
	}
}

func globalFlags(name string, args []string) (*flag.FlagSet, *string, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	db := fs.String("db", "mail.duckdb", "DuckDB file")
	creds := fs.String("credentials", "credentials.json", "OAuth credentials JSON")
	return fs, db, creds
}

func cmdSync(args []string, stdout, stderr io.Writer) error {
	fs, dbPath, creds := globalFlags("sync", args)
	full := fs.Bool("full", false, "list mailbox and mark missing as deleted")
	bodies := fs.Bool("bodies", false, "fetch bodies for messages that lack them")
	oauthPort := fs.Int("oauth-port", auth.DefaultOAuthPort, "OAuth loopback port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	hc, err := auth.HTTPClient(ctx, *creds, auth.TokenPath(*dbPath), *oauthPort)
	if err != nil {
		return err
	}
	api, err := gmail.New(ctx, hc)
	if err != nil {
		return err
	}
	r := &mailsync.Runner{
		DB:  db,
		API: api,
		Log: func(format string, a ...any) { fmt.Fprintf(stderr, format+"\n", a...) },
	}
	return r.Sync(ctx, mailsync.Options{Full: *full, Bodies: *bodies})
}

func cmdSQL(args []string, stdout io.Writer) error {
	fs, dbPath, _ := globalFlags("sql", args)
	if err := fs.Parse(args); err != nil {
		return err
	}
	query := fs.Arg(0)
	if query == "" {
		return fmt.Errorf("sql requires a query")
	}
	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	res, err := db.ExecSQL(context.Background(), query)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, joinTab(res.Columns))
	for _, row := range res.Rows {
		fmt.Fprintln(tw, joinTab(row))
	}
	return tw.Flush()
}

func cmdUI(args []string, stdout, stderr io.Writer) error {
	fs, dbPath, creds := globalFlags("ui", args)
	port := fs.String("port", "8080", "local port")
	oauthPort := fs.Int("oauth-port", auth.DefaultOAuthPort, "OAuth loopback port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.EnsureFTS(ctx); err != nil {
		fmt.Fprintln(stderr, "fts:", err)
	}

	s := &web.Server{DB: db, Token: web.NewToken()}
	if hc, err := auth.HTTPClient(ctx, *creds, auth.TokenPath(*dbPath), *oauthPort); err == nil {
		if api, err := gmail.New(ctx, hc); err == nil {
			s.FetchBody = func(ctx context.Context, id string) (string, error) {
				raw, err := api.Get(ctx, id, "full")
				if err != nil {
					return "", err
				}
				msg, err := parse.Message(raw, "")
				if err != nil {
					return "", err
				}
				return msg.Body, nil
			}
		}
	} else {
		fmt.Fprintln(stderr, "body fetch disabled:", err)
	}

	url := web.Addr(*port)
	if s.Token != "" {
		url += "?t=" + s.Token
	}
	fmt.Fprintln(stdout, "listening on", url)
	_ = exec.Command("xdg-open", url).Start()
	return web.ListenAndServe(ctx, *port, s.Handler())
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
