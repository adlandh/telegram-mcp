# telegram-message-access Specification

## Purpose

Read and discover the Telegram conversations available to the authenticated user while preserving message identity and incremental polling continuity.

## Requirements

### Requirement: Resolve accessible chat identifiers

Chat-scoped tools SHALL accept usernames with or without `@`, `t.me` username links, public message links, `t.me/s/name` links, private `t.me/c/channel/message` links, and marked numeric IDs represented as strings. Numeric lookup SHALL include main and archived dialogs and obtain the account's access information. Positive IDs SHALL prefer a matching user and otherwise permit a matching channel fallback. Invite links SHALL fail without joining chats.

#### Scenario: Resolve an archived private channel after restart

- **WHEN** a caller provides the marked ID of an archived private channel after the application restarts
- **THEN** the server resolves it using the account's dialog access information

#### Scenario: Reject an invite link

- **WHEN** a caller provides an invite link
- **THEN** the server returns an error without joining the chat

### Requirement: Read and search bounded message sets

`read_messages` SHALL select recent messages, `search_messages` SHALL search the specified chat, `get_pinned` SHALL select pinned messages, and `global_search` SHALL search across chats visible to the account. Defaults and caps SHALL follow this table; positive limits above the cap SHALL be clamped. Chat-scoped results SHALL be presented in ascending message-ID order. Global results SHALL include the originating chat title and marked ID. Empty selections SHALL return explanatory text.

| Tool | Default limit | Maximum limit |
| --- | --- | --- |
| read_messages | 50 | 200 |
| search_messages | 30 | 100 |
| get_pinned | 20 | 50 |
| global_search | 30 | 100 |
| fetch_since | 100 | 200 |

#### Scenario: Read beyond a single Telegram page

- **WHEN** a caller requests a bounded set that spans more than one Telegram page
- **THEN** the server returns the requested effective limit in ascending message-ID order

#### Scenario: Search and pinned messages are bounded

- **WHEN** a caller requests chat search results or pinned messages above their maximum
- **THEN** the server clamps the limit and returns the matching messages in ascending message-ID order

#### Scenario: Global search includes chat context

- **WHEN** a global search returns messages from multiple chats
- **THEN** every result includes the originating chat title and marked ID

### Requirement: Preserve message metadata

Message output SHALL include message ID, UTC timestamp to the second, available sender identification, text or a no-text placeholder, and media type where present. Album IDs SHALL remain exact decimal strings and SHALL appear as album references; replies SHALL expose explicit reply-to message IDs. Service messages SHALL be represented rather than silently excluded. `get_message` SHALL additionally expose available views, forwards, total reaction count, reply count, media presence, album ID, and reply reference; unavailable optional counts SHALL be distinguished from known zero counts.

#### Scenario: Large album ID and reply are preserved

- **WHEN** a message has album ID `14285256815022453` and replies to message `11`
- **THEN** the output preserves the album ID as an exact decimal string and exposes the reply reference

#### Scenario: Message lookup is scoped to its chat

- **WHEN** a caller requests a message ID that belongs to another chat
- **THEN** `get_message` returns a descriptive failure

### Requirement: Incremental polling without backlog loss

`fetch_since` SHALL return up to its effective limit of the earliest available messages strictly newer than `sinceId`, ordered by ascending ID, with `maxId` equal to the largest returned ID. Repeated calls using that cursor SHALL not skip available messages merely because the backlog exceeds the limit or contains ID gaps or service messages. Empty results SHALL preserve `sinceId` as `maxId`. A pagination response that cannot advance SHALL terminate rather than loop indefinitely.

#### Scenario: Backlog exceeds one response

- **WHEN** 200 messages are available after a cursor and the effective limit is smaller
- **THEN** the first response returns the earliest available messages and its `maxId` can be used to receive the next messages without gaps

#### Scenario: Cursor excludes overlap

- **WHEN** a caller repeats polling with the previous `maxId`
- **THEN** the previously returned message is not included again

#### Scenario: No new messages preserve the cursor

- **WHEN** no messages are available after `sinceId`
- **THEN** the response has `maxId` equal to `sinceId`

### Requirement: Discover chats and folders

`list_dialogs` SHALL expose chat type, title, available username, unread count, and marked string ID, with default limit 100 and cap 500. `archived=false` or omission SHALL select the main dialog list; `archived=true` SHALL select only archived dialogs. `get_group_info` SHALL expose title, ID, type, available username, description, and member count when available. `list_folders` SHALL expose named custom folders with their explicitly included peer counts, excluding the built-in default filter.

#### Scenario: Archive selection is exclusive

- **WHEN** a caller sets `archived=true`
- **THEN** `list_dialogs` returns only archived dialogs

#### Scenario: Group details are available

- **WHEN** a caller requests information for an accessible group
- **THEN** the response includes its available details and marked ID

#### Scenario: No custom folders

- **WHEN** the account has no named custom folders
- **THEN** `list_folders` returns explanatory text rather than the built-in default filter
