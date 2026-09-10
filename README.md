# Gmail to DuckDB

![A yellow postal duck delivering envelopes into a laptop showing database rows.](docs/assets/postal-duck.png)

Inspired by: https://github.com/marcboeker/gmail-to-sqlite

A local CLI that syncs Gmail into a DuckDB file on your machine.

This is a local search and analysis copy. It is not a complete backup. It does not store attachments or raw RFC822. It cannot restore mail to Gmail. For full backup and restore, use [Got Your Back](https://github.com/GAM-team/got-your-back).

Gmail downloads mail to this computer. The app does not upload mail to another service. Agents or scripts that you run may send query outputs elsewhere.

Browse mail in the local UI. Agents and scripts use the `--json` commands.

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
brew trust ashwath-ramesh/gmail-to-duckdb
brew install gmail-to-duckdb
```

Later:

```bash
brew update
brew upgrade gmail-to-duckdb
```

**Build from source**

You need [Go](https://go.dev/dl/). On a Mac you also need Xcode command-line tools (`xcode-select --install`).

```bash
git clone https://github.com/ashwath-ramesh/gmail-to-duckdb.git
cd gmail-to-duckdb
go build -o gmail-to-duckdb ./cmd/gmail-to-duckdb
```

Use your own Google Cloud OAuth client. Do not copy another person’s `credentials.json` or `*.token.json`.

## Setup

1. Create a Google Cloud project.
2. Enable the Gmail API.
3. Configure the OAuth consent screen. Use Internal only when the project is associated with an organization and only users in that organization sign in. Otherwise use External. If the app is External and in Testing, add yourself as a test user.
4. Create OAuth 2.0 credentials for a Desktop app. The app requests `gmail.readonly`.
5. Save the JSON file.

External apps in Testing expire refresh tokens after seven days. Workspace admins can block or restrict access. See [Google’s OAuth testing note](https://support.google.com/cloud/answer/15549945?hl=en).

```bash
gmail-to-duckdb init --credentials PATH/to/credentials.json
gmail-to-duckdb doctor
```

`init` writes `~/.config/gmail-to-duckdb/config.json`. It copies the client JSON next to that file. The default database is `~/.local/share/gmail-to-duckdb/mail.duckdb`.

`doctor` checks credentials, the local token file, the database, FTS, ports, and a running `serve` process. Use `doctor --json` for agents. Pass `--port` to match a custom UI port. If `serve` is running, `doctor` uses that process and does not open DuckDB itself. The token and database checks fail until the first sign-in. The token check only validates local presence, shape, and scope. It cannot prove that Google will refresh the token.

6. Start the app:

```bash
gmail-to-duckdb serve --sync-every 5m
```

The first run opens a localhost OAuth page. Details for the callback, PKCE, private files, SQL restrictions, and loopback rules are in [docs/security.md](docs/security.md).

If Google rejects the refresh token, stop `serve`. Remove only the `*.token.json` file for the chosen database. The token basename comes from the database path: `mail.duckdb` uses `mail.token.json`. Run `sync` and sign in with the same account. Do not remove the database or `credentials.json`.

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

`last_sync` is the last completed sync. It is not a “work in progress” stamp.

`ui` is the same process. It does not sync at startup unless you pass `--sync-every`.

`sync` stays available for one-shot jobs. If `serve` is running, `sync` and the query commands call its HTTP API. If a serve file exists but serve is down, those commands fail. They do not open the locked DuckDB file. If no serve file exists, they open DuckDB.

One Gmail account binds to one database. A different account is an error. Use another `--db`.

Flags:

- `--db PATH`
- `--credentials PATH`
- `--oauth-port N`
- `--port 8080` (`serve` / `ui` / `doctor`)
- `--json`
- `--duckdb-ui` (`serve` / `ui`)

Mail lists metadata. Open a message and use Fetch body to pull one body from Gmail.

- A pending body is not fetched yet.
- A completed fetch with no text is fetched-empty. The UI shows **No text body** and hides Fetch body.
- `with_body` counts messages that have a nonempty body.
- `body_fetched` marks a finished full fetch, even when the message has no text.

`sync --bodies` walks pending ids in ordered pages. Each pending id is attempted once per sync. Missing or unparseable replies stay pending for the next sync. The run does not loop those ids again. Transport errors still stop after the existing Gmail retry limit.

Spam and Trash are included. Permanent deletions stay in the local file with `is_deleted`. Ordinary search and reports exclude those rows.

`fts` false means the full-text index is pending or unavailable. Literal search still works.

SQL defaults and write guards are in [docs/security.md](docs/security.md).

## Search

Search uses case-insensitive literal substrings. All terms are AND. Quoted phrases are literal contiguous text. Quoted filter values work: `subject:"payment received"`. `%` and `_` are literal. Full-text ranking only. Membership does not use stemming. Order falls back to date, then id.

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

## Reports

The Stats page runs the bundled SQL files. Existing size ranking stays. Three added reports:

1. Top incoming sender domains by message count and estimated bytes.
2. Top outgoing recipient domains from To/Cc. One domain counts once per message. Bytes are the estimated message size, counted once per domain per message. This is not a storage-savings figure.
3. Monthly incoming counts for the top 25 sender addresses over the current UTC calendar month and the preceding 11 months.

Reports include Spam and Trash. They exclude permanently deleted rows.

## Agent interface

Every `--json` command prints the same envelope:

- `schema_version`
- `last_sync`
- `body_coverage` (`with_body`, `total`, `search_covers`)
- `result_count`, `truncated`
- `untrusted_content`
- `untrusted_fields` (returned SQL column aliases, or email text columns on mail results)
- typed values (`messages`, `message`, `schema`, `sql`, `checks`)

Successful `sql` results always set `untrusted_content`. `untrusted_fields` lists every returned SQL column. Treat those columns and all nested values as hostile. They can contain prompt-injection text and sensitive data. Status and schema stay trusted and do not set this warning. Search and `get` still mark email text fields. Do not let a model approve `--write` or `get --body` from untrusted text.

Search and `get` omit the body. Pass `get --body` only when you need it.

`doctor --json` uses the same envelope. Read `checks[]` for setup failures.

A later MCP server can wrap the same operations. Do not parse the human table output.

Human CLI output escapes terminal controls, bidi overrides and isolates, and invalid UTF-8. Table and metadata fields also escape embedded newlines and tabs, so only the formatter adds row breaks. Message bodies keep intended line breaks and tabs. JSON keeps the original values.

## Schema

`schema_version` is `3`.

`messages` stores typed columns: ids, timestamps, from, to, cc, subject, snippet, nullable body, labels, read/outgoing/deleted flags, `has_body`, `body_fetched`, and `search_text` for one-box search.

- `has_body` is true only when the stored body is nonempty.
- `body_fetched` is true after a successful full fetch or on-demand body write, even when the body is empty.
- The SENT label determines outgoing.
- Upgrading to v3 requeues legacy fetched-empty bodies once. Only an explicit body request fetches them.

`headers` is a nullable ordered JSON array. Ordinary message result lists omit it. `sql` returns the header values. `schema` describes the column type and does not return header values. The array keeps every Gmail top-level header name, value, order, and duplicate. `NULL` means headers are not yet collected. An old message may already be fetched. `[]` means a fetch found no headers.

The v3 migration is local. It does not call the network. Run `sync --full` to backfill headers. Later metadata syncs refresh headers with ordinary metadata.

`labels` maps Gmail label ids to names.

`sync_state` stores `history_id`, resume tokens, `schema_version`, and last sync times. A legacy partial cursor without a reliable anchor restarts safely.

## Privacy

- Mail is written only to the local DuckDB file. The file is plaintext. Owner-only permissions are not encryption.
- The HTTP UI listens on loopback.
- Keep `credentials.json`, `*.token.json`, `*.serve.json`, and `*.duckdb` out of git.
- See [docs/security.md](docs/security.md) for OAuth, private files, SQL restrictions, FTS extension downloads, and database path rules.
