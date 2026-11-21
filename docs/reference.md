# Reference

This page lists every command, flag, and data rule. For install and setup, see [README.md](../README.md). For scripts and AI agents, see [agents.md](agents.md). For file and network protections, see [security.md](security.md).

## Stack

- One Go binary
- One DuckDB mailbox
- A disposable local SQLite trigram index (`*.duckdb.search`) for candidate selection
- DuckDB remains the authority for verify and literal search
- Gmail History API and batch get
- Metadata first. Bodies when you ask.

## Performance

On a 4-vCPU Ubuntu VM with a 2 GiB benchmark memory cap, common-term search across 70,000 synthetic messages took **136 ms median**. Initial indexing took **25 seconds**. See the [results and repeatable template](performance.md). Architecture notes are in [performance-history.md](performance-history.md).

## Commands

| Command | What it does |
|---|---|
| `init --credentials PATH [--db PATH]` | Copies your Google client JSON into the config folder (private) and writes `config.json`. It saves an absolute database path. |
| `serve [--sync-every 5m] [--port N]` | Signs in on first use, runs a metadata sync at start and on each interval, and opens the mail page. |
| `ui [--port N]` | If serve runs, opens a new one-time login link to its mail page. If serve does not run, starts serve without the start-up sync. |
| `doctor [--json] [--port N]` | Checks credentials, token, database, search index, ports, and serve. |
| `sync [--full] [--bodies]` | Runs one sync. `--full` lists the whole mailbox and marks missing messages as deleted. `--bodies` downloads bodies for messages that do not have one. |
| `status [--json]` | Shows sync and coverage status. |
| `search QUERY [--json]` | Searches messages. All words count. Returns at most 50 messages. |
| `get MESSAGE_ID [--body] [--json]` | Shows one message. `--body` adds the stored body. It does not download a body. |
| `schema [--json]` | Shows the database schema. |
| `sql [--write] [--read-only] [--format json\|table] [--json] [QUERY]` | Runs one SQL statement. Reads the query from standard input if you give no `QUERY`. Read-only unless you pass `--write`. `--read-only` is an explicit no-op for agents. |

Flags for most commands:

- `--db PATH` (default from `config.json`, else `mail.duckdb` in the working directory)
- `--credentials PATH` (default from `config.json`, else `credentials.json` in the working directory)
- `--oauth-port N` (default `41807`)
- `--port N` (`serve`, `ui`, `doctor`; default `8080`)
- `--json`

Flags override the config file. If no config file exists, the working directory defaults stay `mail.duckdb` and `credentials.json`. If `config.json` exists but cannot be read or is not valid JSON, every command except `init` stops with an error. Run `init` again to write it.

`init` writes `~/.config/gmail-to-duckdb/config.json` and copies the client JSON next to that file. The default database is `~/.local/share/gmail-to-duckdb/mail.duckdb`. `XDG_CONFIG_HOME` and `XDG_DATA_HOME` change these folders. On Windows, `~` is `%USERPROFILE%`.

## Files next to the database

The token and serve file names come from the database path without its extension: `mail.duckdb` uses `mail.token.json`.

| File | What it holds |
|---|---|
| `mail.duckdb` (and `.wal`) | All synced mail. |
| `mail.duckdb.search/` | The disposable search index. |
| `mail.duckdb.tmp/` | DuckDB temporary files. |
| `mail.token.json` | Your Google sign-in (refresh token) and the granted scope. |
| `mail.serve.json` | The URL, API token, process id, and database path of a running serve. |
| `mail.open.html` | The private page that opens a one-time login link. |

## Serve and the CLI

`serve` owns the database. It runs an incremental metadata sync at startup and on `--sync-every`. The mail page shows last success, phase, processed count, last error, and body coverage. Use **Sync now** to run a sync without leaving the mail page.

`last_sync` is the last completed sync. It is not a "work in progress" stamp.

`sync` stays available for one-shot jobs. If `serve` is running, `sync` and the query commands call its HTTP API. If a serve file exists but serve does not answer, the CLI tries to open DuckDB. If the open works, that serve has stopped: the CLI removes the stale serve file and continues. If DuckDB is still locked, the CLI stops with an error that names the serve process. If no serve file exists, the commands open DuckDB.

`serve` stops cleanly on Ctrl+C, on `kill` (SIGTERM), and when its terminal closes (SIGHUP). It waits for a running sync, removes its serve file, and closes DuckDB.

The mail page needs a one-time login link. `serve` prints the link and opens it. `ui` asks a running serve for a new link. Each link works once, for 2 minutes. The browser keeps the session until serve stops.

One Gmail account binds to one database. A different account is an error. Use another `--db`.

