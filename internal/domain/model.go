// Package domain contains Telegram-independent application data.
package domain

import "time"

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
	Chat, Search          string
	Limit, SinceID        int
	Since, Pinned, Global bool
}

type Chat struct {
	ID, Title, Username, Type, About string
	Members                          *int
	Unread                           int
}

type Folder struct {
	ID    int
	Title string
	Count int
}

type Download struct {
	Path  string
	Bytes int64
}
