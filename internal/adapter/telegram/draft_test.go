package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/adlandh/telegram-mcp/internal/domain"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestSaveDraft(t *testing.T) {
	var saved []*tg.MessagesSaveDraftRequest
	var fail error
	result := true
	c := &Client{api: tg.NewClient(invokeFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Any other RPC, such as sending or reading history, fails the test.
		req, ok := in.(*tg.MessagesSaveDraftRequest)
		if !ok {
			t.Fatalf("unexpected RPC %T", in)
		}
		saved = append(saved, req)
		if fail != nil {
			return fail
		}
		out.(*tg.BoolBox).Bool = &tg.BoolFalse{}
		if result {
			out.(*tg.BoolBox).Bool = &tg.BoolTrue{}
		}
		return nil
	}))}
	input := &tg.InputPeerChannel{ChannelID: 5, AccessHash: 99}
	c.peers.Store("-1000000000005", resolved{input: input, info: domain.Chat{ID: "-1000000000005"}, known: true})
	id, err := c.SaveDraft(t.Context(), "-1000000000005", " **bold** <b>x</b>\n")
	if err != nil || id != "-1000000000005" || len(saved) != 1 {
		t.Fatalf("save: %q, %v, %d calls", id, err, len(saved))
	}
	if r := saved[0]; r.Peer != input || r.Message != " **bold** <b>x</b>\n" || len(r.Entities) != 0 || r.ReplyTo != nil {
		t.Fatalf("draft request: %+v", r)
	}
	if _, err := c.SaveDraft(t.Context(), "https://t.me/+invite", "hi"); err == nil || len(saved) != 1 {
		t.Fatalf("invite link: %v, %d calls", err, len(saved))
	}
	result = false
	if _, err := c.SaveDraft(t.Context(), "-1000000000005", "hi"); err == nil {
		t.Fatal("false result reported as saved")
	}
	fail = errors.New("CHAT_WRITE_FORBIDDEN")
	if _, err := c.SaveDraft(t.Context(), "-1000000000005", "hi"); !errors.Is(err, fail) {
		t.Fatalf("telegram error: %v", err)
	}
	fail = nil
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.SaveDraft(ctx, "-1000000000005", "hi"); err == nil {
		t.Fatal("cancelled request succeeded")
	}
}
