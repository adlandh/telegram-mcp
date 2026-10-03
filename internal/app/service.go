// Package app implements use cases without depending on Telegram or MCP SDKs.
package app

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/adlandh/telegram-mcp/internal/domain"
	"github.com/adlandh/telegram-mcp/internal/port"
)

type Arguments struct {
	GroupURL  string `json:"groupUrl,omitempty"`
	Query     string `json:"query,omitempty"`
	Limit     *int   `json:"limit,omitempty"`
	SinceID   *int   `json:"sinceId,omitempty"`
	MessageID int    `json:"messageId,omitempty"`
	FolderID  int    `json:"folderId,omitempty"`
	Archived  bool   `json:"archived,omitempty"`
	MaxMB     *int64 `json:"maxMB,omitempty"`
}

type Service struct {
	telegram      port.Telegram
	maxDownloadMB int64
	timeout       time.Duration
}

func New(telegram port.Telegram, maxDownloadMB int64, timeout time.Duration) *Service {
	return &Service{telegram: telegram, maxDownloadMB: maxDownloadMB, timeout: timeout}
}

func (s *Service) Execute(ctx context.Context, name string, a Arguments) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	a.GroupURL, a.Query = strings.TrimSpace(a.GroupURL), strings.TrimSpace(a.Query)
	switch name {
	case "global_search", "list_dialogs", "list_folders", "list_folder_dialogs":
	case "read_messages", "search_messages", "get_group_info", "fetch_since", "get_message", "get_pinned", "get_media_info", "download_media", "get_thumbnail":
		if a.GroupURL == "" {
			return "", fmt.Errorf("groupUrl is required")
		}
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	if a.Limit != nil && *a.Limit <= 0 {
		return "", fmt.Errorf("limit must be positive")
	}
	if (name == "search_messages" || name == "global_search") && a.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	if name == "fetch_since" && (a.SinceID == nil || *a.SinceID < 0 || *a.SinceID >= 1<<31-1) {
		return "", fmt.Errorf("sinceId must be between 0 and 2147483646")
	}
	if name == "list_folder_dialogs" && (a.FolderID < 2 || a.FolderID > 1<<31-1) {
		return "", fmt.Errorf("folderId must be between 2 and 2147483647")
	}
	switch name {
	case "get_message", "get_media_info", "download_media", "get_thumbnail":
		if a.MessageID <= 0 || a.MessageID > 1<<31-1 {
			return "", fmt.Errorf("messageId must be a positive 32-bit integer")
		}
	}
	limit := func(fallback, maximum int) int {
		if a.Limit == nil {
			return fallback
		}
		return min(*a.Limit, maximum)
	}
	switch name {
	case "read_messages", "search_messages", "fetch_since", "get_pinned", "global_search":
		q := domain.MessageQuery{Chat: a.GroupURL, Limit: limit(50, 200)}
		switch name {
		case "search_messages", "global_search":
			q.Search, q.Global, q.Limit = a.Query, name == "global_search", limit(30, 100)
		case "fetch_since":
			q.Since, q.SinceID, q.Limit = true, *a.SinceID, limit(100, 200)
		case "get_pinned":
			q.Pinned, q.Limit = true, limit(20, 50)
		}
		msgs, err := s.telegram.Messages(ctx, q)
		if err != nil {
			return "", err
		}
		if !q.Global {
			slices.SortFunc(msgs, func(a, b domain.Message) int { return cmp.Compare(a.ID, b.ID) })
		}
		lines := make([]string, 0, len(msgs))
		maxID := q.SinceID
		for _, m := range msgs {
			line := formatMessage(m)
			if q.Global {
				line = fmt.Sprintf("(%s [id:%s]) %s", m.Chat, m.ChatID, line)
			}
			lines = append(lines, line)
			maxID = max(maxID, m.ID)
		}
		body := strings.Join(lines, "\n")
		switch name {
		case "search_messages", "global_search":
			return fmt.Sprintf("Found %d messages for %q:\n%s", len(msgs), a.Query, cmp.Or(body, "(none)")), nil
		case "fetch_since":
			return fmt.Sprintf("New messages (maxId=%d):\n%s", maxID, cmp.Or(body, "(no new messages)")), nil
		case "get_pinned":
			return cmp.Or(body, "(no pinned messages)"), nil
		default:
			return cmp.Or(body, "(no messages)"), nil
		}
	case "get_message", "get_media_info":
		m, err := s.telegram.Message(ctx, a.GroupURL, a.MessageID)
		if err != nil {
			return "", err
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
	case "get_group_info":
		c, err := s.telegram.Chat(ctx, a.GroupURL)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Title: %s\nID: %s\nUsername: %s\nMembers: %s\nType: %s\nAbout: %s", c.Title, c.ID, c.Username, optional(c.Members), c.Type, c.About), nil
	case "list_dialogs":
		chats, err := s.telegram.Dialogs(ctx, limit(100, 500), a.Archived)
		if err != nil {
			return "", err
		}
		return formatDialogs(chats), nil
	case "list_folder_dialogs":
		chats, err := s.telegram.FolderDialogs(ctx, a.FolderID, limit(100, 500))
		if err != nil {
			return "", err
		}
		if len(chats) == 0 {
			return "(no dialogs in folder)", nil
		}
		return formatDialogs(chats), nil
	case "list_folders":
		folders, err := s.telegram.Folders(ctx)
		if err != nil {
			return "", err
		}
		var lines []string
		for _, f := range folders {
			lines = append(lines, fmt.Sprintf("%s — %d explicitly included chats [folderId:%d]", f.Title, f.Count, f.ID))
		}
		return cmp.Or(strings.Join(lines, "\n"), "(no folders defined)"), nil
	case "download_media", "get_thumbnail":
		if a.MaxMB != nil && (*a.MaxMB < 0 || *a.MaxMB > (1<<63-1)/(1024*1024)) {
			return "", fmt.Errorf("maxMB must be non-negative and fit into int64 bytes")
		}
		// The configured limit is a ceiling: callers may only lower it; 0 keeps it.
		capMB := s.maxDownloadMB
		if a.MaxMB != nil && *a.MaxMB > 0 && (capMB == 0 || *a.MaxMB < capMB) {
			capMB = *a.MaxMB
		}
		file, err := s.telegram.Download(ctx, a.GroupURL, a.MessageID, name == "get_thumbnail", capMB*1024*1024)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Saved: %s\nSize: %.2f MB", file.Path, float64(file.Bytes)/(1024*1024)), nil
	}
	return "", fmt.Errorf("unknown tool: %s", name)
}

func optional(n *int) string {
	if n == nil {
		return "N/A"
	}
	return fmt.Sprint(*n)
}

func formatDialogs(chats []domain.Chat) string {
	lines := []string{fmt.Sprintf("%d dialogs:", len(chats))}
	for _, c := range chats {
		lines = append(lines, fmt.Sprintf("%s: %s %s (%d unread) [id:%s]", c.Type, c.Title, c.Username, c.Unread, c.ID))
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