`doctor` checks credentials, the local token file, the database, the search index, ports, and a running `serve` process. Use `doctor --json` for agents. Pass `--port` to match a custom mail page port. If `serve` is running, `doctor` uses that process and does not open DuckDB itself. Before the first sign-in, the token and database checks are `pending`, and `doctor` still passes. The token check only validates local presence, shape, and scope. It cannot prove that Google will refresh the token.

If Google rejects the refresh token, stop `serve`. Remove only the `*.token.json` file for the chosen database. Run `serve` or `sync` and sign in with the same account. Do not remove the database or `credentials.json`.

## Message bodies

The mail list shows metadata. Open a message and use **Fetch body** to download one body from Gmail.

- A pending body is not fetched yet.
- A completed fetch with no text is fetched-empty. The mail page shows **No text body** and hides **Fetch body**.
- `with_body` counts messages that have a nonempty body.
- `body_fetched` marks a finished full fetch, even when the message has no text.
- HTML bodies show in a sandboxed frame. Remote images do not load.
- New body downloads are converted from their declared character set to UTF-8. Invalid bytes become U+FFFD.

`sync --bodies` walks pending ids in ordered pages. Each pending id is attempted once per sync. Missing or unparseable replies stay pending for the next sync. The run does not loop those ids again. Transport errors still stop after the existing Gmail retry limit. `--sync-every` syncs metadata only. It never downloads bodies.

A metadata reply that does not parse is logged and skipped, so one bad message cannot stop every sync.

Spam and Trash are included. Permanent deletions stay in the local file with `is_deleted`. Ordinary search and reports exclude those rows.

## Search

Search uses case-insensitive literal substrings. All terms are AND. Quoted phrases are literal contiguous text. Quoted filter values work: `subject:"payment received"`. `%` and `_` are literal. Membership does not use stemming. Results are newest first (`internal_date DESC`, then `id DESC`).

Supported operators:

- `from:bob`
- `to:jane`
- `subject:invoice`
- `unread` or `is:unread`
- `after:2024-01-01`
- `before:2024-06-01`

`after` is UTC inclusive. `before` is UTC exclusive.

Quote a complete token to search literal colon text.

These inputs are validation errors: unsupported operators; empty or repeated singleton filters; bad dates or ranges; unmatched quotes; more than 200 runes.

Use the Unread chip for the same unread filter.

A disposable local SQLite trigram index (`*.duckdb.search`) selects candidates. DuckDB verifies every hit. Index contents and file rules are in [security.md](security.md#local-search-index).

`fts` is true only when the local search index is fully current. `search_index_state` reports `ready`, `pending`, `building`, `repair`, or `unavailable`. Literal search still works while the index is not ready.

`/api/status` serves a cached envelope (`status_as_of`). A background refresher updates it. Search list uses that cached base.

## Reports

The Stats page runs the bundled SQL files. Existing size ranking stays. Three added reports:

1. Top incoming sender domains by message count and estimated bytes.
2. Top outgoing recipient domains from To/Cc. One domain counts once per message. Bytes are the estimated message size, counted once per domain per message. This is not a storage-savings figure.
3. Monthly incoming counts for the top 25 sender addresses over the current UTC calendar month and the preceding 11 months.

Reports include Spam and Trash. They exclude permanently deleted rows.

## Schema

`schema_version` is `4`. Open migrates supported older versions automatically. A v0.2 binary rejects schema 4.

`messages` stores typed columns: ids, timestamps, from, to, cc, subject, snippet, nullable body, labels, read/outgoing/deleted flags, `has_body`, `body_fetched`, `search_text`, and `search_revision` for one-box search.

- `has_body` is true only when the stored body is nonempty.
- `body_fetched` is true after a successful full fetch or on-demand body write, even when the body is empty.
- The SENT label determines outgoing.
- Upgrading to v3 requeues legacy fetched-empty bodies once. Only an explicit body request fetches them.
- Upgrading to v4 adds `search_revision` and search freshness metadata. It does not rewrite every `search_text` row. A legacy mailbox with existing rows is marked repair. A new empty v4 mailbox is not. Background repair fills cached text. The process does not download a DuckDB FTS extension.
- Roll back from v4 by restoring a pre-upgrade DuckDB backup.
- After an upgrade, restart any old running `serve` process. `/api/health` and the rest of that process keep the old binary until you restart.

`headers` is a nullable ordered JSON array. Ordinary message result lists omit it. `sql` returns the header values. `schema` describes the column type and does not return header values. The array keeps every Gmail top-level header name, value, order, and duplicate. `NULL` means headers are not yet collected. An old message may already be fetched. `[]` means a fetch found no headers.

The v3 migration is local. It does not call the network. Run `sync --full` to backfill headers. Later metadata syncs refresh headers with ordinary metadata.

`labels` maps Gmail label ids to names.

`sync_state` stores `history_id`, resume tokens, `schema_version`, and last sync times. A legacy partial cursor without a reliable anchor restarts safely.
