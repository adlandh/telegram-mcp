package telegram

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/adlandh/telegram-mcp/internal/domain"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
)

func (c *Client) Messages(ctx context.Context, q domain.MessageQuery) ([]domain.Message, error) {
	input, err := c.messagesPeer(ctx, &q)
	if err != nil {
		return nil, err
	}

	p := &pager{q: q, input: input, offset: q.BeforeID, result: make([]domain.Message, 0, q.Limit)}
	if q.Since {
		p.offset = q.SinceID + 1
	}

	for len(p.result) < q.Limit {
		more, err := c.page(ctx, p)
		if err != nil {
			return nil, err
		}

		if !more {
			break
		}
	}

	return p.result, nil
}

// messagesPeer resolves q's chat and, for unread reads, starts q at the chat's read cursor.
func (c *Client) messagesPeer(ctx context.Context, q *domain.MessageQuery) (tg.InputPeerClass, error) {
	var input tg.InputPeerClass

	if !q.Global {
		r, err := c.resolve(ctx, q.Chat)
		if err != nil {
			return nil, err
		}

		input = r.input
	}
	// Unread messages are the forward read from the chat's incoming read cursor.
	if q.Unread {
		d, err := c.dialog(ctx, input)
		if err != nil {
			return nil, err
		}

		if d == nil {
			return nil, fmt.Errorf("read state unavailable: the account has no dialog with this chat")
		}

		q.Since, q.SinceID = true, d.ReadInboxMaxID
	}

	return input, nil
}

// pager walks one Messages query page by page.
type pager struct {
	input  tg.InputPeerClass
	result []domain.Message
	q      domain.MessageQuery
	offset int
}

// page appends the next page to p.result and reports whether another page may follow.
func (c *Client) page(ctx context.Context, p *pager) (bool, error) {
	batch := min(100, p.q.Limit-len(p.result))
	pageMin := p.offset - 1

	modified, err := c.fetchPage(ctx, p, batch, pageMin)
	if err != nil {
		return false, err
	}

	raw := modified.GetMessages()
	if len(raw) == 0 {
		return false, nil
	}

	if !p.q.Global {
		sortPage(raw, p.q.Since)
	}

	previous := p.offset
	p.collect(raw, messageEntities(modified), pageMin)

	if p.q.Global || len(raw) < batch {
		return false, nil
	}

	if p.offset == previous {
		return false, fmt.Errorf("telegram pagination made no progress")
	}

	return true, nil
}

func (c *Client) fetchPage(ctx context.Context, p *pager, batch, pageMin int) (tg.ModifiedMessagesMessages, error) {
	var (
		q        = p.q
		response tg.MessagesMessagesClass
		err      error
	)

	switch {
	case q.Global:
		response, err = c.api.MessagesSearchGlobal(ctx, &tg.MessagesSearchGlobalRequest{Q: q.Search, Filter: &tg.InputMessagesFilterEmpty{}, OffsetPeer: &tg.InputPeerEmpty{}, Limit: batch})
	case q.Search != "" || q.Pinned:
		var filter tg.MessagesFilterClass = &tg.InputMessagesFilterEmpty{}
		if q.Pinned {
			filter = &tg.InputMessagesFilterPinned{}
		}

		response, err = c.api.MessagesSearch(ctx, &tg.MessagesSearchRequest{Peer: p.input, Q: q.Search, Filter: filter, OffsetID: p.offset, Limit: batch})
	default:
		r := &tg.MessagesGetHistoryRequest{Peer: p.input, OffsetID: p.offset, Limit: batch}
		// Read forward from the cursor, so a backlog larger than limit is not skipped.
		if q.Since {
			r.AddOffset, r.MinID = -batch, pageMin
		}

		response, err = c.api.MessagesGetHistory(ctx, r)
	}

	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}

	modified, ok := response.AsModified()
	if !ok {
		return nil, fmt.Errorf("unexpected unmodified message response")
	}

	return modified, nil
}

// sortPage orders a chat page ascending for forward reads, descending otherwise.
func sortPage(raw []tg.MessageClass, ascending bool) {
	slices.SortFunc(raw, func(a, b tg.MessageClass) int {
		if ascending {
			return cmp.Compare(a.GetID(), b.GetID())
		}

		return cmp.Compare(b.GetID(), a.GetID())
	})
}

