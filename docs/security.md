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

`invalid_grant` on refresh means Google rejected the refresh token. Stop `serve`. Remove only the `*.token.json` file for the chosen database. The token basename comes from the database path: `mail.duckdb` uses `mail.token.json`. Run `sync` and sign in with the same account. Do not remove the database or `credentials.json`. The error hint does not include token or HTTP response bytes. Background refresh failures do not open a browser.

`doctor` only checks local token presence, shape, and `gmail.readonly` scope. It cannot prove that Google will refresh the token. It does not call Google.

## Private files

Secret files (`credentials.json` copy, `*.token.json`, `*.serve.json`, and `config.json`) are written as private files:

- Unix: files use mode `0600`. New app directories use mode `0700`. Existing parent directories stay unchanged.
- Windows: the current user and SYSTEM get access. Inherited access from other users is blocked.
- A write creates a private temp file in the same directory, writes the bytes, syncs, then replaces the destination as one file.
- A reader sees only the previous complete JSON or the new complete JSON.
- A later write replaces an existing shared file with a private file.
- A symlink destination or a non-regular file is rejected.
- A private read checks owner and file type, then tightens permissions, before it reads bytes. It does not follow a symlink.

## Loopback UI

`serve` and `ui` bind `127.0.0.1` only. The browser Host must be `127.0.0.1` or `localhost` on the listen port. Cross-site and other local-port origins are rejected. The browser gets an HttpOnly session cookie. The CLI sends `X-Token` from `*.serve.json`. The token is not in the printed URL. Do not run them on a shared host if other users can reach your loopback port.

The DuckDB UI on port 4213 has no session token. It stays off unless you pass `--duckdb-ui`. Treat that flag as full database access on loopback.

## SQL restrictions

`sql` is read-only by default. Pass `--write` for ordinary database DML and DDL only. `--write` does not allow transaction control, settings changes, extension install or load, `ATTACH`/`DETACH`, `COPY`, import/export, or external files. `--read-only` is an explicit no-op for agents. Both modes disable DuckDB external file access and lock that configuration.

Dynamic `PIVOT table ON ...` is rejected. DuckDB expands that form into writes and multiple statements. Use `FROM table PIVOT (...)` for a supported read.

## Local search index

DuckDB stores raw mail bodies and metadata in the mailbox file. Search acceleration adds a disposable owner-only SQLite sidecar (`*.duckdb.search/index.sqlite`). That sidecar stores tokenized postings plus message ids, internal dates, and search revisions. Those fields can reveal message content. Both files are plaintext. Owner-only mode is not encryption.

The process does not download a DuckDB FTS extension. DuckDB remains the verify authority. A symlink, non-regular, or unowned cache directory or sidecar is a startup error. `--duckdb-ui` disables acceleration for that run and leaves the cache dirty. The next normal `serve` may rebuild it.

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
- Unix accepts a custom parent that is only traversable (mode `0755`) because umask `077` still creates private files. A parent that is writable by group or other is refused. The tool does not chmod a custom directory.
- Windows requires a private inherited parent (current user and SYSTEM only) so DuckDB-created files stay private. The parent must inherit current-user protection to files and subdirectories. An inherit-only ACE for another trustee is refused. A custom unsafe parent is refused. `init` hardens the configured app config and data directories only.
- Windows also accepts the process token owner (often Administrators when the process is elevated) together with a tight DACL (current user, SYSTEM, and that token owner only). It does not trust an Admin-owned file that allows other trustees.
- A serve file that exists but is unreadable or unsafe is a hard error. The tool does not fall back to opening the database.
