package main

import (
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/auth"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

var usage = `gmail-to-duckdb — copy Gmail into a local DuckDB file and search it

Commands:
  init --credentials PATH     Save your Google sign-in file and a config
  serve [--sync-every 5m]     Sign in, sync, and open the mail page
  ui                          Open the mail page of the running serve
  doctor [--json] [--port N]  Check setup
  sync [--full] [--bodies]    Sync once (--bodies downloads message text)
  status [--json]             Sync and coverage status
  search QUERY [--json]       Search messages (50 results at most)
  get MESSAGE_ID [--body]     Show one message (--body: stored text only)
  schema [--json]             Database schema
  sql [--write] [--json]      Run SQL (read-only; --write is database-only)
  logout                      Revoke Google access and delete the sign-in token

Flags (most commands):
  --db PATH            DuckDB file (default mail.duckdb)
  --credentials PATH   OAuth client JSON (default credentials.json)
  --oauth-port N       OAuth callback port (default ` + strconv.Itoa(auth.DefaultOAuthPort) + `)
  --port N             Mail page port (serve, ui, doctor; default 8080)
  --no-browser         Print links only, do not open a browser (serve, ui)
  --json               Machine-readable envelope
`

func main() {
	privfile.LockDownProcess()
	if err := run(os.Args, os.Stdin, os.Stdout, os.Stderr); err != nil {
		writeDiag(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("missing command")
	}
	switch args[1] {
	case "sync":
		return cmdSync(args[2:], stdout, stderr)
	case "sql":
		return cmdSQL(args[2:], stdin, stdout, stderr)
	case "ui":
		return cmdServe(args[2:], false, stdout, stderr)
	case "serve":
		return cmdServe(args[2:], true, stdout, stderr)
	case "status":
		return cmdStatus(args[2:], stdout)
	case "search":
		return cmdSearch(args[2:], stdout)
	case "get":
		return cmdGet(args[2:], stdout)
	case "schema":
		return cmdSchema(args[2:], stdout)
	case "init":
		return cmdInit(args[2:], stdout)
	case "doctor":
		return cmdDoctor(args[2:], stdout)
	case "logout":
		return cmdLogout(args[2:], stdout)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", args[1])
	}
}
