package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/adlandh/telegram-mcp/internal/domain"
	"github.com/adlandh/telegram-mcp/internal/port"
)

type fakeTelegram struct {
	port.Telegram
	query    domain.MessageQuery
	maxBytes int64
	preview  bool
	deadline bool
}

func (f *fakeTelegram) Messages(ctx context.Context, q domain.MessageQuery) ([]domain.Message, error) {
	f.query = q
	_, f.deadline = ctx.Deadline()
	return []domain.Message{{ID: 12, Text: "second", AlbumID: "14285256815022453", ReplyTo: 11}, {ID: 11, Text: "first"}}, nil
}

func (f *fakeTelegram) Download(_ context.Context, _ string, _ int, preview bool, maxBytes int64) (domain.Download, error) {
	f.preview, f.maxBytes = preview, maxBytes
	return domain.Download{Path: "/tmp/file", Bytes: 10}, nil
}

func TestMessageOrderingLimitsAndMetadata(t *testing.T) {
	f := &fakeTelegram{}
	s := New(f, 200, time.Minute)
	text, err := s.Execute(t.Context(), "fetch_since", Arguments{GroupURL: "@test", SinceID: new(10), Limit: new(1000)})
	if err != nil {
		t.Fatal(err)
	}
	if !f.deadline || !f.query.Since || f.query.SinceID != 10 || f.query.Limit != 200 {
		t.Fatalf("bad query: %+v", f.query)
	}
	for _, want := range []string{"maxId=12", "[album:14285256815022453]", "[reply:11]"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q: %s", want, text)
		}
	}
	if strings.Index(text, "#11 ") > strings.Index(text, "#12 ") {
		t.Fatalf("not chronological: %s", text)
	}
	for name, want := range map[string]int{"read_messages": 50, "search_messages": 30, "get_pinned": 20, "global_search": 30, "fetch_since": 100} {
		if _, err := s.Execute(t.Context(), name, Arguments{GroupURL: "@test", Query: "test", SinceID: new(0)}); err != nil {
			t.Fatal(err)
		}
		if f.query.Limit != want {
			t.Errorf("%s default limit = %d", name, f.query.Limit)
		}
	}
}

func TestRejectInvalidArgumentsBeforeCallingTelegram(t *testing.T) {
	s := New(nil, 200, time.Minute)
	for _, tc := range []struct {
		name string
		args Arguments
	}{
		{"read_messages", Arguments{}},
		{"read_messages", Arguments{GroupURL: "x", Limit: new(0)}},
		{"search_messages", Arguments{GroupURL: "x", Query: "  "}},
		{"fetch_since", Arguments{GroupURL: "x"}},
		{"fetch_since", Arguments{GroupURL: "x", SinceID: new(-1)}},
		{"get_message", Arguments{GroupURL: "x", MessageID: 1 << 31}},
		{"download_media", Arguments{GroupURL: "x", MessageID: 1, MaxMB: new(int64(-1))}},
		{"download_media", Arguments{GroupURL: "x", MessageID: 1, MaxMB: new(int64(1<<63 - 1))}},
		{"unknown", Arguments{}},
	} {
		if _, err := s.Execute(t.Context(), tc.name, tc.args); err == nil {
			t.Errorf("accepted invalid %s: %+v", tc.name, tc.args)
		}
	}
}

func TestDownloadLimits(t *testing.T) {
	f := &fakeTelegram{}
	s := New(f, 200, time.Minute)
	for _, tc := range []struct {
		override *int64
		want     int64
	}{{nil, 200 * 1024 * 1024}, {new(int64(0)), 0}, {new(int64(3)), 3 * 1024 * 1024}} {
		_, err := s.Execute(t.Context(), "download_media", Arguments{GroupURL: "x", MessageID: 1, MaxMB: tc.override})
		if err != nil || f.maxBytes != tc.want || f.preview {
			t.Fatalf("download limit = %d, err %v", f.maxBytes, err)
		}
	}
	if _, err := s.Execute(t.Context(), "get_thumbnail", Arguments{GroupURL: "x", MessageID: 1}); err != nil || !f.preview {
		t.Fatalf("preview: %v", err)
	}
}
