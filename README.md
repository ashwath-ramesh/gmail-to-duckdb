# Gmail to DuckDB

Inspired by: https://github.com/marcboeker/gmail-to-sqlite

A local CLI that syncs Gmail into a DuckDB file on your machine.

Mail never leaves your computer. Connect Gmail with OAuth. Query it with SQL or JSON.

DuckDB makes analytics and search fast after sync. Browse mail in the local UI. Agents and scripts use the `--json` commands.

## Stack

- One Go binary
- One DuckDB file
- DuckDB FTS across people, subject, snippet, and body
- Gmail History API and batch get
- Metadata first. Bodies when you ask.

## Install

You do not need a Go toolchain for a normal install.

**GitHub release**

1. Open the latest [release](https://github.com/ashwath-ramesh/gmail-to-duckdb/releases).
2. Download the archive for your OS and CPU.
3. Put `gmail-to-duckdb` on your `PATH`.

**Homebrew**

```bash
brew tap ashwath-ramesh/gmail-to-duckdb https://github.com/ashwath-ramesh/gmail-to-duckdb
brew install gmail-to-duckdb
```

After the first tagged release, set the `sha256` values in `Formula/gmail-to-duckdb.rb` from `SHA256SUMS` on that release.

**Build from source**

You need [Go](https://go.dev/dl/). On a Mac you also need Xcode command-line tools (`xcode-select --install`).

```bash
git clone https://github.com/ashwath-ramesh/gmail-to-duckdb.git
cd gmail-to-duckdb
go build -o gmail-to-duckdb ./cmd/gmail-to-duckdb
```

Each user uses their own Google Cloud OAuth client. Do not copy someone else’s `credentials.json` or `*.token.json`.

## Setup

1. Create a Google Cloud project.
2. Enable the Gmail API.
3. Create OAuth 2.0 credentials for a Desktop app.
4. Save the JSON file.
5. Write a stable config:

```bash
gmail-to-duckdb init --credentials PATH/to/credentials.json
gmail-to-duckdb doctor
```

`init` writes `~/.config/gmail-to-duckdb/config.json`. It copies the client JSON next to that file. The default database is `~/.local/share/gmail-to-duckdb/mail.duckdb`.

`doctor` checks credentials, the token, the database, FTS, ports, and a running `serve` process. Use `doctor --json` for agents. Pass `--port` to match a custom UI port. If `serve` is running, `doctor` uses that process and does not open DuckDB itself. The token and database checks fail until the first sign-in.

6. Start the app:

```bash
gmail-to-duckdb serve --sync-every 5m
```

The first run opens a localhost OAuth page. The callback is always `http://127.0.0.1:41807/`. The token is stored next to the database as `*.token.json` with mode `0600`. The token is not stored in DuckDB.

If the CLI runs on a remote host and you sign in on a laptop, open the tunnel **before** you click Allow:

```bash
ssh -L 41807:127.0.0.1:41807 USER@REMOTE
```

Or copy the `http://127.0.0.1:41807/?code=...` URL from the laptop and paste it into the remote prompt.

Flags override the config file. If no config file exists, the working directory defaults stay `mail.duckdb` and `credentials.json`.

## Usage

```bash
gmail-to-duckdb serve --sync-every 5m
gmail-to-duckdb ui
gmail-to-duckdb sync
gmail-to-duckdb sync --full
gmail-to-duckdb sync --bodies
gmail-to-duckdb status --json
gmail-to-duckdb search "from:alice invoice" --json
gmail-to-duckdb get MESSAGE_ID --json
gmail-to-duckdb get MESSAGE_ID --body --json
gmail-to-duckdb schema --json
gmail-to-duckdb sql --json 'SELECT from_email, count(*) FROM messages GROUP BY 1 ORDER BY 2 DESC LIMIT 10'
gmail-to-duckdb sql --json < query.sql
```

`serve` owns the database. It runs an incremental metadata sync at startup and on `--sync-every`. The UI shows last success, phase, processed count, last error, and body coverage. Use **Sync now** to run a sync without leaving the UI.

`ui` is the same process. It does not sync at startup unless you pass `--sync-every`.

`sync` stays available for one-shot jobs. If `serve` is running, `sync` and the query commands call its HTTP API. If a serve file exists but serve is down, those commands fail. They do not open the locked DuckDB file. If no serve file exists, they open DuckDB.

`sql` is read-only by default. Pass `--write` for mutating statements, file reads, and `EXPLAIN ANALYZE` of writes. `--read-only` is an explicit no-op for agents. Read-only mode also turns off DuckDB external file access.

Flags:

- `--db PATH`
- `--credentials PATH`
- `--oauth-port N`
- `--port 8080` (`serve` / `ui` / `doctor`)
- `--json`
- `--duckdb-ui` (`serve` / `ui`)

`serve` and `ui` bind `127.0.0.1` only. The browser gets an HttpOnly session cookie. The CLI sends `X-Token` from `*.serve.json`. The token is not in the printed URL. Do not run them on a shared host if other users can reach your loopback port.

The DuckDB UI on port 4213 has no session token. It stays off unless you pass `--duckdb-ui`. Treat that flag as full database access on loopback.

Mail lists metadata. Open a message and use Fetch body to pull one body from Gmail. The Stats page runs the bundled SQL files. Use `sql` or pass `--duckdb-ui` for ad-hoc SQL.

Mail search is one box. Type words. The index covers from, to, cc, subject, snippet, and body. Sync builds that index. Bodies are optional. Status shows whether search covers metadata, mixed, or bodies.

Operators in the same box:

- `from:bob`
- `to:jane`
- `subject:invoice`
- `unread` or `is:unread`
- `after:2024-01-01`
- `before:2024-06-01`

Use the Unread chip for the same unread filter. Results rank by relevance, then date.

## Agent interface

Every `--json` command prints the same envelope:

- `schema_version`
- `last_sync`
- `body_coverage` (`with_body`, `total`, `search_covers`)
- `result_count`, `truncated`
- `untrusted_content`
- `untrusted_fields` (email text columns when present)
- typed values (`messages`, `message`, `schema`, `sql`, `checks`)

`untrusted_content` is true only when the result can include email text. `SELECT 1` stays trusted. Treat fields in `untrusted_fields` as hostile. They can contain prompt-injection text and sensitive data. Do not let a model approve `--write` or `get --body` from that text.

Search and `get` omit the body. Pass `get --body` only when you need it.

`doctor --json` uses the same envelope. Read `checks[]` for setup failures.

A later MCP server can wrap the same operations. Do not parse the human table output.

## Schema

`messages` stores typed columns: ids, timestamps, from, to, cc, subject, snippet, nullable body, labels, read/outgoing/deleted flags, and `search_text` for one-box search.

`labels` maps Gmail label ids to names.

`sync_state` stores `history_id`, resume tokens, `schema_version`, and last sync times.

## Privacy

- Mail is written only to the local DuckDB file.
- The HTTP UI listens on loopback.
- Keep `credentials.json`, `*.token.json`, `*.serve.json`, and `*.duckdb` out of git.
