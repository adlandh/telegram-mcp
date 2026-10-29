# telegram-drafts Specification

## Purpose

Let MCP callers prepare a message for the user to review by saving it as a chat's cloud draft, without ever sending a message on the user's behalf.

## Requirements

### Requirement: Save a chat draft without sending

`save_draft` with `groupUrl` and `text` SHALL resolve the chat exactly like other chat-scoped tools and SHALL store `text` as plain text in that chat's cloud draft, replacing any existing draft. It SHALL NOT send, edit or delete messages, join chats, or change read state. Successful results SHALL state the chat's marked ID and that the draft was saved, not sent.

#### Scenario: Draft is saved

- **WHEN** a caller invokes `save_draft` with `groupUrl="@alice"` and `text="See you at 5"`
- **THEN** the chat's draft becomes "See you at 5" and no message is sent
- **AND** the result names the chat's marked ID and states the draft was saved, not sent

#### Scenario: Existing draft is replaced

- **WHEN** the chat already has a draft and a caller invokes `save_draft` with new text
- **THEN** the draft contains only the new text

#### Scenario: Text is not parsed as formatting

- **WHEN** `text` contains Markdown or HTML markup such as `**bold**`
- **THEN** the draft contains the characters literally, without formatting entities

#### Scenario: Read state is unchanged

- **WHEN** a caller saves a draft in a chat with unread messages
- **THEN** the chat's unread count and read cursor are unchanged

### Requirement: Draft failures are reported

Unresolvable chats, invite links, chats where Telegram refuses the draft, Telegram failures, and expired deadlines or cancellation SHALL return MCP tool errors. A failed call SHALL NOT be reported as saved.

#### Scenario: Invite link is rejected

- **WHEN** a caller passes an invite link to `save_draft`
- **THEN** the tool returns an error without joining the chat or saving a draft

#### Scenario: Telegram refuses the draft

- **WHEN** Telegram rejects the draft, for example in a broadcast channel where the account cannot post
- **THEN** the tool returns an MCP tool error rather than a success text
