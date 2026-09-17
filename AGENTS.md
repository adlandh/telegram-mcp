# Agent guidance

- Apply `cc-skills-golang:golang-how-to` for Go coding, review, debugging and setup.
- Write OpenSpec specs and planning artifacts in English (`openspec/config.yaml`).

## Commands

Run from the repository root with Go 1.26+ and golangci-lint v2:

- `task build` — builds `bin/telegram-mcp` from `./cmd/telegram-mcp`, not the root package.
- `task test` — runs `go test -race ./...`; tests need no Telegram credentials or network.
- `task lint` — runs `golangci-lint run ./...` with `.golangci.yml`.
- `task fmt` — runs `gofmt -w cmd internal`.
- Single package: `go test -race -count=1 ./internal/app`.
- Single test: `go test -race -count=1 ./internal/adapter/telegram -run '^TestHistoryPaginationDoesNotLoseBacklog$'`.
- MCP schemas, handshake and tool calls: `go test -race ./internal/adapter/mcp -run '^TestMCPContract$'`.
- Run `task test`, `task lint` and `task build` after meaningful changes.

## Hexagonal architecture

- Call flow: MCP adapter → `Executor` → `app.Service` → `port.Telegram` → Telegram adapter → MTProto.
- Core: `internal/domain` holds domain models; `internal/app` owns use cases, validation, limits and result formatting.
- Outbound port: `internal/port.Telegram` defines the operations the application needs; `internal/adapter/telegram` implements them with gotd/td and local file downloads.
- Inbound adapter: `internal/adapter/mcp` owns MCP schemas, stdio transport and the consumer-defined `Executor` interface implemented by `app.Service`.
- Keep `internal/domain`, `internal/port` and `internal/app` free of SDK and transport dependencies; the core must not read environment variables.
- `cmd/telegram-mcp` is the composition root: load settings from `internal/config`, construct adapters and service, and manage authorization and lifecycle. Inject dependencies through constructors.

## Boundaries and behavior

- This is a personal-account MTProto client using gotd/td and the official Go MCP SDK, not a Bot API service.
- Telegram tools are read-only. Never add sends, edits, joins or read acknowledgements as incidental behavior.
- `download_media` and `get_thumbnail` write local files and must retain their non-read-only MCP annotations.
- Preserve tool/parameter names, including camelCase keys such as `groupUrl` and `sinceId`. Numeric chat IDs are strings; numeric parameters are integers.
- `archived=true` selects only archived dialogs, not main plus archived.
- `fetch_since` must return the earliest messages after `sinceId` with a safe `maxId` cursor; do not skip backlog when the limit is reached.
- Preserve download byte limits, no-forward restrictions, `0600` file permissions and incomplete-file cleanup.

## Runtime gotchas

- Configuration comes exclusively from environment variables; `.env` is not loaded automatically. See `.env.example` and README setup instructions.
- Authorize with `./bin/telegram-mcp setup` in an interactive terminal; login codes and 2FA passwords are prompts, not configuration variables.
- Normal startup requires API ID/hash and a saved gotd session; `TELEGRAM_PHONE` is setup-only. GramJS string sessions are incompatible.
- Use the same OS user/session path for setup and the MCP client, with one server process per session. Relative paths resolve from the process working directory.
- stdout belongs to MCP. Diagnostics and setup prompts go to stderr.
- Offline tests do not validate real authorization or Telegram access; those require an explicit manual check after setup.
- Do not commit credentials, session files or downloaded Telegram content.
