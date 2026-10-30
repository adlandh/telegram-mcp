package telegram

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
)

// SaveDraft replaces the chat's cloud draft with plain text. It never sends a message.
func (c *Client) SaveDraft(ctx context.Context, chat, text string) (string, error) {
	r, err := c.resolve(ctx, chat)
	if err != nil {
		return "", err
	}

	ok, err := c.api.MessagesSaveDraft(ctx, &tg.MessagesSaveDraftRequest{Peer: r.input, Message: text})
	if err != nil {
		return "", fmt.Errorf("save draft: %w", err)
	}

	if !ok {
		return "", fmt.Errorf("telegram did not save the draft")
	}

	return r.info.ID, nil
}
