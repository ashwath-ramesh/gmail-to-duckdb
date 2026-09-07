# Gmail to DuckDB

Inspired by: https://github.com/marcboeker/gmail-to-sqlite

A local Go CLI that syncs your Gmail into a DuckDB file on your machine.

Mail never leaves your computer. Connect Gmail with OAuth. Query it with SQL.

DuckDB makes analytics and search fast after sync. Browse mail in the local UI, open the DuckDB UI, or run SQL from the CLI.

## Stack

- Go CLI (one binary)
- One `mail.duckdb` file
- DuckDB FTS for subject and body
- Gmail History API and batch get
- Metadata first. Bodies when you ask.

## Commands

- `sync` — incremental (first run does a full metadata load)
- `sync --full` — list the mailbox and mark missing rows deleted
- `sync --bodies` — fetch bodies for rows that lack them
- `sql` — run a query
- `ui` — local mail browser and preloaded SQL stats

## Install on a Mac

You need [Go](https://go.dev/dl/) and Xcode command-line tools (`xcode-select --install`). Each user uses their own Google Cloud OAuth client. Do not copy someone else’s `credentials.json` or `mail.token.json`.

```bash
git clone https://github.com/ashwath-ramesh/gmail-to-duckdb.git
cd gmail-to-duckdb
go build -o gmail-to-duckdb ./cmd/gmail-to-duckdb
```

Then follow Setup below. Run `sync` and `ui` on the same Mac so the browser can reach `127.0.0.1`.

## Setup

1. Create a Google Cloud project.
2. Enable the Gmail API.
3. Create OAuth 2.0 credentials for a Desktop app.
4. Save the file as `credentials.json` in the working directory.
5. Build:

```bash
go build -o gmail-to-duckdb ./cmd/gmail-to-duckdb
```

6. Sync:

```bash
./gmail-to-duckdb sync
```

The first run opens a localhost OAuth page. The callback is always `http://127.0.0.1:41807/`. The token is stored as `mail.token.json` with mode `0600`. The token is not stored in DuckDB.

If the CLI runs on a remote host and you sign in on a laptop, open the tunnel **before** you click Allow:

```bash
ssh -L 41807:127.0.0.1:41807 USER@REMOTE
```

Or copy the `http://127.0.0.1:41807/?code=...` URL from the laptop and paste it into the remote `sync` prompt.

## Usage

```bash
./gmail-to-duckdb sync
./gmail-to-duckdb sync --full
./gmail-to-duckdb sync --bodies
./gmail-to-duckdb sql 'SELECT from_email, count(*) FROM messages GROUP BY 1 ORDER BY 2 DESC LIMIT 10'
./gmail-to-duckdb ui
```

Flags:

- `--db mail.duckdb`
- `--credentials credentials.json`
- `--port 8080` (ui only)

`ui` binds `127.0.0.1` only. The printed URL includes a session token. Mail lists metadata. Open a message and use Fetch body to pull one body from Gmail. The Stats page runs the bundled SQL files. Use Open DuckDB UI for ad-hoc SQL. Do not run `ui` on a shared host if other users can reach your loopback port.

DuckDB allows one writer. Close `ui` before you run `sync` or `sql` on the same file.

## Schema

`messages` stores typed columns: ids, timestamps, from, to, cc, subject, snippet, nullable body, labels, read/outgoing/deleted flags.

`labels` maps Gmail label ids to names.

`sync_state` stores `history_id` and resume tokens.

## Privacy

- Mail is written only to the local DuckDB file.
- The HTTP UI listens on loopback.
- Keep `credentials.json`, `*.token.json`, and `*.duckdb` out of git.
