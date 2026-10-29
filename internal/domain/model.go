// Package domain contains Telegram-independent application data.
package domain

import (
	"math"
	"time"
)

// MaxDownloadMB is the largest MB count whose byte size fits in int64.
const MaxDownloadMB = math.MaxInt64 / (1 << 20)

type Message struct {
	ID                                  int
	Date                                time.Time
	Sender, Text, Chat, ChatID, AlbumID string
	ReplyTo, Reactions                  int
	Views, Forwards, Replies            *int
	Media                               Media
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
}

type Chat struct {
	ID, Title, Username, Type, About string
	Members                          *int
	Unread                           int
	// UnreadMark is Telegram's manual "marked as unread" flag.
	UnreadMark bool
}

// HasUnread reports whether a chat has unread messages or a manual unread mark.
func (c Chat) HasUnread() bool { return c.Unread > 0 || c.UnreadMark }

type Folder struct {
	ID    int
	Title string
	Count int
}

type Download struct {
	Path  string
	Bytes int64
}

type Image struct {
	Data []byte
	MIME string
}
