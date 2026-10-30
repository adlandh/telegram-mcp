// Package port defines the application's external dependencies.
package port

import (
	"context"

	"github.com/adlandh/telegram-mcp/internal/domain"
)

// Telegram is a user-account port that is read-only except MarkRead, the explicit
// read acknowledgement, and SaveDraft. Nothing sends, edits, deletes or joins.
// Downloads only write local files; Preview writes nothing.
type Telegram interface {
	Messages(context.Context, domain.MessageQuery) ([]domain.Message, error)
	Message(context.Context, string, int) (domain.Message, error)
	Chat(context.Context, string) (domain.Chat, error)
	// Self returns the authenticated account without its phone number.
	Self(context.Context) (domain.Chat, error)
	// Dialogs and FolderDialogs return only chats with HasUnread when unreadOnly is set;
	// the limit counts returned chats.
	Dialogs(ctx context.Context, limit int, archived, unreadOnly bool) ([]domain.Chat, error)
	Folders(context.Context) ([]domain.Folder, error)
	FolderDialogs(ctx context.Context, folderID, limit int, unreadOnly bool) ([]domain.Chat, error)
	Download(context.Context, string, int, bool, int64) (domain.Download, error)
	// Preview returns the get_thumbnail preview in memory instead of saving it.
	Preview(ctx context.Context, chat string, id int, maxBytes int64) (domain.Image, error)
	// MarkRead marks a chat read up to messageID, or entirely when it is 0.
	// It returns the chat's marked ID and the read cursor, which is 0 for an empty chat.
	MarkRead(ctx context.Context, chat string, messageID int) (chatID string, upTo int, err error)
	// SaveDraft replaces the chat's cloud draft with plain text and returns the chat's marked ID.
	SaveDraft(ctx context.Context, chat, text string) (chatID string, err error)
}
