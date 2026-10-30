# Telegram MCP

<p align="center">
  <img src="./assets/readme/hero.svg" width="100%" alt="Telegram MCP connects MCP clients over stdio or authenticated HTTP to your personal Telegram account over MTProto.">
</p>

A Go MCP server for reading and searching conversations, groups, and channels visible to **your personal Telegram account**. Connect through stdio or authenticated HTTP; Telegram access uses MTProto, with no Bot API or bot token.

[Get started](#getting-started) · [Connect a client](#client-setup) · [HTTP hosting](#http-access) · [Tool reference](#tools) · [Configuration](#configuration)

- **Find context:** read history, search one chat or across chats, inspect replies and pinned messages.
- **Follow conversations:** check which account is connected, browse dialogs and folders (optionally unread only), or page through new messages with a safe `fetch_since` cursor.
- **Inspect media:** view metadata, download files, fetch thumbnails to a file, or view them inline as images, within configured limits.

### A tool call

After setup, ask your MCP client to call `fetch_since` with these arguments:

```json
{
  "groupUrl": "@channel_username",
  "sinceId": 1234,
  "limit": 50
}
```

Replace the example chat and ID. The tool returns the earliest messages after `sinceId`; use the returned `maxId` as the next cursor. See [Tools](#tools) for all 17 tools and their limits.

**Side effects are explicit:** only `mark_read` and `save_draft` change account state: `mark_read` advances the read cursor, `save_draft` replaces a chat's draft. `download_media` and `get_thumbnail` write local files; `view_thumbnail` writes none. Other tools do not mark messages read. Nothing sends, edits, or deletes messages or joins chats.

## Getting started

Requires Go 1.26+ and an existing Telegram account; creating accounts is unsupported.

1. **Get API credentials.** At [my.telegram.org](https://my.telegram.org), sign in with your account's phone number, complete verification, and open **API development tools**. Register an application if needed, then copy **App api_id** (positive integer) and **App api_hash** (32 hexadecimal characters) to `TELEGRAM_API_ID` and `TELEGRAM_API_HASH`. Set `TELEGRAM_PHONE` to the account number with `+` and country code, e.g. `+491234567890` (placeholder).

   See the [official registration guide](https://core.telegram.org/api/obtaining_api_id). Keep credentials private; no BotFather, bot token, chat ID, or group URL is needed for setup.

2. **Load the environment.**

   ```sh
   cp .env.example .env
   chmod 600 .env
   # Set TELEGRAM_API_ID, TELEGRAM_API_HASH, and TELEGRAM_PHONE in .env.
   set -a
   . ./.env
   set +a
   ```

   Configuration is environment-only: `.env` is not loaded automatically. Source it in your shell or process manager; quote values containing spaces.

3. **Build and authorize.** Run once in an interactive terminal:

   ```sh
   go build -o bin/telegram-mcp .
   ./bin/telegram-mcp setup
   ```

   Enter the code for this attempt and, if prompted, your two-step verification (2FA) password. Input is hidden; codes and passwords are interactive inputs, never `.env` or client settings. Setup reports the delivery method: check the Telegram service chat in other sessions of the same account for in-app codes, or your phone for SMS/calls. A successful request does not prove delivery; enter SMS word/phrase codes in full.

   **No code:** enter `resend`. It requests the next method only when Telegram offers one and the displayed timeout has elapsed. Early requests show the wait without queuing a resend. With no fallback, enter the requested code or exit with Ctrl+C. Telegram controls delivery and may restrict third-party SMS or silently drop repeated requests despite reporting delivery. Repeated restarts cannot force SMS; wait before retrying. Rate limits show a retry delay and stop setup. Unsupported email verification, Firebase, or Fragment flows stop with official-client guidance. See [Telegram authorization](https://core.telegram.org/api/auth#sending-a-verification-code).

   **QR alternative:** run `./bin/telegram-mcp setup qr`, then confirm the QR token in the logged-in phone's Telegram app: Settings → Devices → Scan QR Code. Tokens refresh on expiry. Enter 2FA when prompted after scanning; otherwise login remains incomplete. QR needs no phone number or code and is unaffected by code-delivery restrictions.

   Setup creates `TELEGRAM_SESSION_PATH` (default `~/.telegram-mcp/session.json`) with `0600` permissions. This is a local session, not a portal download or generated string session. Updates replace the file atomically (temp file, fsync, rename), so a crash or full disk keeps the previous session. Codes and passwords are never written to configuration.

4. **Connect your client.** Use this shared `mcpServers` configuration:

   ```json
   {
     "mcpServers": {
       "telegram": {
         "command": "/absolute/path/to/telegram-mcp/bin/telegram-mcp",
         "env": {
           "TELEGRAM_API_ID": "123456",
           "TELEGRAM_API_HASH": "your_32_character_api_hash"
         }
       }
     }
   }
   ```

   Replace the path and credentials. `TELEGRAM_PHONE` is setup-only; normal startup reuses the session. Use the same OS user and, if customized, the same `TELEGRAM_SESSION_PATH` (preferably absolute). For clients without `mcpServers`, set the equivalent command and environment variables.

Without arguments, the binary starts stdio MCP. stdout is protocol-only; diagnostics and setup prompts use stderr. `--help` needs no credentials. Use one server process per session.

## Client setup

- **Local (stdio):** the client starts the binary as a child process under the OS user who ran `setup`; no token or proxy is needed.
- **Remote (HTTP):** run the server as described in [HTTP access](#http-access), then connect to `https://tg.example.com/mcp` with the bearer token.

Replace example paths, credentials, domain, and token; never commit them.

### Claude Code

Local:

```sh
claude mcp add telegram -s user \
  -e TELEGRAM_API_ID=123456 -e TELEGRAM_API_HASH=your_32_character_api_hash \
  -- /absolute/path/to/telegram-mcp/bin/telegram-mcp
```

Remote:

```sh
claude mcp add telegram -s user --transport http https://tg.example.com/mcp \
  --header "Authorization: Bearer $TELEGRAM_MCP_HTTP_TOKEN"
```

Run `/mcp` inside Claude Code to check the connection.

### Claude Desktop and claude.ai

Local: in Claude Desktop, open **Settings → Developer → Edit Config**, put the [shared JSON example](#getting-started) in `claude_desktop_config.json`, and restart.

Remote: **Customize → Connectors → Add custom connector**, URL `https://tg.example.com/mcp`, authentication **No sign-in**. Under **Request headers**, set `authorization` to `Bearer <token>` (prefix required). Anthropic's cloud must reach the URL. Request headers were beta and account-dependent when documented ([docs](https://claude.com/docs/connectors/custom/add-unlisted#authenticate-with-request-headers)); if absent, use local setup or Claude Code. Never expose the server without a token.

### ChatGPT and Codex

The documented ChatGPT developer-mode connector cannot use this server: it supports remote OAuth/no-auth servers, cannot send static bearer tokens, and cannot start local processes. Use Codex CLI or IDE extension; add one example to `~/.codex/config.toml`.

Local:

```toml
[mcp_servers.telegram]
command = "/absolute/path/to/telegram-mcp/bin/telegram-mcp"
env = { TELEGRAM_API_ID = "123456", TELEGRAM_API_HASH = "your_32_character_api_hash" }
```

Remote (the token is read from the environment variable named here, not stored in the file):

```toml
[mcp_servers.telegram]
url = "https://tg.example.com/mcp"
bearer_token_env_var = "TELEGRAM_MCP_HTTP_TOKEN"
```

Run `/mcp` inside Codex to check the connection.

### Hermes Agent

Add one of these to `~/.hermes/config.yaml`, then run `/reload-mcp`. `${VAR}` placeholders are filled in from the environment.

Local:

```yaml
mcp_servers:
  telegram:
    command: "/absolute/path/to/telegram-mcp/bin/telegram-mcp"
    env:
      TELEGRAM_API_ID: "123456"
      TELEGRAM_API_HASH: "your_32_character_api_hash"
```

Remote:

```yaml
mcp_servers:
  telegram:
    url: "https://tg.example.com/mcp"
    headers:
      Authorization: "Bearer ${TELEGRAM_MCP_HTTP_TOKEN}"
```

## HTTP access

After `setup`, `telegram-mcp http` reuses the session and serves Streamable HTTP at `http://TELEGRAM_MCP_HTTP_ADDR/mcp`:

```sh
export TELEGRAM_MCP_HTTP_TOKEN="$(openssl rand -hex 32)"   # keep it; clients need it
./bin/telegram-mcp http
# serving MCP on http://127.0.0.1:8080/mcp
```

- Require `Authorization: Bearer <TELEGRAM_MCP_HTTP_TOKEN>` on every request; otherwise `401`. Protect the token like the session: it grants all tools, including `mark_read`, `save_draft` and downloads.
- Plain HTTP binds only to loopback (`127.0.0.1`, `[::1]`, `localhost`); `0.0.0.0` and `:8080` are refused. Publish through an HTTPS proxy on the same host.
- Files from `download_media` and `get_thumbnail` stay on the server host. Use `view_thumbnail` to get a preview inline in the MCP response.
- Use a process manager, e.g. systemd with `ExecStart=/path/to/bin/telegram-mcp http` and `EnvironmentFile=`. On SIGTERM or a listener error, the server cancels in-flight tool calls and waits for them (HTTP shutdown up to 5s) before disconnecting from Telegram, so incomplete downloads are removed.

Caddy example (TLS certificates are obtained automatically):

```caddyfile
tg.example.com {
    reverse_proxy 127.0.0.1:8080 {
        header_up Host {upstream_hostport}
    }
}
```

Keep `header_up Host`: DNS-rebinding protection rejects public `Host` headers at the loopback listener with `403 Forbidden: invalid Host header`.

See [Client setup](#client-setup) for Claude, ChatGPT/Codex and Hermes.

## Configuration

The API ID/hash come from Telegram's portal; the phone belongs to your account. Paths, limits, and timeouts are local settings with usable defaults.

| Variable | Default | Purpose |
| --- | --- | --- |
| `TELEGRAM_API_ID` | Required | Positive 32-bit Telegram API ID |
| `TELEGRAM_API_HASH` | Required | 32 hexadecimal characters |
| `TELEGRAM_PHONE` | Required for `setup` (unused by `setup qr`) | Phone number with country calling code |
| `TELEGRAM_SESSION_PATH` | `~/.telegram-mcp/session.json` | gotd authorized-session file |
| `TELEGRAM_DOWNLOAD_DIR` | `~/.telegram-mcp/downloads` | Directory for downloaded files |
| `TELEGRAM_MAX_DOWNLOAD_MB` | `200` | Download limit in MiB and ceiling for `maxMB`; `0` disables the limit |
| `TELEGRAM_REQUEST_TIMEOUT` | `5m` | Total timeout for a tool call, including downloads |
| `TELEGRAM_MCP_HTTP_ADDR` | `127.0.0.1:8080` | Listen address for `telegram-mcp http`; loopback hosts only |
| `TELEGRAM_MCP_HTTP_TOKEN` | Required for `http` | Bearer token, at least 32 characters; generate with `openssl rand -hex 32` |

`~/` expands to your home; relative paths use the process working directory. Sessions hold account authorization, separate from configuration; GramJS string sessions are incompatible, so run `setup` again. Never commit `.env`, sessions, or downloads. Only `telegram-mcp http` reads HTTP settings; stdio ignores them.

## Tools

Defaults and maximums are listed below. Numeric parameters are integers; chat IDs are strings.

| Tool | Required parameters | Optional parameters |
| --- | --- | --- |
| `read_messages` | `groupUrl` | `limit`: 50, maximum 200; `beforeId`: only older messages; `unreadOnly`: false; `maxTextChars` |
| `search_messages` | `groupUrl`, `query` | `limit`: 30, maximum 100; `beforeId`: only older matches; `maxTextChars` |
| `fetch_since` | `groupUrl`, `sinceId` | `limit`: 100, maximum 200; `maxTextChars` |
| `get_message` | `groupUrl`, `messageId` | — |
| `global_search` | `query` | `limit`: 30, maximum 100; `maxTextChars` |
| `get_group_info` | `groupUrl` | — |
| `get_me` | — | — |
| `list_dialogs` | — | `limit`: 100, maximum 500; `archived`: false; `unreadOnly`: false |
| `list_folders` | — | — |
| `list_folder_dialogs` | `folderId` | `limit`: 100, maximum 500; `unreadOnly`: false |
| `get_pinned` | `groupUrl` | `limit`: 20, maximum 50; `maxTextChars` |
| `get_media_info` | `groupUrl`, `messageId` | — |
| `download_media` | `groupUrl`, `messageId` | `maxMB`: lowers the configured limit; `0` or omitted uses it |
| `get_thumbnail` | `groupUrl`, `messageId` | — |
| `view_thumbnail` | `groupUrl`, `messageId` | — |
| `mark_read` | `groupUrl` | `messageId`: marks up to and including it; omitted marks the whole chat |
| `save_draft` | `groupUrl`, `text` | — |

### Chats and folders

`groupUrl` accepts `@username`, a bare name, `https://t.me/name`, message links (including private-channel `https://t.me/c/...`), or a numeric **ID as a string** from `list_dialogs`. IDs resolve through main and archived dialogs. Invite links are unused; the service never joins chats. `get_me` returns the connected account's ID, name and username, never its phone number.

Numeric parameters must be integers. `archived=true` selects only the archive; `false` selects only the main list.

`unreadOnly=true` on `list_dialogs` or `list_folder_dialogs` keeps only chats with unread messages or a manual "marked as unread" flag; `limit` counts those chats, so the scan continues past read ones. Dialog lines show `marked unread` for manually marked chats.

`list_folders` reports custom `folderId`s and **explicitly included** chat counts; automatic rules may add chats. Use the integer ID with `list_folder_dialogs`, e.g. `{"folderId": 2, "limit": 50}`; returned marked chat IDs work with other tools. Order: pinned, other explicit inclusions, then automatic main/archive matches; it differs from Telegram's tab order. Empty folders return explanatory text; missing IDs return errors. `list_dialogs` retains its main/archive selection.

### Messages and cursors

`read_messages`, `search_messages`, and `get_pinned` sort selected messages by ascending ID. `[album:...]` preserves the full 64-bit ID as a string; `[reply:...]` connects replies. `get_message` adds views, forwards, reactions, and reply count.

`fetch_since` returns the earliest messages after `sinceId`; use its `maxId` as the next `sinceId` until results are empty. Reaching `limit` never skips backlog.

`read_messages` with `unreadOnly=true` returns the oldest messages after the chat's read cursor, with `maxId`. Continue with `fetch_since` using that `maxId` as `sinceId`, or call `mark_read` with `messageId=maxId` and repeat. Reading never marks messages read, and `unreadOnly` cannot be combined with `beforeId`. Forum topics use the chat-wide read cursor.

To walk older history, call `read_messages` or `search_messages` without `beforeId`, then pass each returned `nextBeforeId` (the smallest ID in the page) as `beforeId`. Repeat until the response is empty; that response has no cursor. `beforeId` is exclusive, so pages never overlap or skip messages.

To keep list responses small, pass `maxTextChars` (minimum 1) to `read_messages`, `search_messages`, `fetch_since`, `get_pinned` or `global_search`: each longer text is cut to that many characters (Unicode code points, not bytes) and ends with `[truncated]`. Omitted, texts are returned in full. `get_message` always returns the full text.

### Media and local files

Downloads stream with declared-size and actual-byte checks. `get_thumbnail` fetches a preview up to 320 pixels per side or an embedded thumbnail, without full media. `view_thumbnail` returns the same preview as MCP image content (with its detected MIME type), plus a text item with the MIME type and size, and writes no file, so remote HTTP clients can see it; the configured size limit applies to the preview bytes. Repeated downloads create separate `0600` files; failures remove incomplete files. All three tools refuse saving-prohibited (`no-forward`) media; `download_media` and `get_thumbnail` have MCP annotations for local-file writes.

### Read state

Only `mark_read` changes Telegram read state; other tools never acknowledge reads. Its per-chat cursor marks all messages through `messageId`, not individual messages. Omit `messageId` to mark through the latest message and clear manual "marked as unread". Mention/reaction badges and forum-topic read state stay unchanged. MCP annotations declare it not read-only and idempotent.

### Drafts

`save_draft` stores `text` (1–4096 characters, plain text, kept exactly as given) as the chat's cloud draft, replacing any existing draft; the user sees it in every Telegram client and decides whether to send it. It never sends a message, does not mark messages read, and resolves `groupUrl` like other tools, so invite links are rejected. Telegram may refuse drafts where the account cannot post, e.g. broadcast channels; that returns an error. MCP annotations declare it not read-only, destructive (it overwrites a draft) and idempotent.

## Architecture

```text
main.go                 — Uber Fx dependency wiring, lifecycle, CLI setup
internal/config          — environment loading and validation
internal/domain          — messages, chats, folders, media; standard library only
internal/port            — outbound Telegram interface
internal/app             — use cases, validation, limits, formatting
internal/adapter/mcp     — inbound `Executor` port, MCP schemas, stdio server, and HTTP handler
internal/adapter/telegram — outbound MTProto adapter and file downloads
```

Flow: `MCP → Executor → app.Service → port.Telegram → MTProto`. Consumers own interfaces; the core has no SDK imports or environment reads. Entry-point constructors supply dependencies. Adapters use the [official Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk) and [gotd/td](https://github.com/gotd/td).

## Development and checks

```sh
task build
task test
task lint
```

Without Task:

```sh
go build -o bin/telegram-mcp .
go test -race ./...
golangci-lint run ./...
```

Offline tests need no credentials or network. MCP tests cover handshake, tools, calls, validation, errors, and inline `view_thumbnail` images; fake MTProto tests cover `fetch_since` and `beforeId` history and search walks across ID gaps, archives, and message/chat ownership, plus `unreadOnly` scans past read pages and unread reads from the read cursor, `get_me` without the phone, and `save_draft` issuing only the draft RPC. Other tests cover `maxTextChars` truncation, media metadata, limits, truncated-download rejection, cleanup, atomic session writes, and HTTP shutdown draining. Real login, API access, and account downloads require manual checks after `setup`.

CI (`.github/workflows/ci.yml`) runs `go test -race ./...`, golangci-lint v2.15.0, and `go build .` on pull requests and pushes to `main`. The lint config is not tracked: CI and `task lint` download `.golangci.yml` from [adlandh/golangci-lint-config](https://github.com/adlandh/golangci-lint-config) before linting; without Task, fetch it first with `curl -fsS https://raw.githubusercontent.com/adlandh/golangci-lint-config/refs/heads/main/.golangci.yml -o .golangci.yml`. `govulncheck.yml` scans with the current stable Go on the same triggers and weekly. Dependabot updates Go modules and GitHub Actions weekly.

## License

[MIT](LICENSE).
