// Package app implements use cases without depending on Telegram or MCP SDKs.
package app

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/adlandh/telegram-mcp/internal/domain"
	"github.com/adlandh/telegram-mcp/internal/port"
)

type Arguments struct {
	Limit        *int   `json:"limit,omitempty"`
	SinceID      *int   `json:"sinceId,omitempty"`
	MaxMB        *int64 `json:"maxMB,omitempty"`
	GroupURL     string `json:"groupUrl,omitempty"`
	Query        string `json:"query,omitempty"`
	Text         string `json:"text,omitempty"`
	BeforeID     int    `json:"beforeId,omitempty"`
	MessageID    int    `json:"messageId,omitempty"`
	FolderID     int    `json:"folderId,omitempty"`
	MaxTextChars int    `json:"maxTextChars,omitempty"`
	Archived     bool   `json:"archived,omitempty"`
	UnreadOnly   bool   `json:"unreadOnly,omitempty"`
}

type Service struct {
	telegram      port.Telegram
	maxDownloadMB int64
	timeout       time.Duration
}

func New(telegram port.Telegram, maxDownloadMB int64, timeout time.Duration) *Service {
	return &Service{telegram: telegram, maxDownloadMB: maxDownloadMB, timeout: timeout}
}

// tool binds a tool name to its handler and argument checks.
type tool struct {
	run   func(s *Service, ctx context.Context, name string, a Arguments) (string, error)
	check func(a *Arguments) error
	chat  bool // groupUrl is required
}

// tools lists every tool the service validates; view_thumbnail runs through ViewThumbnail.
var tools = map[string]tool{
	"read_messages":       {run: (*Service).messages, check: checkUnread, chat: true},
	"search_messages":     {run: (*Service).messages, check: checkQuery, chat: true},
	"fetch_since":         {run: (*Service).messages, check: checkSinceID, chat: true},
	"get_pinned":          {run: (*Service).messages, chat: true},
	"global_search":       {run: (*Service).messages, check: checkQuery},
	"get_message":         {run: (*Service).message, check: checkMessageID(1), chat: true},
	"get_media_info":      {run: (*Service).message, check: checkMessageID(1), chat: true},
	"get_group_info":      {run: (*Service).chat, chat: true},
	"get_me":              {run: (*Service).self},
	"list_dialogs":        {run: (*Service).dialogs},
	"list_folder_dialogs": {run: (*Service).folderDialogs, check: checkFolderID},
	"list_folders":        {run: (*Service).folders},
	"mark_read":           {run: (*Service).markRead, check: checkMessageID(0), chat: true}, // 0 marks the whole chat
	"save_draft":          {run: (*Service).saveDraft, check: checkText, chat: true},
	"download_media":      {run: (*Service).download, check: checkMessageID(1), chat: true},
	"get_thumbnail":       {run: (*Service).download, check: checkMessageID(1), chat: true},
	"view_thumbnail":      {check: checkMessageID(1), chat: true},
}

func (s *Service) Execute(ctx context.Context, name string, a Arguments) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	if err := validate(name, &a); err != nil {
		return "", err
	}

	t := tools[name]
	if t.run == nil {
		return "", fmt.Errorf("unknown tool: %s", name)
	}

	return t.run(s, ctx, name, a)
}

func limit(a Arguments, fallback, maximum int) int {
	if a.Limit == nil {
		return fallback
	}

	return min(*a.Limit, maximum)
}

func messageQuery(name string, a Arguments) domain.MessageQuery {
	q := domain.MessageQuery{Chat: a.GroupURL, Limit: limit(a, 50, 200)}

	switch name {
	case "read_messages":
		q.BeforeID, q.Unread = a.BeforeID, a.UnreadOnly
	case "search_messages":
		q.Search, q.BeforeID, q.Limit = a.Query, a.BeforeID, limit(a, 30, 100)
	case "global_search":
		q.Search, q.Global, q.Limit = a.Query, true, limit(a, 30, 100)
	case "fetch_since":
		q.Since, q.SinceID, q.Limit = true, *a.SinceID, limit(a, 100, 200)
	case "get_pinned":
		q.Pinned, q.Limit = true, limit(a, 20, 50)
	}

	return q
}

