# Telegram MCP

A Go implementation of [22syn/telegram-user-mcp](https://github.com/22syn/telegram-user-mcp) with hexagonal architecture and environment-variable configuration.

The service connects to a **personal Telegram account over MTProto** and exposes 14 MCP tools over stdio or, with `telegram-mcp http`, over authenticated HTTP. Personal conversations, groups, and channels visible to the account are available. It does not use the Bot API or a bot token.

## Getting started

Go 1.26+ and an existing Telegram account are required.

1. Obtain the credentials for your Telegram application:

   - Sign in to [my.telegram.org](https://my.telegram.org) with the phone number of your existing Telegram account and complete the verification requested by Telegram.
   - Open **API development tools**. If you have not registered an application, complete and submit the application form. If an application already exists, use its credentials.
   - Copy **App api_id** (`api_id`) into `TELEGRAM_API_ID` and **App api_hash** (`api_hash`) into `TELEGRAM_API_HASH`. The ID is a positive integer; the hash is a 32-character hexadecimal string. Copy both exactly from the portal.
   - Set `TELEGRAM_PHONE` to the number of the account you want to authorize, including `+` and the country calling code, for example `+491234567890` (placeholder only).

   See Telegram's [official application registration guide](https://core.telegram.org/api/obtaining_api_id). This service uses a personal account: you do not need to create a bot with BotFather, obtain a bot token, or find a chat ID or group URL to configure it. Keep your API credentials private.

2. Prepare the environment:

   ```sh
   cp .env.example .env
   chmod 600 .env
   # Set TELEGRAM_API_ID, TELEGRAM_API_HASH, and TELEGRAM_PHONE in .env.
   set -a
   . ./.env
   set +a
   ```

   The application reads environment variables only; it does not load `.env` automatically. Source it explicitly from the shell or process manager. Quote values that contain spaces.

3. Build the binary and authorize once in a regular terminal:

   ```sh
   go build -o bin/telegram-mcp .
   ./bin/telegram-mcp setup
   ```

   Enter the login code sent by Telegram for this setup attempt and, if prompted, your account's two-step verification (2FA) password. These are interactive inputs, not environment variables; do not put them in `.env` or MCP-client configuration. Input is hidden.

   Setup displays the delivery method reported by Telegram before asking for the code. For an in-app notification, check the Telegram service chat in other sessions logged into the same account; for SMS or a voice call, check your phone. A successful request does not confirm that the code arrived. SMS word/phrase codes must be entered in full.

   If no code arrives, enter `resend` at the code prompt. This requests Telegram's next delivery method only if Telegram offers one and its displayed timeout has elapsed. An early request shows the remaining wait; it does not queue an automatic resend. If no fallback is available, enter the original code or press Ctrl+C to exit. Telegram controls delivery and can restrict SMS for third-party apps; restarting setup repeatedly does not force SMS. After repeated requests Telegram may report a code as delivered yet silently drop it — waiting helps, restarting does not. Rate limits display the retry delay and stop the attempt. Unsupported delivery flows (including email verification, Firebase and Fragment) stop with guidance to check your account in an official Telegram client. See [Telegram's authorization documentation](https://core.telegram.org/api/auth#sending-a-verification-code).

   If codes never arrive, authorize without a code instead: `./bin/telegram-mcp setup qr` shows a QR login token (refreshed on expiry); confirm it in the Telegram app on the logged-in phone via Settings → Devices → Scan QR Code. If the account has two-step verification, enter the 2FA password when prompted after confirming the scan — without it Telegram leaves the login incomplete. QR login needs no phone number or code and is unaffected by code-delivery restrictions.

   Successful setup creates the local session file at `TELEGRAM_SESSION_PATH` (by default `~/.telegram-mcp/session.json`) with `0600` permissions. You do not download this file from the developer portal or use a string-session generator. The command never writes the login code or password to configuration. Creating a new Telegram account is not supported.

4. Connect the executable to an MCP client:

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

   Replace the absolute path and credentials with your own values from step 1. `TELEGRAM_PHONE` is needed only for `setup`; normal startup reuses the saved session. Run the client under the same OS user so the default session path resolves to the same file. If you changed `TELEGRAM_SESSION_PATH` during `setup`, pass the same path to the client, preferably as an absolute path. The JSON example is for clients that use the `mcpServers` format; configure the equivalent command and environment variables in other clients.

Without arguments, the executable starts the MCP server. stdout contains protocol output only; errors and authorization prompts go to stderr. `--help` works without credentials. Use one server process per session.

## HTTP access

`telegram-mcp http` serves MCP over the Streamable HTTP transport at `http://TELEGRAM_MCP_HTTP_ADDR/mcp` instead of stdio. Run `setup` first; the same session is used.

```sh
export TELEGRAM_MCP_HTTP_TOKEN="$(openssl rand -hex 32)"   # keep it; clients need it
./bin/telegram-mcp http
# serving MCP on http://127.0.0.1:8080/mcp
```

- Every request must carry `Authorization: Bearer <TELEGRAM_MCP_HTTP_TOKEN>`; others get `401`. The token grants full access to the tools, including `mark_read` and downloads, so treat it like the session file.
- The server speaks plain HTTP and only listens on loopback (`127.0.0.1`, `[::1]`, `localhost`); `0.0.0.0` or `:8080` is refused. Publish it over HTTPS through a reverse proxy on the same host.
- `download_media` and `get_thumbnail` write files on the server host, not on the remote client.
- Run it under a process manager, for example a systemd unit with `ExecStart=/path/to/bin/telegram-mcp http` and the variables in `EnvironmentFile=`. SIGTERM stops it cleanly.

Caddy example (TLS certificates are obtained automatically):

```caddyfile
tg.example.com {
    reverse_proxy 127.0.0.1:8080 {
        header_up Host {upstream_hostport}
    }
}
```

`header_up Host` is required: the server rejects requests that reach its loopback listener with a public `Host` header (DNS-rebinding protection). Without it every request fails with `403 Forbidden: invalid Host header`.

See [Client setup](#client-setup) for Claude, ChatGPT/Codex and Hermes.

## Client setup

Every client can run the server in one of two ways:

- **Local (stdio):** the client starts `bin/telegram-mcp` itself as a child process on the same machine. You don't need a token or a proxy. The client runs as the OS user that ran `setup`.
- **Remote (HTTP):** you run `telegram-mcp http` behind Caddy (see [HTTP access](#http-access)). The client connects to `https://tg.example.com/mcp` with `Authorization: Bearer <token>`.

In the examples below, replace the paths, the API ID and hash, the domain and the token with your own values. Do not commit files that contain them.

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

Local: open **Settings → Developer → Edit Config** in Claude Desktop, add the server to `claude_desktop_config.json`, and restart the app:

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

Remote: go to **Customize → Connectors → Add custom connector** and enter `https://tg.example.com/mcp`. Set authentication to **No sign-in**. Under **Request headers**, add `authorization` with the value `Bearer <token>`, and include the `Bearer ` prefix. Claude connects from Anthropic's cloud, so the URL must be reachable from the internet. At the time of writing, request headers are in beta and only some accounts have them ([docs](https://claude.com/docs/connectors/custom/add-unlisted#authenticate-with-request-headers)). If the dialog has no **Request headers** section, use the local setup or Claude Code. Do not expose the server without a token.

### ChatGPT and Codex

The ChatGPT app (developer-mode connectors) only supports remote servers with OAuth or no authentication. It cannot send a static bearer token and cannot start local processes, so it can't use this server. Use the OpenAI Codex CLI or IDE extension instead. Add one of these to `~/.codex/config.toml`.

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

## Configuration

Only the API ID and hash come from Telegram's developer portal; the phone number belongs to your account. Optional paths, download limits, and timeouts are local settings you choose, and their defaults can be left unchanged.

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

`~/` in paths expands to the home directory; relative paths are resolved from the working directory. The session is authorization state, separate from environment configuration. It is incompatible with a GramJS string session; run `setup` again. The session file grants access to the account; do not commit `.env`, session files, or downloaded files to Git. The HTTP settings are read only by `telegram-mcp http`; stdio startup ignores them.

## Tools

| Tool | Required parameters | Optional parameters |
| --- | --- | --- |
| `read_messages` | `groupUrl` | `limit`: 50, maximum 200 |
| `search_messages` | `groupUrl`, `query` | `limit`: 30, maximum 100 |
| `fetch_since` | `groupUrl`, `sinceId` | `limit`: 100, maximum 200 |
| `get_message` | `groupUrl`, `messageId` | — |
| `global_search` | `query` | `limit`: 30, maximum 100 |
| `get_group_info` | `groupUrl` | — |
| `list_dialogs` | — | `limit`: 100, maximum 500; `archived`: false |
| `list_folders` | — | — |
| `list_folder_dialogs` | `folderId` | `limit`: 100, maximum 500 |
| `get_pinned` | `groupUrl` | `limit`: 20, maximum 50 |
| `get_media_info` | `groupUrl`, `messageId` | — |
| `download_media` | `groupUrl`, `messageId` | `maxMB`: lowers the configured limit; `0` or omitted uses it |
| `get_thumbnail` | `groupUrl`, `messageId` | — |
| `mark_read` | `groupUrl` | `messageId`: marks up to and including it; omitted marks the whole chat |

`groupUrl` accepts `@username`, a name without `@`, a `https://t.me/name` URL, a message link, or a numeric **ID as a string** from `list_dialogs`. `https://t.me/c/...` links are supported for private channels. Invite links are not used: the service never joins chats. Numeric IDs are resolved through the account's dialogs, including archived dialogs.

Existing tool and parameter names match the original; result text is not a byte-for-byte copy. Numeric parameters must be integers. `archived=true` selects only the archive, while `false` selects only the main list. This is the original's actual behavior despite its “include archived” description.

`list_folders` shows each custom folder's `folderId` and its **explicitly included** chat count; automatic rules can add more chats. Pass that integer ID to `list_folder_dialogs`, for example `{"folderId": 2, "limit": 50}`. It returns up to 100 matching chats by default (maximum 500), with marked chat IDs usable by other tools. Pinned chats come first, then other explicit inclusions, then automatic matches from the main list and archive. This order is not Telegram's exact tab order. An empty folder returns explanatory text; a missing ID returns an error. `list_dialogs` keeps its main/archive selection.

`read_messages`, `search_messages`, and `get_pinned` return selected messages in ascending ID order. `[album:...]` labels preserve the complete 64-bit ID as a string, and `[reply:...]` labels explicitly connect replies. `get_message` also shows views, forwards, reactions, and the reply count.

`fetch_since` returns the earliest messages after `sinceId` and a `maxId` for the next call. Pass the returned `maxId` to the next request until the result is empty. A large backlog is not skipped when `limit` is reached.

Media is downloaded as a stream, with checks for both declared size and bytes actually written. `get_thumbnail` selects a preview no larger than 320 pixels per side or an embedded thumbnail without downloading the original. Repeated downloads create separate files with `0600` permissions; incomplete files are removed on failure. Media from chats that prohibit saving (`no-forward`) is not downloaded. These two tools are marked in MCP as writing local files. Sending and editing messages are not supported.

`mark_read` is the only tool that changes Telegram account state, and no other tool marks messages as read. Telegram stores read state as a per-chat cursor, so `messageId` marks every message up to and including it; individual messages cannot be marked in isolation. Without `messageId` the whole chat is marked read up to its latest message, and a manual "marked as unread" flag is cleared. Unread mention and reaction badges and forum topic read state are not changed. The tool is advertised as not read-only and idempotent.

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

Call flow: `MCP → Executor → app.Service → port.Telegram → MTProto`. Interfaces belong to their consumers. The core imports no SDK and reads no environment variables; dependencies are passed through constructors at the entry point. The project uses the [official Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk) and [gotd/td](https://github.com/gotd/td).

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

Tests run without credentials or network access: the MCP client covers the handshake, all 12 tools, calls, validation, and errors. A fake MTProto transport covers paginated history, cursors with ID gaps, archived chats, and message-to-chat ownership. Media metadata, limits, and file cleanup are tested separately. A real login, the Telegram API, and downloads from a real account must be checked manually after `setup`.

## Origin

Functional reference: [22syn/telegram-user-mcp, commit 073e96a](https://github.com/22syn/telegram-user-mcp/tree/073e96a33f8ef3e04c60ab60882bf4304c40ab64). The original project is MIT © 2026 Kobi Hazout; its notice is preserved in [LICENSE](LICENSE).
