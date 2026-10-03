// Package telegram implements the read-only application port using MTProto.
package telegram

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/adlandh/telegram-mcp/internal/domain"
	"github.com/adlandh/telegram-mcp/internal/port"
	gotd "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/tg"
)

type Client struct {
	api         *tg.Client
	downloadDir string
	mu          sync.Mutex
	peers       map[string]resolved // ponytail: never invalidated; restart clears it
}

var _ port.Telegram = (*Client)(nil)

func New(client *gotd.Client, downloadDir string) *Client {
	return &Client{api: client.API(), downloadDir: downloadDir}
}

type resolved struct {
	input     tg.InputPeerClass
	info      domain.Chat
	protected bool
}

func normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	for _, prefix := range []string{"t.me/", "www.t.me/", "telegram.me/", "www.telegram.me/"} {
		if strings.HasPrefix(lower, prefix) {
			raw = "https://" + raw
		}
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !slices.Contains([]string{"t.me", "telegram.me"}, strings.TrimPrefix(strings.ToLower(u.Host), "www.")) {
			return "", fmt.Errorf("expected a t.me URL, username, or numeric ID")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 2 && parts[0] == "c" {
			id, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil || id <= 0 || id >= 1000000000000 {
				return "", fmt.Errorf("invalid private channel ID")
			}
			return strconv.FormatInt(-1000000000000-id, 10), nil
		}
		if parts[0] == "s" && len(parts) >= 2 {
			parts = parts[1:]
		}
		raw = parts[0]
	}
	raw = strings.TrimPrefix(raw, "@")
	if raw == "" || strings.ContainsAny(raw, "/+ ?#") || raw == "joinchat" {
		return "", fmt.Errorf("use a chat username or ID; invite links do not join chats")
	}
	return raw, nil
}

func (c *Client) resolve(ctx context.Context, raw string) (resolved, error) {
	name, err := normalize(raw)
	if err != nil {
		return resolved{}, err
	}
	if id, err := strconv.ParseInt(name, 10, 64); err == nil {
		// Numeric peers need an access hash, including after restart.
		if r, ok := c.cached(name); ok {
			return r, nil
		}
		var fallback *resolved
		for _, folder := range []int{0, 1} {
			iter := query.GetDialogs(c.api).FolderID(folder).BatchSize(100).Iter()
			for iter.Next(ctx) {
				e := iter.Value()
				d, ok := e.Dialog.(*tg.Dialog)
				if !ok {
					continue
				}
				r := describe(d.Peer, e.Entities)
				r.input = e.Peer
				c.store(r.info.ID, r)
				if r.info.ID == name {
					return r, nil
				}
				if id > 0 && r.info.Type == "Channel" && r.info.ID == strconv.FormatInt(-1000000000000-id, 10) {
					fallback = &r
				}
			}
			if err := iter.Err(); err != nil {
				return resolved{}, fmt.Errorf("resolve dialogs: %w", err)
			}
		}
		if fallback != nil {
			c.store(name, *fallback)
			return *fallback, nil
		}
		return resolved{}, fmt.Errorf("chat ID not found in account dialogs; use list_dialogs or a username")
	}
	r, err := c.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: name})
	if err != nil {
		return resolved{}, fmt.Errorf("resolve username: %w", err)
	}
	entities := peer.EntitiesFromResult(r)
	info := describe(r.Peer, entities)
	info.input, err = entities.ExtractPeer(r.Peer)
	return info, err
}

func (c *Client) cached(key string) (resolved, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.peers[key]
	return r, ok
}

func (c *Client) store(key string, r resolved) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.peers == nil {
		c.peers = map[string]resolved{}
	}
	c.peers[key] = r
}

func describe(p tg.PeerClass, entities peer.Entities) resolved {
	r := resolved{}
	switch p := p.(type) {
	case *tg.PeerUser:
		r.info.ID, r.info.Type = strconv.FormatInt(p.UserID, 10), "User"
		if u, ok := entities.User(p.UserID); ok {
			r.info.Title, r.info.Username = strings.TrimSpace(u.FirstName+" "+u.LastName), u.Username
		}
	case *tg.PeerChat:
		r.info.ID, r.info.Type = strconv.FormatInt(-p.ChatID, 10), "Chat"
		if chat, ok := entities.Chat(p.ChatID); ok {
			r.info.Title, r.info.Members, r.protected = chat.Title, new(chat.ParticipantsCount), chat.Noforwards
		}
	case *tg.PeerChannel:
		r.info.ID, r.info.Type = strconv.FormatInt(-1000000000000-p.ChannelID, 10), "Channel"
		if channel, ok := entities.Channel(p.ChannelID); ok {
			r.info.Title, r.info.Username, r.protected = channel.Title, channel.Username, channel.Noforwards
			if n, ok := channel.GetParticipantsCount(); ok {
				r.info.Members = new(n)
			}
		}
	}
	if r.info.Username != "" {
		r.info.Username = "@" + r.info.Username
	}
	return r
}

func (c *Client) Dialogs(ctx context.Context, limit int, archived bool) ([]domain.Chat, error) {
	folder := 0
	if archived {
		folder = 1
	}
	iter := query.GetDialogs(c.api).FolderID(folder).BatchSize(min(limit, 100)).Iter()
	chats := make([]domain.Chat, 0, limit)
	for len(chats) < limit && iter.Next(ctx) {
		e := iter.Value()
		d, ok := e.Dialog.(*tg.Dialog)
		if !ok {
			continue
		}
		info := describe(d.Peer, e.Entities).info
		info.Unread = d.UnreadCount
		chats = append(chats, info)
	}
	return chats, iter.Err()
}

func (c *Client) Folders(ctx context.Context) ([]domain.Folder, error) {
	r, err := c.api.MessagesGetDialogFilters(ctx)
	if err != nil {
		return nil, err
	}
	var result []domain.Folder
	for _, f := range r.Filters {
		switch f := f.(type) {
		case *tg.DialogFilter:
			result = append(result, domain.Folder{ID: f.ID, Title: f.Title.Text, Count: len(f.PinnedPeers) + len(f.IncludePeers)})
		case *tg.DialogFilterChatlist:
			result = append(result, domain.Folder{ID: f.ID, Title: f.Title.Text, Count: len(f.PinnedPeers) + len(f.IncludePeers)})
		}
	}
	return result, nil
}

func (c *Client) Chat(ctx context.Context, name string) (domain.Chat, error) {
	r, err := c.resolve(ctx, name)
	if err != nil {
		return domain.Chat{}, err
	}
	var full *tg.MessagesChatFull
	switch p := r.input.(type) {
	case *tg.InputPeerChannel:
		full, err = c.api.ChannelsGetFullChannel(ctx, &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash})
	case *tg.InputPeerChat:
		full, err = c.api.MessagesGetFullChat(ctx, p.ChatID)
	case *tg.InputPeerUser:
		u, userErr := c.api.UsersGetFullUser(ctx, &tg.InputUser{UserID: p.UserID, AccessHash: p.AccessHash})
		if userErr != nil {
			return domain.Chat{}, userErr
		}
		r.info.About = u.FullUser.About
	}
	if err != nil {
		return domain.Chat{}, err
	}
	if full != nil {
		switch f := full.FullChat.(type) {
		case *tg.ChannelFull:
			r.info.About = f.About
			if n, ok := f.GetParticipantsCount(); ok {
				r.info.Members = new(n)
			}
		case *tg.ChatFull:
			r.info.About = f.About
			if p, ok := f.Participants.(*tg.ChatParticipants); ok {
				r.info.Members = new(len(p.Participants))
			}
		}
	}
	return r.info, nil
}
