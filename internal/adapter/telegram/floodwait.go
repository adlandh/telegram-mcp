package telegram

import (
	"context"
	"time"

	"github.com/gotd/td/bin"
	gotd "github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// ponytail: fixed cap, make it configurable if 30s proves wrong for real accounts.
const maxFloodWait = 30 * time.Second

// FloodWait retries a request once after a FLOOD_WAIT of at most maxFloodWait.
// Telegram does not execute a rate-limited request, so repeating it is safe.
func FloodWait() gotd.Middleware {
	return gotd.MiddlewareFunc(func(next tg.Invoker) gotd.InvokeFunc {
		return func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
			err := next.Invoke(ctx, in, out)
			if d, ok := tgerr.AsFloodWait(err); !ok || d > maxFloodWait {
				return err
			}
			if _, err := tgerr.FloodWait(ctx, err); ctx.Err() != nil {
				return err
			}
			return next.Invoke(ctx, in, out)
		}
	})
}
