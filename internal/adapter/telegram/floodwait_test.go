package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestFloodWaitRetry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses []error
		calls     int
		ok        bool
	}{
		{"short wait is retried", []error{tgerr.New(420, "FLOOD_WAIT_0"), nil}, 2, true},
		{"premium wait is retried", []error{tgerr.New(420, "FLOOD_PREMIUM_WAIT_0"), nil}, 2, true},
		{"long wait is reported", []error{tgerr.New(420, "FLOOD_WAIT_31")}, 1, false},
		{"retry happens once", []error{tgerr.New(420, "FLOOD_WAIT_0"), tgerr.New(420, "FLOOD_WAIT_0"), nil}, 2, false},
		{"other errors pass through", []error{errors.New("boom")}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			invoke := FloodWait().Handle(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
				calls++
				return tc.responses[calls-1]
			}))
			err := invoke(t.Context(), &tg.HelpGetConfigRequest{}, &tg.Config{})
			if calls != tc.calls || (err == nil) != tc.ok {
				t.Fatalf("calls %d, err %v", calls, err)
			}
			if !tc.ok && tc.responses[calls-1] != nil && !errors.Is(err, tc.responses[calls-1]) {
				t.Fatalf("err %v, want %v", err, tc.responses[calls-1])
			}
		})
	}
}

func TestFloodWaitHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	calls := 0
	invoke := FloodWait().Handle(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		calls++
		return tgerr.New(420, "FLOOD_WAIT_10")
	}))
	start := time.Now()
	err := invoke(ctx, &tg.HelpGetConfigRequest{}, &tg.Config{})
	if calls != 1 || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("calls %d, err %v, after %s", calls, err, time.Since(start))
	}
}