func (s *Service) messages(ctx context.Context, name string, a Arguments) (string, error) {
	q := messageQuery(name, a)

	msgs, err := s.telegram.Messages(ctx, q)
	if err != nil {
		return "", fmt.Errorf("get messages: %w", err)
	}

	if !q.Global {
		slices.SortFunc(msgs, func(a, b domain.Message) int { return cmp.Compare(a.ID, b.ID) })
	}

	body, maxID := formatMessages(msgs, q, a.MaxTextChars)
	// Chat-scoped results are ascending, so msgs[0] is the oldest: the next backward cursor.
	cursor := ""
	if len(msgs) > 0 && ((name == "read_messages" && !q.Unread) || name == "search_messages") {
		cursor = fmt.Sprintf(" (nextBeforeId=%d)", msgs[0].ID)
	}

	switch name {
	case "search_messages", "global_search":
		return fmt.Sprintf("Found %d messages for %q%s:\n%s", len(msgs), a.Query, cursor, cmp.Or(body, "(none)")), nil
	case "fetch_since":
		return fmt.Sprintf("New messages (maxId=%d):\n%s", maxID, cmp.Or(body, "(no new messages)")), nil
	case "get_pinned":
		return cmp.Or(body, "(no pinned messages)"), nil
	}

	return history(q, len(msgs), body, cursor, maxID), nil
}

// formatMessages renders msgs one per line and returns the highest message ID seen.
func formatMessages(msgs []domain.Message, q domain.MessageQuery, maxTextChars int) (string, int) {
	lines := make([]string, 0, len(msgs))
	maxID := q.SinceID

	for _, m := range msgs {
		if maxTextChars > 0 && utf8.RuneCountInString(m.Text) > maxTextChars {
			m.Text = string([]rune(m.Text)[:maxTextChars]) + " [truncated]"
		}

		line := formatMessage(m)
		if q.Global {
			line = fmt.Sprintf("(%s [id:%s]) %s", m.Chat, m.ChatID, line)
		}

		lines = append(lines, line)
		maxID = max(maxID, m.ID)
	}

	return strings.Join(lines, "\n"), maxID
}

// history renders a read_messages result.
func history(q domain.MessageQuery, count int, body, cursor string, maxID int) string {
	switch {
	case q.Unread && count == 0:
		return "(no unread messages)"
	case q.Unread:
		return fmt.Sprintf("Unread messages (maxId=%d):\n%s", maxID, body)
	case count == 0 && q.BeforeID > 0:
		return "(no older messages)"
	case count == 0:
		return "(no messages)"
	}

	return "Messages" + cursor + ":\n" + body
}

func (s *Service) message(ctx context.Context, name string, a Arguments) (string, error) {
	m, err := s.telegram.Message(ctx, a.GroupURL, a.MessageID)
	if err != nil {
		return "", fmt.Errorf("get message: %w", err)
	}

	if name == "get_media_info" {
		if m.Media.Type == "none" {
			return "(message has no media)", nil
		}

		return fmt.Sprintf("Type: %s\nMime: %s\nSize: %.2f MB\nDuration: %gs\nFile: %s\nDownloadable: %t", m.Media.Type, m.Media.MIME, float64(m.Media.SizeBytes)/(1024*1024), m.Media.Duration, m.Media.FileName, m.Media.Downloadable), nil
	}

	reply := "none"
	if m.ReplyTo != 0 {
		reply = fmt.Sprint(m.ReplyTo)
	}

	return fmt.Sprintf("%s\n— views:%s forwards:%s reactions:%d replies:%s media:%t album:%s reply:%s", formatMessage(m), optional(m.Views), optional(m.Forwards), m.Reactions, optional(m.Replies), m.Media.Type != "none", cmp.Or(m.AlbumID, "none"), reply), nil
}

func (s *Service) chat(ctx context.Context, _ string, a Arguments) (string, error) {
	c, err := s.telegram.Chat(ctx, a.GroupURL)
	if err != nil {
		return "", fmt.Errorf("get chat: %w", err)
	}

	return fmt.Sprintf("Title: %s\nID: %s\nUsername: %s\nMembers: %s\nType: %s\nAbout: %s", c.Title, c.ID, c.Username, optional(c.Members), c.Type, c.About), nil
}

