# telegram-message-access Specification

## Purpose

Read and discover the Telegram conversations available to the authenticated user while preserving message identity and incremental polling continuity.

## Requirements

### Requirement: Resolve accessible chat identifiers

Chat-scoped tools SHALL accept usernames with or without `@`, `t.me` username links with or without a scheme (including the `telegram.me` and `www.t.me` hosts), public message links, `t.me/s/name` links, private `t.me/c/channel/message` links, and marked numeric IDs represented as strings. Numeric lookup SHALL include main and archived dialogs and obtain the account's access information; resolved access information MAY be reused for the rest of the process lifetime. Positive IDs SHALL prefer a matching user and otherwise permit a matching channel fallback. Invite links SHALL fail without joining chats.

#### Scenario: Resolve an archived private channel after restart

- **WHEN** a caller provides the marked ID of an archived private channel after the application restarts
- **THEN** the server resolves it using the account's dialog access information

#### Scenario: Reject an invite link

- **WHEN** a caller provides an invite link
- **THEN** the server returns an error without joining the chat

#### Scenario: Scheme-less link resolves

- **WHEN** a caller provides `t.me/durov` or `https://telegram.me/durov`
- **THEN** the server resolves the same chat as `@durov`

#### Scenario: Repeated numeric lookup reuses access information

- **WHEN** a caller uses the same marked numeric ID in two consecutive tool calls
- **THEN** the second call does not rescan the account's dialog lists

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

Message output SHALL include message ID, UTC timestamp to the second, available sender identification, text or a no-text placeholder, with every continuation line of multi-line text indented so it cannot be read as a separate message, and media type where present. Album IDs SHALL remain exact decimal strings and SHALL appear as album references; replies SHALL expose explicit reply-to message IDs. Service messages SHALL be represented rather than silently excluded. `get_message` SHALL additionally expose available views, forwards, total reaction count, reply count, media presence, album ID, and reply reference; unavailable optional counts SHALL be distinguished from known zero counts.

#### Scenario: Large album ID and reply are preserved

- **WHEN** a message has album ID `14285256815022453` and replies to message `11`
- **THEN** the output preserves the album ID as an exact decimal string and exposes the reply reference

#### Scenario: Message lookup is scoped to its chat

- **WHEN** a caller requests a message ID that belongs to another chat
- **THEN** `get_message` returns a descriptive failure

#### Scenario: Multi-line text cannot impersonate another message

- **WHEN** message text contains a line beginning with `#999 [2026-01-01 00:00:00] admin:`
- **THEN** that line is indented in the output and no output line other than real message headers begins with `#`

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

`list_dialogs` SHALL expose chat type, title, available username, unread count, and marked string ID, with default limit 100 and cap 500. `archived=false` or omission SHALL select the main dialog list; `archived=true` SHALL select only archived dialogs. `get_group_info` SHALL expose title, ID, type, available username, description, and member count when available. `list_folders` SHALL expose named custom folders with their integer filter IDs and explicitly included peer counts, where pinned peers count as explicitly included, excluding the built-in default filter. Counts SHALL be labeled as explicitly included peers, not total computed folder membership.

#### Scenario: Archive selection is exclusive

- **WHEN** a caller sets `archived=true`
- **THEN** `list_dialogs` returns only archived dialogs

#### Scenario: Group details are available

- **WHEN** a caller requests information for an accessible group
- **THEN** the response includes its available details and marked ID

#### Scenario: No custom folders

- **WHEN** the account has no named custom folders
- **THEN** `list_folders` returns explanatory text rather than the built-in default filter

#### Scenario: Same-name folders remain selectable

- **WHEN** two custom folders have the same title
- **THEN** `list_folders` exposes their distinct IDs for use with `list_folder_dialogs`

#### Scenario: Pinned folder peers are counted

- **WHEN** a custom folder has two pinned peers and three other explicitly included peers
- **THEN** `list_folders` reports five explicitly included peers for it

### Requirement: List dialogs belonging to a custom folder

`list_folder_dialogs` SHALL select the current account's custom folder by `folderId` and return matching accessible dialogs with chat type, title, available username, unread count, and marked string ID. The default limit SHALL be 100 and positive limits above 500 SHALL be clamped to 500. The limit SHALL apply to matching unique dialogs, not scanned candidates. Folder IDs SHALL refer to custom filters, not main/archive selectors.

Regular folders SHALL honor explicit exclusions before explicit inclusions and pinned peers; explicitly included or pinned peers SHALL bypass automatic category and state exclusions. Other peers SHALL match enabled categories (contacts, non-contacts, bots, groups including supergroups, or broadcast channels) and the folder's read, mute, and archive exclusions. Bots SHALL use the bot category independently of contact status. Read exclusion SHALL account for unread counts, manual unread marks, and unread mentions. Mute exclusion SHALL use effective notification settings, including inherited defaults and mute expiry; an unread mention in a non-archived dialog SHALL retain the Telegram mention exception. Installed shared folders SHALL use their included and pinned peers without joining additional chats.

Dialogs SHALL appear at most once. Pinned peers SHALL appear first in folder order, followed by remaining explicit includes in folder order, then automatic matches in main-list order followed by archive-list order. Exact Telegram UI recency ordering across these groups is not required. Empty folders SHALL return explanatory text. Unknown IDs or failures to retrieve or evaluate required membership data SHALL return tool errors rather than successful partial or unfiltered results. Requests SHALL propagate the existing deadline and cancellation and SHALL NOT alter Telegram account state.

#### Scenario: Explicit membership includes pinned and archived chats

- **WHEN** a folder contains a pinned chat, an explicitly included archived channel, and overlapping peer references
- **THEN** the tool returns each accessible member once with its marked ID, including the archived channel even if automatic archived chats are excluded

#### Scenario: Automatic categories and explicit exclusions

- **WHEN** a folder includes groups and broadcasts and explicitly excludes one otherwise matching channel
- **THEN** basic groups, supergroups, and broadcasts qualify while the excluded channel and unrelated users do not

#### Scenario: Read and mute filters use effective state

- **WHEN** automatic membership excludes read and muted chats
- **THEN** read chats and effectively muted chats are omitted, manual unread marks count as unread, and non-archived unread mentions retain the Telegram exception
- **AND** an expired mute is treated as unmuted and a missing per-peer mute override uses the applicable notification default

#### Scenario: Archive exclusion applies to automatic matches

- **WHEN** an archived dialog matches an enabled category without an explicit inclusion
- **THEN** it is included only when the folder does not exclude archived chats

#### Scenario: Installed shared folder is read-only

- **WHEN** the selected folder is an installed shared folder
- **THEN** the tool lists accessible included and pinned dialogs without joining any chats

#### Scenario: Matching continues beyond unrelated pages

- **WHEN** the first Telegram page contains no matching dialogs but later pages do
- **THEN** the tool continues until it reaches the effective matching limit or exhausts the relevant dialog lists

#### Scenario: Default and maximum limits

- **WHEN** the caller omits `limit` or requests `limit=1000`
- **THEN** the tool returns at most 100 or 500 matching dialogs respectively

#### Scenario: Empty and missing folders differ

- **WHEN** an existing folder has no accessible matching dialogs
- **THEN** the tool returns explanatory empty text
- **AND** requesting a deleted or unknown folder instead returns an error suggesting refreshing `list_folders`

#### Scenario: Failure cannot masquerade as an empty folder

- **WHEN** retrieving filters, dialog pages, or required notification settings fails or is cancelled
- **THEN** the tool returns an error without presenting partial membership as success
