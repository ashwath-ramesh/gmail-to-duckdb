# Security

This page records the exact local protections. See [README.md](../README.md) for install, setup, search, reports, and the agent JSON contract.

The database is local plaintext. Owner-only file permissions are not encryption.

## OAuth callback and PKCE

The first run opens a localhost OAuth page. The callback is `http://127.0.0.1:41807/` unless you set `--oauth-port`. Each sign-in creates a new random `state` and a PKCE S256 challenge. The callback must send exactly one matching `state`. A missing, reused, wrong, or duplicate `state` is rejected. A callback may send one `code` or one `error`, not both. A malformed query is rejected. Sign-in stays open after an invalid callback. The token exchange sends the matching `code_verifier`. The token is stored next to the database as `*.token.json`. The token is not stored in DuckDB.

If the CLI runs on a remote host and you sign in on a laptop, open the tunnel **before** you click Allow:

```bash
ssh -L 41807:127.0.0.1:41807 USER@REMOTE
```

Or copy the redirect URL from the laptop and paste it into the remote prompt. The URL must use `127.0.0.1` and the listen port. It must include the same `state`. It must not include a user name or a fragment. You can also paste the `code=` value or the raw code. The exchange is bound to this sign-in by PKCE. If the paste includes `state`, including a percent-encoded `state` key, it must match. A URL on another host is rejected.

`invalid_grant` on refresh means Google rejected the refresh token. Stop `serve`. Run `logout` for the chosen database: it deletes only that `*.token.json` file. The token basename comes from the database path: `mail.duckdb` uses `mail.token.json`. Run `sync` and sign in with the same account. Do not remove the database or `credentials.json`. The error hint does not include token or HTTP response bytes. Background refresh failures do not open a browser.

`doctor` only checks local token presence, shape, and `gmail.readonly` scope. It cannot prove that Google will refresh the token. It does not call Google.

After the code exchange, the tool reads the scopes that Google granted. If `gmail.readonly` is not in the list, the sign-in fails with a clear message, and no token is saved. The token file keeps the granted scope for `doctor`.

The credentials file names the OAuth endpoints. The tool accepts only a Desktop client (`installed`) with `client_id` and `client_secret`, and only `https` endpoints on `accounts.google.com`, `oauth2.googleapis.com`, or `www.googleapis.com`. It checks this at `init` and at each sign-in. If the file does not name an endpoint, the tool uses the standard Google one. `logout` sends the refresh token to `https://oauth2.googleapis.com/revoke` in the POST body, not in the URL, and then deletes the token file.

Each token request has a 30-second limit. Each Gmail API request has a 2-minute limit. A server `Retry-After` value is capped at 5 minutes. HTTP error text keeps the status, the reason, and at most 200 characters of the Google error message. It never keeps the raw reply body. One Gmail reply, or one part of a batch reply, can be at most 128 MiB; a larger one is an error. Batch replies are read part by part, not all at once. The tool talks to Gmail with plain HTTPS REST calls and does not use the Google API client library.

## Opening a browser

The tool opens a browser with `open` (macOS), `xdg-open` (Linux), or `rundll32 url.dll,FileProtocolHandler` (Windows). It never runs a shell such as `cmd.exe`. It opens only `https` URLs, loopback `http` URLs, and existing local files.

## Private files

Secret files (`credentials.json` copy, `*.token.json`, `*.serve.json`, `*.open.html`, and `config.json`) are written as private files:

- Unix: files use mode `0600`. New app directories use mode `0700`. Existing parent directories stay unchanged.
- Windows: the current user and SYSTEM get access. Inherited access from other users is blocked.
- A write creates a private temp file in the same directory, writes the bytes, syncs, then replaces the destination as one file.
- A reader sees only the previous complete JSON or the new complete JSON.
- A later write replaces an existing shared file with a private file.
- A symlink destination or a non-regular file is rejected.
- A private read checks owner and file type, then tightens permissions, before it reads bytes. It does not follow a symlink.

## Loopback UI

`serve` and `ui` bind `127.0.0.1` only. Every request must come from a loopback address, with Host `127.0.0.1` or `localhost` on the listen port. That stops DNS rebinding.

Every `/api/` request needs the `X-Token` header. The API does not accept cookies: browsers send cookies to every port on `127.0.0.1`, and a web page cannot add a custom header to a cross-site request. `/api/` requests also need a matching `Origin` (if sent) and `Sec-Fetch-Site` of `none` or `same-origin` (if sent). The app shell (`/` and `/static/`) holds no mail data and no secret, so it skips the Origin and Fetch Metadata checks.

The browser gets the token through a one-time login code:

- `serve` makes a random 128-bit code. It works once and expires after 2 minutes.
- `serve` writes an owner-only launcher file, `*.open.html`, that redirects to `http://127.0.0.1:PORT/#code=CODE`, and opens that file. The code is in the URL fragment, so it does not go to the server in a request line, and it never appears in process arguments that other users can read.
- The page sends the code to `POST /api/login` and gets the token. It keeps the token in `localStorage`, which is scoped to this origin and port. It removes the code from the address bar.
- `ui` asks a running serve for a new code (`POST /api/login-code`, which needs `X-Token`).

The CLI sends `X-Token` from `*.serve.json`. Other local users cannot read that file. On a shared host, other users can still reach the loopback port, but without the token they get only the empty app shell.

