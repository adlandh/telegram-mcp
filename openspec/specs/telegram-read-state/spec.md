# telegram-read-state Specification

## Purpose

Let MCP callers explicitly acknowledge Telegram messages as read for the authenticated account, either for a whole chat or up to a chosen message, without any other tool changing read state.

## Requirements

### Requirement: Mark a chat read up to a message

`mark_read` with `groupUrl` and `messageId` SHALL mark messages in the resolved chat as read up to and including `messageId`, using Telegram's per-chat read cursor. It SHALL NOT mark messages with larger IDs as read. Successful results SHALL state the chat's marked ID and the message ID used as the read cursor.

#### Scenario: Partial acknowledgement

- **WHEN** a chat has unread messages 10 through 15 and a caller invokes `mark_read` with `messageId=12`
- **THEN** messages up to 12 become read, messages 13 through 15 remain unread
- **AND** the result names the chat's marked ID and message 12

#### Scenario: Cursor is not moved backwards

- **WHEN** a caller invokes `mark_read` with a `messageId` lower than the chat's current read cursor
- **THEN** the call succeeds and no previously read message becomes unread

### Requirement: Mark a whole chat read

`mark_read` without `messageId` SHALL mark all messages of the resolved chat as read up to its latest message and SHALL clear the chat's manual "marked as unread" flag when set. A chat without messages SHALL return explanatory text rather than an error.

#### Scenario: Whole chat acknowledgement

- **WHEN** a caller invokes `mark_read` with only `groupUrl` for a chat whose latest message is 42 and which is manually marked unread
- **THEN** all messages up to 42 become read and the manual unread mark is cleared
- **AND** the result names the chat's marked ID and message 42

#### Scenario: Empty chat

- **WHEN** a caller invokes `mark_read` without `messageId` for a chat that has no messages
- **THEN** the tool returns explanatory text and reports no error

### Requirement: Read acknowledgement is explicit and scoped

Only `mark_read` SHALL change Telegram read state. It SHALL resolve `groupUrl` exactly like other chat-scoped tools, SHALL NOT join chats or send, edit or delete messages, and SHALL support users, basic groups, supergroups and broadcast channels. Telegram failures, unresolvable chats and cancellation SHALL return tool errors.

#### Scenario: Supergroup and channel use channel read state

- **WHEN** a caller marks a supergroup or broadcast channel as read
- **THEN** the channel's read cursor advances the same way as for private chats and basic groups

#### Scenario: Invite link is rejected

- **WHEN** a caller passes an invite link to `mark_read`
- **THEN** the tool returns an error without joining the chat or changing read state

#### Scenario: Read tools stay side-effect free

- **WHEN** a caller uses `read_messages`, `fetch_since`, `get_message` or any other tool except `mark_read`
- **THEN** no message is acknowledged as read

#### Scenario: Telegram failure is reported

- **WHEN** Telegram rejects the read acknowledgement or the request deadline expires
- **THEN** the tool returns an MCP tool error rather than a success text
