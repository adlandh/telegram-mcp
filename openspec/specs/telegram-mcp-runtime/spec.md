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

Before requesting a code, setup SHALL describe Telegram's reported delivery method on stderr, distinguishing an in-app service notification, SMS (including word or phrase), voice call, and other delivery types. It SHALL NOT equate a successful code request with confirmed receipt. Delivery types requiring an unimplemented authentication flow SHALL produce an actionable explanation instead of a generic code prompt. Sensitive response payloads, code hashes, login codes, passwords, and full phone numbers SHALL NOT appear in diagnostics.

At a supported code prompt, setup SHALL accept `resend` as an explicit recovery action. It SHALL request another code only when Telegram supplies a next delivery method and the server-provided timeout has elapsed. An early request SHALL report the remaining wait without issuing a resend RPC; an absent timeout SHALL impose no additional local delay. Without a next method, setup SHALL explain that retry is unavailable and SHALL NOT request another code. Each successful resend SHALL replace the active delivery metadata and code hash; subsequent login SHALL use the newest hash. Setup SHALL NOT force SMS delivery or automatically loop on errors. Telegram rate limits and unsupported responses SHALL terminate the attempt with an actionable error and no false success message.

Setup SHALL also provide a code-free `setup qr` alternative that renders a QR login token on stderr for confirmation in the phone app's device-scan flow. QR login SHALL require no phone number and SHALL persist the same reusable session with identical permissions and success behavior. An expired token SHALL refresh with a new rendering while setup keeps waiting. QR export or import failures, including rate limits, SHALL terminate the attempt with an actionable error and no session-saved message. All diagnostics SHALL stay on stderr and stdout SHALL remain empty.

#### Scenario: First login creates a private session

- **WHEN** setup completes with a valid code and required 2FA password, or with a confirmed QR scan
- **THEN** it creates a reusable session file with 0600 permissions

#### Scenario: Noninteractive login requires an interactive terminal

- **WHEN** setup needs credentials but no interactive terminal is available
- **THEN** setup fails with an instruction to run it from a terminal

#### Scenario: Serving without authorization directs setup

- **WHEN** the server starts without an authorized session
- **THEN** it reports that setup must be run
- **AND** it does not consume MCP stdin for prompts

#### Scenario: Delivery channel is visible

- **WHEN** Telegram reports in-app, SMS, or voice-call delivery
- **THEN** setup identifies that channel on stderr before hidden code input
- **AND** it does not claim that the user has received the code

#### Scenario: Unsupported delivery does not wait for an unusable code

- **WHEN** Telegram requests an unsupported delivery flow or returns an unknown delivery type
- **THEN** setup explains the limitation and suggests checking the account in an official Telegram client
- **AND** it does not display a generic login-code prompt or dump the response payload

#### Scenario: Retry waits for Telegram eligibility

- **WHEN** the user enters `resend` before the returned timeout expires
- **THEN** setup reports the remaining wait and allows further input without requesting another code

#### Scenario: Retry uses current authorization state

- **WHEN** the user enters `resend` after the timeout and Telegram supplies a next method
- **THEN** setup requests another code using the current phone and code hash
- **AND** it displays the new delivery method and uses the new hash for subsequent login or resend

#### Scenario: No fallback is available

- **WHEN** the user enters `resend` and Telegram supplied no next delivery method
- **THEN** setup explains that Telegram offers no resend path for this attempt
- **AND** it allows code input or exit without issuing a resend RPC

#### Scenario: Rate limits do not cause repeated requests

- **WHEN** Telegram rejects an initial or repeated code request with a rate limit
- **THEN** setup reports the retry delay when supplied and exits without automatic retries or a session-saved message

#### Scenario: QR login needs no code or phone

- **WHEN** setup runs as `setup qr`
- **THEN** it renders the QR login token on stderr without requiring `TELEGRAM_PHONE`
- **AND** a confirmed scan creates the same reusable 0600 session as the code flow

#### Scenario: Expired QR token refreshes

- **WHEN** the displayed QR token expires before confirmation
- **THEN** setup renders the replacement token and keeps waiting

#### Scenario: QR failure is explicit

- **WHEN** QR export or import fails, including rate limits
- **THEN** setup reports an actionable error without a session-saved message

#### Scenario: Authorization succeeds without another code

- **WHEN** Telegram returns successful existing-account authorization from an initial or repeated code request
- **THEN** setup completes without requesting another login code
- **AND** a sign-up-required response is rejected without registering an account

### Requirement: Stdio isolation and read-only account access

Normal invocation SHALL serve MCP over stdin/stdout. Diagnostics and setup prompts SHALL go to stderr. Help SHALL succeed without credentials. Tools SHALL NOT send, edit, delete, join chats, or acknowledge messages as read. Each tool call SHALL receive the configured request deadline and propagate cancellation to external operations; normal serving SHALL respond to process termination signals by canceling its lifecycle.

#### Scenario: Help and startup errors keep stdout clean

- **WHEN** help is requested or startup fails before MCP serving
- **THEN** stdout remains empty and diagnostics are written to stderr

#### Scenario: Deadline cancellation reaches Telegram

- **WHEN** a tool request exceeds its configured deadline or its MCP request is cancelled
- **THEN** the Telegram operation receives cancellation and the tool returns an error
