package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/auth"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

var usage = `gmail-to-duckdb — sync Gmail into a local DuckDB file

Commands:
  init [--credentials PATH]   Write a stable config
  doctor [--json] [--port N]  Check setup
  serve [--sync-every 5m]     UI, status, and optional scheduled sync
  ui [--port 8080]            Same process as serve (no startup sync)
  sync [--full] [--bodies]    Incremental metadata sync
  status [--json]             Sync and coverage status
  search QUERY [--json]       Search messages
  get MESSAGE_ID [--body]     Fetch one message
  schema [--json]             Database schema
  sql [--write] [--json]      Run SQL (read-only; --write is database-only)

Flags (most commands):
  --db PATH            DuckDB file (default mail.duckdb)
  --credentials PATH   OAuth client JSON (default credentials.json)
  --oauth-port N       OAuth callback port (default ` + itoa(auth.DefaultOAuthPort) + `)
  --json               Machine-readable envelope
  --duckdb-ui          Allow DuckDB UI on loopback (serve/ui)
`

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

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
		return cmdSQL(args[2:], stdin, stdout)
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
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", args[1])
	}
}
