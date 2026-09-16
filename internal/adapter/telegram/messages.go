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
	var input tg.InputPeerClass
	if !q.Global {
		r, err := c.resolve(ctx, q.Chat)
		if err != nil {
			return nil, err
		}
		input = r.input
	}
	result := make([]domain.Message, 0, q.Limit)
	offset := 0
	if q.Since {
		offset = q.SinceID + 1
	}
	for len(result) < q.Limit {
		batch := min(100, q.Limit-len(result))
		pageMin := offset - 1
		var response tg.MessagesMessagesClass
		var err error
		switch {
		case q.Global:
			response, err = c.api.MessagesSearchGlobal(ctx, &tg.MessagesSearchGlobalRequest{Q: q.Search, Filter: &tg.InputMessagesFilterEmpty{}, OffsetPeer: &tg.InputPeerEmpty{}, Limit: batch})
		case q.Search != "" || q.Pinned:
			var filter tg.MessagesFilterClass = &tg.InputMessagesFilterEmpty{}
			if q.Pinned {
				filter = &tg.InputMessagesFilterPinned{}
			}
			response, err = c.api.MessagesSearch(ctx, &tg.MessagesSearchRequest{Peer: input, Q: q.Search, Filter: filter, OffsetID: offset, Limit: batch})
		default:
			r := &tg.MessagesGetHistoryRequest{Peer: input, OffsetID: offset, Limit: batch}
			// Read forward from the cursor, so a backlog larger than limit is not skipped.
			if q.Since {
				r.AddOffset, r.MinID = -batch, pageMin
			}
			response, err = c.api.MessagesGetHistory(ctx, r)
		}
		if err != nil {
			return nil, err
		}
		modified, ok := response.AsModified()
		if !ok {
			return nil, fmt.Errorf("unexpected unmodified message response")
		}
		raw := modified.GetMessages()
		if len(raw) == 0 {
			break
		}
		entities := messageEntities(modified)
		if !q.Global {
			slices.SortFunc(raw, func(a, b tg.MessageClass) int {
				if q.Since {
					return cmp.Compare(a.GetID(), b.GetID())
				}
				return cmp.Compare(b.GetID(), a.GetID())
			})
		}
		previous := offset
		for _, m := range raw {
			if q.Since && m.GetID() <= pageMin {
				continue
			}
			offset = m.GetID()
			if q.Since {
				offset++
			}
			if m, ok := m.AsNotEmpty(); ok {
				result = append(result, convertMessage(m, entities))
			}
			if len(result) == q.Limit {
				break
			}
		}
		if q.Global || len(raw) < batch {
			break
		}
		if offset == previous {
			return nil, fmt.Errorf("telegram pagination made no progress")
		}
	}
	return result, nil
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
		return nil, peer.Entities{}, r, err
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
	if service, ok := raw.(*tg.MessageService); ok {
		text := "[service message]"
		if service.Action != nil {
			text = "[" + service.Action.TypeName() + "]"
		}
		return convertMessage(&tg.Message{ID: service.ID, Date: service.Date, PeerID: service.PeerID, FromID: service.FromID, ReplyTo: service.ReplyTo, Reactions: service.Reactions, Message: text}, entities)
	}
	m := raw.(*tg.Message)
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