Every response has a Content-Security-Policy (`default-src 'self'`, no inline script, `img-src 'none'`, `frame-ancestors 'none'`, `form-action 'none'`), `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, and `Referrer-Policy: no-referrer`. `/api/` responses also have `Cache-Control: no-store`.

The tool does not start the DuckDB UI. Older versions had a `--duckdb-ui` flag. It opened an SQL server on port 4213 with no token and installed the `ui` extension at runtime, so it was removed. To use DuckDB's own tools, stop `serve` first, then open the database file.

## Email HTML

HTML bodies go through a bluemonday allow-list policy on the server. Remote, protocol-relative, and `data:` image sources are removed. The mail page then shows the HTML in an `<iframe sandbox srcdoc>`. The sandbox allows only pop-ups: no scripts, no forms, no same-origin access, and no top-level navigation. Links open in a new tab. Plain-text bodies and all metadata use `textContent`.

## SQL restrictions

`sql` is read-only by default. Pass `--write` for ordinary database DML and DDL only. `--write` does not allow transaction control, settings changes, extension install or load, `ATTACH`/`DETACH`, `COPY`, import/export, or external files. `--read-only` is an explicit no-op for agents. Both modes disable DuckDB external file access and lock that configuration.

Dynamic `PIVOT table ON ...` is rejected. DuckDB expands that form into writes and multiple statements. Use `FROM table PIVOT (...)` for a supported read.

Limits stop one query from filling memory or holding the database:

- A result keeps at most 100,000 rows or about 64 MiB of values. After that, the tool stops reading rows and sets `truncated: true`. A write still commits, and only its `RETURNING` output is cut.
- A query stops after 5 minutes with the error `SQL stopped after 5m0s`. A stopped write does not commit.
- `NaN` and infinity are returned as the strings `"NaN"`, `"Infinity"`, and `"-Infinity"`, because JSON has no value for them. `DECIMAL` values are exact JSON numbers. `UUID` values are text.

## Local search index

DuckDB stores raw mail bodies and metadata in the mailbox file. Search acceleration adds a disposable owner-only SQLite sidecar (`*.duckdb.search/index.sqlite`). That sidecar stores tokenized postings plus message ids, internal dates, and search revisions. Those fields can reveal message content. Both files are plaintext. Owner-only mode is not encryption.

The process does not download a DuckDB FTS extension. DuckDB remains the verify authority. A symlink, non-regular, or unowned cache directory or sidecar is a startup error.

If the index is missing or `fts` is false, literal substring search still works. `fts` is a ready-index compatibility flag. Ranking stays newest-first. Background maintenance rebuilds or applies deltas without blocking the listener. Repair sizes live canonical text. Full and delta builds size cached `search_text` only.

`/api/status` returns a cached snapshot (`status_as_of`) and does not query DuckDB on the request path.

## Database path and sidecars

- Keep `credentials.json`, `*.token.json`, `*.serve.json`, `*.duckdb`, and `*.duckdb.search` out of git.
- Secret files are owner-only. Unix uses `0600`. Windows uses current-user and SYSTEM ACLs.
- The process sets Unix umask `077` once at start. It does not restore the previous umask.
- The database path stays where you set it. The tool does not move the file. The path is a filesystem path. A NUL byte, DSN options after `?`, or an in-memory URL is a startup error. A Windows drive colon is allowed.
- Before DuckDB opens the mailbox, the tool hardens an existing database and known WAL sidecars (`.wal`, `.wal.checkpoint`, `.wal.recovery`) when they are regular files you own. It does not delete WAL files. A symlink or a file you do not own is a startup error.
- DuckDB temp and spill files use a private directory next to the database (`*.duckdb.tmp`). Existing files in that directory are made private. A symlink or other non-regular entry in that directory is a startup error. The tool does not follow or delete those entries, and it does not change files outside that directory.
- New database files use mode `0600`. New private directories use mode `0700`. Windows uses current-user and SYSTEM ACLs, with inheritance on those directories, before DuckDB creates files.
- Unix accepts a custom parent that is only traversable (mode `0755`) because umask `077` still creates private files. A parent that is writable by group or other is refused. A parent that another user owns is also refused, because that user can rename or replace files in it. A parent that you or root own is accepted. The tool does not chmod or chown a custom directory.
- Windows requires a private inherited parent (current user and SYSTEM only) so DuckDB-created files stay private. The parent must inherit current-user protection to files and subdirectories. An inherit-only ACE for another trustee is refused. A custom unsafe parent is refused. `init` hardens the configured app config and data directories only.
- Windows also accepts the process token owner (often Administrators when the process is elevated) together with a tight DACL (current user, SYSTEM, and that token owner only). It does not trust an Admin-owned file that allows other trustees.
- A serve file that exists but is unreadable or unsafe is a hard error. The tool does not fall back to opening the database.
- A serve file records the absolute database path. `mail.duckdb` and `mail.db` share one serve file name, so the CLI uses a serve only when the paths match. Otherwise it opens its own database and keeps the other serve file.
- If serve does not answer, the CLI tries to open DuckDB. DuckDB's file lock decides: if the open works, serve has stopped, and the CLI removes the stale serve file. If the database is locked, the CLI stops and names the serve process.
- `serve` stops cleanly on SIGINT, SIGTERM, and SIGHUP. It removes the serve file and the launcher file.
- A `config.json` that cannot be read or is not valid JSON is an error. The tool does not fall back to `./mail.duckdb`.
