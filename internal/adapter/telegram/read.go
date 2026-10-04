package telegram

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
)

// MarkRead is the only operation that changes Telegram account state.
func (c *Client) MarkRead(ctx context.Context, chat string, messageID int) (string, int, error) {
	r, err := c.resolve(ctx, chat)
	if err != nil {
		return "", 0, err
	}
	unreadMark := false
	if messageID == 0 {
		// Official clients read up to the latest message instead of relying on max_id=0.
		dialogs, err := c.api.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: r.input}})
		if err != nil {
			return "", 0, fmt.Errorf("get dialog: %w", err)
		}
		for _, item := range dialogs.Dialogs {
			if d, ok := item.(*tg.Dialog); ok {
				messageID, unreadMark = d.TopMessage, d.UnreadMark
			}
		}
	}
	// An empty chat has no history to acknowledge, but may still carry a manual unread mark.
	if messageID > 0 {
		switch p := r.input.(type) {
		case *tg.InputPeerChannel:
			_, err = c.api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{Channel: &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash}, MaxID: messageID})
		case *tg.InputPeerUser, *tg.InputPeerChat, *tg.InputPeerSelf:
			_, err = c.api.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: p, MaxID: messageID})
		default:
			return "", 0, fmt.Errorf("unsupported chat type %T", p)
		}
		if err != nil {
			return "", 0, fmt.Errorf("read history: %w", err)
		}
	}
	if unreadMark {
		if _, err := c.api.MessagesMarkDialogUnread(ctx, &tg.MessagesMarkDialogUnreadRequest{Peer: &tg.InputDialogPeer{Peer: r.input}}); err != nil {
			return "", 0, fmt.Errorf("clear unread mark: %w", err)
		}
	}
	return r.info.ID, messageID, nil
}
