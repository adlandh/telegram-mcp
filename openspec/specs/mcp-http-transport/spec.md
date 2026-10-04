# mcp-http-transport Specification

## Purpose

Lets remote or containerized MCP clients reach the Telegram MCP server over an authenticated HTTP endpoint published through a TLS-terminating reverse proxy instead of a local stdio child process.

## Requirements

### Requirement: Opt-in HTTP transport

The command `telegram-mcp http` SHALL serve MCP over the Streamable HTTP transport at path `/mcp` on the address in `TELEGRAM_MCP_HTTP_ADDR` (`host:port`, default `127.0.0.1:8080`). Without arguments, the server SHALL serve stdio only, and HTTP settings SHALL be neither read for mode selection nor validated. Extra arguments after `http` SHALL be rejected. Both transports SHALL expose the same tools, validation, deadlines and read-only guarantees, and require the same authorized session.

#### Scenario: Stdio remains the default

- **WHEN** the server starts without arguments, even if `TELEGRAM_MCP_HTTP_ADDR` or `TELEGRAM_MCP_HTTP_TOKEN` is set or invalid
- **THEN** it serves MCP over stdin/stdout and opens no network listener

#### Scenario: HTTP command uses the default address

- **WHEN** `telegram-mcp http` runs with a valid token, no `TELEGRAM_MCP_HTTP_ADDR`, and an authorized session
- **THEN** it listens on `127.0.0.1:8080` and does not read MCP messages from stdin

#### Scenario: HTTP mode serves the tool catalog

- **WHEN** `telegram-mcp http` runs with a valid token and an authorized session
- **THEN** an authenticated MCP client connecting to `/mcp` can initialize and list the same 14 tools as over stdio

#### Scenario: Extra HTTP arguments are rejected

- **WHEN** the command is `telegram-mcp http extra`
- **THEN** it fails with an argument error before loading the session or opening a listener

#### Scenario: HTTP mode without a session directs setup

- **WHEN** `telegram-mcp http` runs but the session is missing or unauthorized
- **THEN** the server reports that setup must be run and does not open a listener

### Requirement: Bearer token authentication

The `http` command SHALL require `TELEGRAM_MCP_HTTP_TOKEN`, at least 32 characters after trimming whitespace. Every HTTP request SHALL carry `Authorization: Bearer <token>`. The comparison SHALL run in constant time. Requests with a missing, malformed or wrong token SHALL receive `401 Unauthorized` before any MCP message is processed or Telegram is contacted. The token SHALL NOT appear in diagnostics.

#### Scenario: Missing token configuration fails startup

- **WHEN** `telegram-mcp http` runs and `TELEGRAM_MCP_HTTP_TOKEN` is unset or shorter than 32 characters
- **THEN** the application reports a configuration error before loading the session and does not start serving

#### Scenario: Unauthenticated request is rejected

- **WHEN** a request to `/mcp` has no `Authorization` header, a non-bearer scheme, or a wrong token
- **THEN** the server responds `401` and no tool executes

#### Scenario: Authenticated request is served

- **WHEN** a request carries the configured bearer token
- **THEN** the server processes it as an MCP message

### Requirement: Loopback-only plaintext listener

The server SHALL serve plain HTTP only and SHALL NOT terminate TLS itself; HTTPS is the responsibility of a reverse proxy on the same host. The server SHALL refuse to start unless the listen host in `TELEGRAM_MCP_HTTP_ADDR` is a loopback IP literal or `localhost`. An empty host such as `:8080` counts as non-loopback. The server SHALL keep DNS-rebinding and cross-origin request protection enabled.

#### Scenario: Non-loopback address is refused

- **WHEN** `telegram-mcp http` runs and `TELEGRAM_MCP_HTTP_ADDR` is `0.0.0.0:8080`, `:8080` or a non-loopback IP
- **THEN** the application reports a configuration error that tells the operator to bind to loopback and publish through a reverse proxy

#### Scenario: Loopback address is accepted

- **WHEN** `telegram-mcp http` runs and `TELEGRAM_MCP_HTTP_ADDR` is `127.0.0.1:8080`, `[::1]:8080` or `localhost:8080`
- **THEN** the server serves plain HTTP on that address

#### Scenario: Proxied request with loopback Host is served

- **WHEN** a reverse proxy forwards an authenticated request and sets the `Host` header to the loopback upstream address
- **THEN** the server processes it as an MCP message

#### Scenario: Non-loopback Host header is rejected

- **WHEN** a request reaches the loopback listener with a non-loopback `Host` header, such as a public domain forwarded unchanged by a proxy or a DNS-rebinding attack
- **THEN** the server responds `403` without processing the MCP message

#### Scenario: Cross-origin browser request is rejected

- **WHEN** a request arrives with browser cross-site fetch metadata or a mismatched `Origin`
- **THEN** the server rejects it without processing the MCP message

### Requirement: HTTP lifecycle and output isolation

In HTTP mode, the server SHALL write diagnostics, including the listening address, only to stderr and SHALL leave stdout empty. On SIGINT or SIGTERM it SHALL stop accepting connections, cancel in-flight tool calls and exit. A listen failure, such as an address already in use, SHALL terminate startup with an error.

#### Scenario: Termination stops the HTTP server

- **WHEN** the process receives SIGTERM while serving HTTP
- **THEN** the listener closes, in-flight Telegram operations receive cancellation, and the process exits

#### Scenario: Listen failure is reported

- **WHEN** the configured address is already in use
- **THEN** the application exits with an error on stderr
