// Package port defines the application's external dependencies.
package port

import (
	"context"
	"github.com/adlandh/telegram-mcp/internal/domain"
)

// Telegram is a user-account port that is read-only except MarkRead, the explicit
// read acknowledgement. Downloads only write local files.
type Telegram interface {
	Messages(context.Context, domain.MessageQuery) ([]domain.Message, error)
	Message(context.Context, string, int) (domain.Message, error)
	Chat(context.Context, string) (domain.Chat, error)
	Dialogs(context.Context, int, bool) ([]domain.Chat, error)
	Folders(context.Context) ([]domain.Folder, error)
	FolderDialogs(context.Context, int, int) ([]domain.Chat, error)
	Download(context.Context, string, int, bool, int64) (domain.Download, error)
	// MarkRead marks a chat read up to messageID, or entirely when it is 0.
	// It returns the chat's marked ID and the read cursor, which is 0 for an empty chat.
	MarkRead(ctx context.Context, chat string, messageID int) (chatID string, upTo int, err error)
}