func (s *Service) self(ctx context.Context, _ string, _ Arguments) (string, error) {
	c, err := s.telegram.Self(ctx)
	if err != nil {
		return "", fmt.Errorf("get account: %w", err)
	}

	return fmt.Sprintf("ID: %s\nName: %s\nUsername: %s", c.ID, c.Title, cmp.Or(c.Username, "N/A")), nil
}

func (s *Service) dialogs(ctx context.Context, _ string, a Arguments) (string, error) {
	chats, err := s.telegram.Dialogs(ctx, limit(a, 100, 500), a.Archived, a.UnreadOnly)
	if err != nil {
		return "", fmt.Errorf("list dialogs: %w", err)
	}

	return formatDialogs(chats), nil
}

func (s *Service) folderDialogs(ctx context.Context, _ string, a Arguments) (string, error) {
	chats, err := s.telegram.FolderDialogs(ctx, a.FolderID, limit(a, 100, 500), a.UnreadOnly)
	if err != nil {
		return "", fmt.Errorf("list folder dialogs: %w", err)
	}

	if len(chats) == 0 {
		return "(no dialogs in folder)", nil
	}

	return formatDialogs(chats), nil
}

func (s *Service) folders(ctx context.Context, _ string, _ Arguments) (string, error) {
	folders, err := s.telegram.Folders(ctx)
	if err != nil {
		return "", fmt.Errorf("list folders: %w", err)
	}

	lines := make([]string, 0, len(folders))
	for _, f := range folders {
		lines = append(lines, fmt.Sprintf("%s — %d explicitly included chats [folderId:%d]", f.Title, f.Count, f.ID))
	}

	return cmp.Or(strings.Join(lines, "\n"), "(no folders defined)"), nil
}

func (s *Service) markRead(ctx context.Context, _ string, a Arguments) (string, error) {
	chatID, upTo, err := s.telegram.MarkRead(ctx, a.GroupURL, a.MessageID)
	if err != nil {
		return "", fmt.Errorf("mark read: %w", err)
	}

	if upTo == 0 {
		return fmt.Sprintf("(no messages to mark as read) [id:%s]", chatID), nil
	}

	return fmt.Sprintf("Marked as read up to message #%d [id:%s]", upTo, chatID), nil
}

func (s *Service) saveDraft(ctx context.Context, _ string, a Arguments) (string, error) {
	chatID, err := s.telegram.SaveDraft(ctx, a.GroupURL, a.Text)
	if err != nil {
		return "", fmt.Errorf("save draft: %w", err)
	}

	return fmt.Sprintf("Draft saved (not sent) [id:%s]", chatID), nil
}

func (s *Service) download(ctx context.Context, name string, a Arguments) (string, error) {
	if a.MaxMB != nil && (*a.MaxMB < 0 || *a.MaxMB > domain.MaxDownloadMB) {
		return "", fmt.Errorf("maxMB must be non-negative and fit into int64 bytes")
	}
	// The configured limit is a ceiling: callers may only lower it; 0 keeps it.
	capMB := s.maxDownloadMB
	if a.MaxMB != nil && *a.MaxMB > 0 && (capMB == 0 || *a.MaxMB < capMB) {
		capMB = *a.MaxMB
	}

	file, err := s.telegram.Download(ctx, a.GroupURL, a.MessageID, name == "get_thumbnail", capMB*1024*1024)
	if err != nil {
		return "", fmt.Errorf("download media: %w", err)
	}

	return fmt.Sprintf("Saved: %s\nSize: %.2f MB", file.Path, float64(file.Bytes)/(1024*1024)), nil
}

// ViewThumbnail returns the get_thumbnail preview in memory without writing a file.
func (s *Service) ViewThumbnail(ctx context.Context, a Arguments) (domain.Image, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	if err := validate("view_thumbnail", &a); err != nil {
		return domain.Image{}, err
	}

	img, err := s.telegram.Preview(ctx, a.GroupURL, a.MessageID, s.maxDownloadMB*1024*1024)
	if err != nil {
		return domain.Image{}, fmt.Errorf("view thumbnail: %w", err)
	}

	return img, nil
}

