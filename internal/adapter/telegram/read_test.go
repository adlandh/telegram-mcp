package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/adlandh/telegram-mcp/internal/domain"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func markReadClient(t *testing.T, input tg.InputPeerClass, dialog *tg.Dialog, fail string) (*Client, *[]string) {
	t.Helper()
	var calls []string
	c := &Client{api: tg.NewClient(invokeFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch r := in.(type) {
		case *tg.MessagesGetPeerDialogsRequest:
			calls = append(calls, "dialogs")
			if dialog != nil {
				out.(*tg.MessagesPeerDialogs).Dialogs = []tg.DialogClass{dialog}
			}
		case *tg.MessagesReadHistoryRequest:
			calls = append(calls, fmt.Sprintf("messages:%d", r.MaxID))
		case *tg.ChannelsReadHistoryRequest:
			ch := r.Channel.(*tg.InputChannel)
			calls = append(calls, fmt.Sprintf("channel:%d:%d:%d", ch.ChannelID, ch.AccessHash, r.MaxID))
		case *tg.MessagesMarkDialogUnreadRequest:
			if r.Unread {
				t.Error("marked chat unread")
			}
			calls = append(calls, "unmark")
		default:
			t.Fatalf("unexpected RPC %T", in)
		}
		if fail != "" {
			return errors.New(fail)
		}
		return nil
	}))}
	c.store("-5", resolved{input: input, info: domain.Chat{ID: "-5"}, known: true})
	return c, &calls
}

func TestMarkRead(t *testing.T) {
	for _, tc := range []struct {
		name      string
		input     tg.InputPeerClass
		dialog    *tg.Dialog
		messageID int
		upTo      int
		calls     []string
	}{
		{"partial basic group", &tg.InputPeerChat{ChatID: 5}, nil, 12, 12, []string{"messages:12"}},
		{"partial channel", &tg.InputPeerChannel{ChannelID: 5, AccessHash: 99}, nil, 12, 12, []string{"channel:5:99:12"}},
		{"whole chat clears unread mark", &tg.InputPeerChat{ChatID: 5}, &tg.Dialog{TopMessage: 42, UnreadMark: true}, 0, 42, []string{"dialogs", "messages:42", "unmark"}},
		{"whole chat keeps absent mark", &tg.InputPeerChannel{ChannelID: 5, AccessHash: 99}, &tg.Dialog{TopMessage: 42}, 0, 42, []string{"dialogs", "channel:5:99:42"}},
		{"empty chat", &tg.InputPeerChat{ChatID: 5}, &tg.Dialog{}, 0, 0, []string{"dialogs"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := markReadClient(t, tc.input, tc.dialog, "")
			id, upTo, err := c.MarkRead(t.Context(), "-5", tc.messageID)
			if err != nil || id != "-5" || upTo != tc.upTo || !slices.Equal(*calls, tc.calls) {
				t.Fatalf("got %q %d %v, calls %v", id, upTo, err, *calls)
			}
		})
	}
}

func TestMarkReadFailures(t *testing.T) {
	c, calls := markReadClient(t, &tg.InputPeerChat{ChatID: 5}, nil, "")
	if _, _, err := c.MarkRead(t.Context(), "https://t.me/+invite", 0); err == nil || len(*calls) != 0 {
		t.Fatalf("invite link: %v, calls %v", err, *calls)
	}
	c, _ = markReadClient(t, &tg.InputPeerChat{ChatID: 5}, nil, "telegram failed")
	if _, _, err := c.MarkRead(t.Context(), "-5", 3); err == nil {
		t.Fatal("telegram error was swallowed")
	}
	c, calls = markReadClient(t, &tg.InputPeerChat{ChatID: 5}, nil, "")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := c.MarkRead(ctx, "-5", 3); err == nil {
		t.Fatalf("cancelled request succeeded, calls %v", *calls)
	}
}
