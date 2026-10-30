// Package domain contains Telegram-independent application data.
package domain

import (
	"math"
	"time"
)

// MaxDownloadMB is the largest MB count whose byte size fits in int64.
const MaxDownloadMB = math.MaxInt64 / (1 << 20)

type Message struct {
	Date      time.Time
	Views     *int
	Replies   *int
	Forwards  *int
	Text      string
	ChatID    string
	AlbumID   string
	Chat      string
	Sender    string
	Media     Media
	ReplyTo   int
	Reactions int
	ID        int
}

type Media struct {
	Type, MIME, FileName string
	SizeBytes            int64
	Duration             float64
	Downloadable         bool
}

type MessageQuery struct {
	Chat, Search             string
	Limit, SinceID, BeforeID int
	Since, Pinned, Global    bool
	// Unread selects messages after the chat's read cursor, oldest first.
	Unread bool
}

type Chat struct {
	Members    *int
	ID         string
	Title      string
	Username   string
	Type       string
	About      string
	Unread     int
	UnreadMark bool
}

// HasUnread reports whether a chat has unread messages or a manual unread mark.
func (c Chat) HasUnread() bool { return c.Unread > 0 || c.UnreadMark }

type Folder struct {
	Title string
	ID    int
	Count int
}

type Download struct {
	Path  string
	Bytes int64
}

type Image struct {
	MIME string
	Data []byte
}