// validate trims a's text fields in place and checks them for the named tool.
func validate(name string, a *Arguments) error {
	a.GroupURL, a.Query = strings.TrimSpace(a.GroupURL), strings.TrimSpace(a.Query)

	t, ok := tools[name]
	if !ok {
		return fmt.Errorf("unknown tool: %s", name)
	}

	if t.chat && a.GroupURL == "" {
		return fmt.Errorf("groupUrl is required")
	}

	if a.Limit != nil && *a.Limit <= 0 {
		return fmt.Errorf("limit must be positive")
	}

	if a.MaxTextChars < 0 {
		return fmt.Errorf("maxTextChars must be positive")
	}
	// 0 means beforeId was omitted: read from the latest message.
	if a.BeforeID < 0 || a.BeforeID > math.MaxInt32 {
		return fmt.Errorf("beforeId must be between 1 and 2147483647")
	}

	if t.check != nil {
		return t.check(a)
	}

	return nil
}

func checkText(a *Arguments) error {
	// Drafts keep surrounding whitespace; only blank text is rejected.
	if strings.TrimSpace(a.Text) == "" || utf8.RuneCountInString(a.Text) > 4096 {
		return fmt.Errorf("text must be non-blank and at most 4096 characters")
	}

	return nil
}

func checkQuery(a *Arguments) error {
	if a.Query == "" {
		return fmt.Errorf("query is required")
	}

	return nil
}

func checkSinceID(a *Arguments) error {
	if a.SinceID == nil || *a.SinceID < 0 || *a.SinceID >= math.MaxInt32 {
		return fmt.Errorf("sinceId must be between 0 and 2147483646")
	}

	return nil
}

func checkUnread(a *Arguments) error {
	if a.UnreadOnly && a.BeforeID > 0 {
		return fmt.Errorf("beforeId cannot be combined with unreadOnly")
	}

	return nil
}

func checkFolderID(a *Arguments) error {
	if a.FolderID < 2 || a.FolderID > math.MaxInt32 {
		return fmt.Errorf("folderId must be between 2 and 2147483647")
	}

	return nil
}

// checkMessageID requires messageId in [minID, MaxInt32].
func checkMessageID(minID int) func(*Arguments) error {
	return func(a *Arguments) error {
		if a.MessageID < minID || a.MessageID > math.MaxInt32 {
			return fmt.Errorf("messageId must be a positive 32-bit integer")
		}

		return nil
	}
}

func optional(n *int) string {
	if n == nil {
		return "N/A"
	}

	return fmt.Sprint(*n)
}

func formatDialogs(chats []domain.Chat) string {
	lines := make([]string, 0, 1+len(chats))

	lines = append(lines, fmt.Sprintf("%d dialogs:", len(chats)))
	for _, c := range chats {
		mark := ""
		if c.UnreadMark {
			mark = ", marked unread"
		}

		lines = append(lines, fmt.Sprintf("%s: %s %s (%d unread%s) [id:%s]", c.Type, c.Title, c.Username, c.Unread, mark, c.ID))
	}

	return strings.Join(lines, "\n")
}

var continuation = strings.NewReplacer("\r\n", "\n  ", "\n", "\n  ", "\r", "\n  ")

func formatMessage(m domain.Message) string {
	media, reply, album := "", "", ""
	if m.Media.Type != "" && m.Media.Type != "none" {
		media = " [" + m.Media.Type + "]"
	}

	if m.ReplyTo != 0 {
		reply = fmt.Sprintf(" [reply:%d]", m.ReplyTo)
	}

	if m.AlbumID != "" {
		album = " [album:" + m.AlbumID + "]"
	}
	// Indent continuation lines so message text cannot fake another "#id" line.
	text := continuation.Replace(cmp.Or(m.Text, "[no text]"))

	return fmt.Sprintf("#%d [%s] %s:%s %s%s%s", m.ID, m.Date.UTC().Format("2006-01-02 15:04:05"), cmp.Or(m.Sender, "Unknown"), media, text, reply, album)
}
