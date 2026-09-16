# telegram-mcp-runtime Specification

## Purpose

Provide MCP clients with a locally configured, read-only Telegram user-account service using stdio and reusable authentication.

## Requirements

### Requirement: Stable MCP tool catalog

The server SHALL expose exactly the following tools and parameter names. Successful results SHALL contain text content. Tool execution failures SHALL be distinguishable from successful results through MCP error semantics.

| Tool | Required parameters | Optional parameters |
| --- | --- | --- |
| read_messages | groupUrl | limit |
| search_messages | groupUrl, query | limit |
| fetch_since | groupUrl, sinceId | limit |
| get_message | groupUrl, messageId | none |
| global_search | query | limit |
| get_group_info | groupUrl | none |
| list_dialogs | none | limit, archived |
| list_folders | none | none |
| get_pinned | groupUrl | limit |
| get_media_info | groupUrl, messageId | none |
| download_media | groupUrl, messageId | maxMB |
| get_thumbnail | groupUrl, messageId | none |

#### Scenario: Discover tools

- **WHEN** an MCP client initializes and requests the tool list
- **THEN** it receives the 12 tools and their input schemas
- **AND** only `download_media` and `get_thumbnail` advertise `readOnlyHint=false`, while all tools advertise `destructiveHint=false`

#### Scenario: Telegram request fails

- **WHEN** Telegram rejects a valid request
- **THEN** the corresponding tool returns an MCP tool error rather than a successful text result

### Requirement: Validate tool arguments

The server SHALL reject unknown properties, missing required parameters, incorrect types, non-integer numeric inputs, non-positive limits, and blank required chat identifiers or search queries. `messageId` SHALL be in `[1, 2147483647]`, `sinceId` in `[0, 2147483646]`, and `maxMB` SHALL be non-negative and representable as signed 64-bit bytes after multiplication by 1048576.

#### Scenario: Invalid arguments do not reach Telegram

- **WHEN** a caller provides `limit=1.5`, `limit=0`, or an unknown property
- **THEN** the server returns a validation error before calling Telegram

#### Scenario: Oversized media limit is rejected

- **WHEN** a caller provides `maxMB=8796093022208`
- **THEN** the server rejects the value before attempting a download

### Requirement: Environment-only configuration

The application SHALL read settings from environment variables and SHALL NOT automatically load a configuration file or `.env` file. `TELEGRAM_API_ID` SHALL be a positive signed 32-bit integer and `TELEGRAM_API_HASH` SHALL contain exactly 32 hexadecimal characters. `TELEGRAM_PHONE` SHALL be required for setup only.

Optional settings SHALL default to `TELEGRAM_SESSION_PATH=~/.telegram-mcp/session.json`, `TELEGRAM_DOWNLOAD_DIR=~/.telegram-mcp/downloads`, `TELEGRAM_MAX_DOWNLOAD_MB=200`, and `TELEGRAM_REQUEST_TIMEOUT=5m`. Download limits SHALL be non-negative whole MiB values fitting signed 64-bit bytes; zero disables the size limit. Timeouts SHALL be positive durations. Paths beginning with `~/` SHALL resolve under the user's home directory; relative paths SHALL resolve against the working directory.

#### Scenario: Defaults are applied

- **WHEN** optional environment variables are absent
- **THEN** the application uses a 200 MiB download limit and a 5 minute request timeout

#### Scenario: Invalid environment configuration fails safely

- **WHEN** a required variable is missing or an environment value is invalid
- **THEN** the application reports a configuration error without starting MCP serving

### Requirement: Interactive setup and reusable session

The setup command SHALL authenticate an existing Telegram user account, requesting a login code and a 2FA password when required, with hidden terminal input. It SHALL persist the session with owner-only 0600 file permissions and SHALL NOT persist login codes or 2FA passwords as configuration. Subsequent serving SHALL reuse the session without consuming MCP stdin for login prompts. Missing or unauthorized sessions SHALL produce an instruction to run setup. Automatic new-account registration SHALL NOT occur.

#### Scenario: First login creates a private session

- **WHEN** setup completes with a valid code and required 2FA password
- **THEN** it creates a reusable session file with 0600 permissions

#### Scenario: Noninteractive login requires an interactive terminal

- **WHEN** setup needs credentials but no interactive terminal is available
- **THEN** setup fails with an instruction to run it from a terminal

#### Scenario: Serving without authorization directs setup

- **WHEN** the server starts without an authorized session
- **THEN** it reports that setup must be run
- **AND** it does not consume MCP stdin for prompts

### Requirement: Stdio isolation and read-only account access

Normal invocation SHALL serve MCP over stdin/stdout. Diagnostics and setup prompts SHALL go to stderr. Help SHALL succeed without credentials. Tools SHALL NOT send, edit, delete, join chats, or acknowledge messages as read. Each tool call SHALL receive the configured request deadline and propagate cancellation to external operations; normal serving SHALL respond to process termination signals by canceling its lifecycle.

#### Scenario: Help and startup errors keep stdout clean

- **WHEN** help is requested or startup fails before MCP serving
- **THEN** stdout remains empty and diagnostics are written to stderr

#### Scenario: Deadline cancellation reaches Telegram

- **WHEN** a tool request exceeds its configured deadline or its MCP request is cancelled
- **THEN** the Telegram operation receives cancellation and the tool returns an error
