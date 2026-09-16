# Telegram MCP

A Go implementation of [22syn/telegram-user-mcp](https://github.com/22syn/telegram-user-mcp) with hexagonal architecture and environment-variable configuration.

The service connects to a **personal Telegram account over MTProto** and exposes 12 MCP tools over stdio. Personal conversations, groups, and channels visible to the account are available. It does not use the Bot API or a bot token.

## Getting started

Go 1.26+ and an existing Telegram account are required.

1. Obtain `api_id` and `api_hash` from [Telegram API development tools](https://my.telegram.org/apps).
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
   go build -o bin/telegram-mcp ./cmd/telegram-mcp
   ./bin/telegram-mcp setup
   ```

   Enter the code sent by Telegram and, if prompted, the 2FA password. Input is hidden. The command stores the session with `0600` permissions; it never writes the code or password to configuration. Creating a new Telegram account is not supported.

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

   Replace the absolute path and credentials. If you changed `TELEGRAM_SESSION_PATH` during `setup`, pass the same path to the client. The JSON example is for clients that use the `mcpServers` format; configure the equivalent command and environment variables in other clients.

Without arguments, the executable starts the MCP server. stdout contains protocol output only; errors and authorization prompts go to stderr. `--help` works without credentials. Use one server process per session.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `TELEGRAM_API_ID` | Required | Positive 32-bit Telegram API ID |
| `TELEGRAM_API_HASH` | Required | 32 hexadecimal characters |
| `TELEGRAM_PHONE` | Required for `setup` | Phone number with country calling code |
| `TELEGRAM_SESSION_PATH` | `~/.telegram-mcp/session.json` | gotd authorized-session file |
| `TELEGRAM_DOWNLOAD_DIR` | `~/.telegram-mcp/downloads` | Directory for downloaded files |
| `TELEGRAM_MAX_DOWNLOAD_MB` | `200` | Download limit in MiB; `0` disables the limit |
| `TELEGRAM_REQUEST_TIMEOUT` | `5m` | Total timeout for a tool call, including downloads |

`~/` in paths expands to the home directory; relative paths are resolved from the working directory. The session is authorization state, separate from environment configuration. It is incompatible with a GramJS string session; run `setup` again. The session file grants access to the account; do not commit `.env`, session files, or downloaded files to Git.

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
| `get_pinned` | `groupUrl` | `limit`: 20, maximum 50 |
| `get_media_info` | `groupUrl`, `messageId` | — |
| `download_media` | `groupUrl`, `messageId` | `maxMB`: overrides the limit; `0` means no limit |
| `get_thumbnail` | `groupUrl`, `messageId` | — |

`groupUrl` accepts `@username`, a name without `@`, a `https://t.me/name` URL, a message link, or a numeric **ID as a string** from `list_dialogs`. `https://t.me/c/...` links are supported for private channels. Invite links are not used: the service never joins chats. Numeric IDs are resolved through the account's dialogs, including archived dialogs.

Tool and parameter names match the original; result text is not a byte-for-byte copy. Numeric parameters must be integers. `archived=true` selects only the archive, while `false` selects only the main list. This is the original's actual behavior despite its “include archived” description.

`read_messages`, `search_messages`, and `get_pinned` return selected messages in ascending ID order. `[album:...]` labels preserve the complete 64-bit ID as a string, and `[reply:...]` labels explicitly connect replies. `get_message` also shows views, forwards, reactions, and the reply count.

`fetch_since` returns the earliest messages after `sinceId` and a `maxId` for the next call. Pass the returned `maxId` to the next request until the result is empty. A large backlog is not skipped when `limit` is reached.

Media is downloaded as a stream, with checks for both declared size and bytes actually written. `get_thumbnail` selects a preview no larger than 320 pixels per side or an embedded thumbnail without downloading the original. Repeated downloads create separate files with `0600` permissions; incomplete files are removed on failure. Media from chats that prohibit saving (`no-forward`) is not downloaded. These two tools are marked in MCP as writing local files; all others are read-only. Sending messages, editing messages, and read acknowledgements are not supported.

## Architecture

```text
cmd/telegram-mcp         — dependency wiring, lifecycle, CLI setup
internal/config          — environment loading and validation
internal/domain          — messages, chats, folders, media; standard library only
internal/port            — outbound Telegram interface
internal/app             — use cases, validation, limits, formatting
internal/adapter/mcp     — inbound `Executor` port, MCP schemas, and stdio server
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
go build -o bin/telegram-mcp ./cmd/telegram-mcp
go test -race ./...
golangci-lint run ./...
```

Tests run without credentials or network access: the MCP client covers the handshake, all 12 tools, calls, validation, and errors. A fake MTProto transport covers paginated history, cursors with ID gaps, archived chats, and message-to-chat ownership. Media metadata, limits, and file cleanup are tested separately. A real login, the Telegram API, and downloads from a real account must be checked manually after `setup`.

## Origin

Functional reference: [22syn/telegram-user-mcp, commit 073e96a](https://github.com/22syn/telegram-user-mcp/tree/073e96a33f8ef3e04c60ab60882bf4304c40ab64). The original project is MIT © 2026 Kobi Hazout; its notice is preserved in [LICENSE](LICENSE).
