# Development

- Apply `cc-skills-golang:golang-how-to` for Go coding, review, debugging and setup.
- Keep `internal/domain`, `internal/port` and `internal/app` free of SDK and transport dependencies.
- Wire dependencies explicitly in `cmd/telegram-mcp`; configuration comes from environment variables.
- Telegram tools are read-only. Never add sends, edits, joins or read acknowledgements as incidental behavior.
- stdout belongs to MCP. Diagnostics and setup prompts go to stderr.
- Run `task test`, `task lint` and `task build` after meaningful changes.
- Do not commit credentials, session files or downloaded Telegram content.