// collect appends the page's in-range messages and advances p.offset past them.
func (p *pager) collect(raw []tg.MessageClass, entities peer.Entities, pageMin int) {
	for _, m := range raw {
		// BeforeID is exclusive, like Telegram's offset_id; 0 means latest.
		if (p.q.Since && m.GetID() <= pageMin) || (p.q.BeforeID > 0 && m.GetID() >= p.q.BeforeID) {
			continue
		}

		p.offset = m.GetID()
		if p.q.Since {
			p.offset++
		}

		if m, ok := m.AsNotEmpty(); ok {
			p.result = append(p.result, convertMessage(m, entities))
		}

		if len(p.result) == p.q.Limit {
			return
		}
	}
}

func (c *Client) rawMessage(ctx context.Context, chat string, id int) (tg.NotEmptyMessage, peer.Entities, resolved, error) {
	r, err := c.resolve(ctx, chat)
	if err != nil {
		return nil, peer.Entities{}, r, err
	}

	ids := []tg.InputMessageClass{&tg.InputMessageID{ID: id}}

	var response tg.MessagesMessagesClass
	if p, ok := r.input.(*tg.InputPeerChannel); ok {
		response, err = c.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash}, ID: ids})
	} else {
		response, err = c.api.MessagesGetMessages(ctx, ids)
	}

	if err != nil {
		return nil, peer.Entities{}, r, fmt.Errorf("get message: %w", err)
	}

	modified, ok := response.AsModified()
	if !ok {
		return nil, peer.Entities{}, r, fmt.Errorf("message %d not found", id)
	}

	entities := messageEntities(modified)
	for _, m := range modified.GetMessages() {
		if m, ok := m.AsNotEmpty(); ok && m.GetID() == id && describe(m.GetPeerID(), entities).info.ID == r.info.ID {
			return m, entities, r, nil
		}
	}

	return nil, entities, r, fmt.Errorf("message %d not found in this chat", id)
}

func (c *Client) Message(ctx context.Context, chat string, id int) (domain.Message, error) {
	m, entities, _, err := c.rawMessage(ctx, chat, id)
	if err != nil {
		return domain.Message{}, err
	}

	return convertMessage(m, entities), nil
}

func messageEntities(m tg.ModifiedMessagesMessages) peer.Entities {
	chats := tg.ChatClassArray(m.GetChats())
	return peer.NewEntities(tg.UserClassArray(m.GetUsers()).UserToMap(), chats.ChatToMap(), chats.ChannelToMap())
}

func convertMessage(raw tg.NotEmptyMessage, entities peer.Entities) domain.Message {
	m, ok := raw.(*tg.Message)
	if !ok {
		m = serviceMessage(raw)
	}

	chat := describe(m.PeerID, entities).info

	sender := describe(m.FromID, entities).info
	if m.FromID == nil {
		sender = chat
	}

	r := domain.Message{ID: m.ID, Date: time.Unix(int64(m.Date), 0), Text: m.Message,
		Sender: cmp.Or(sender.Username, sender.Title, m.PostAuthor), Chat: chat.Title, ChatID: chat.ID, Media: mediaInfo(m)}
	if m.GroupedID != 0 {
		r.AlbumID = strconv.FormatInt(m.GroupedID, 10)
	}

	if reply, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok {
		r.ReplyTo = reply.ReplyToMsgID
	}

	if n, ok := m.GetViews(); ok {
		r.Views = new(n)
	}

	if n, ok := m.GetForwards(); ok {
		r.Forwards = new(n)
	}

	if replies, ok := m.GetReplies(); ok {
		r.Replies = new(replies.Replies)
	}

	for _, reaction := range m.Reactions.Results {
		r.Reactions += reaction.Count
	}

	return r
}

// serviceMessage renders a non-*tg.Message, in practice a *tg.MessageService, as a text message.
func serviceMessage(raw tg.NotEmptyMessage) *tg.Message {
	service, ok := raw.(*tg.MessageService)
	if !ok {
		return &tg.Message{ID: raw.GetID(), PeerID: raw.GetPeerID(), Message: "[service message]"}
	}

	text := "[service message]"
	if service.Action != nil {
		text = "[" + service.Action.TypeName() + "]"
	}

	return &tg.Message{ID: service.ID, Date: service.Date, PeerID: service.PeerID, FromID: service.FromID, ReplyTo: service.ReplyTo, Reactions: service.Reactions, Message: text}
}
