package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/adlandh/telegram-mcp/internal/domain"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
)

type invokeFunc func(context.Context, bin.Encoder, bin.Decoder) error

func (f invokeFunc) Invoke(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
	return f(ctx, in, out)
}

func TestNormalize(t *testing.T) {
	for input, want := range map[string]string{" @name ": "name", "https://t.me/name/123": "name", "https://t.me/s/name": "name", "https://t.me/c/123456/7": "-1000000123456", "-1001234567890": "-1001234567890", "t.me/durov": "durov", "T.me/durov/5": "durov", "www.t.me/durov": "durov", "https://telegram.me/durov": "durov", "HTTPS://WWW.TELEGRAM.ME/durov": "durov"} {
		got, err := normalize(input)
		if err != nil || got != want {
			t.Errorf("%q -> %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"", "https://evil.example/name", "https://t.me/+invite", "https://t.me/joinchat/abc", "https://t.me/c/0/1", "https://evil.t.me/name", "evil.t.me/name", "https://t.me.evil.example/name", "https://%zz"} {
		if _, err := normalize(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestFoldersExposeIDs(t *testing.T) {
	filters := []tg.DialogFilterClass{
		&tg.DialogFilterDefault{},
		&tg.DialogFilter{ID: 2, Title: tg.TextWithEntities{Text: "Work"}, PinnedPeers: []tg.InputPeerClass{&tg.InputPeerChat{ChatID: 3}, &tg.InputPeerChat{ChatID: 4}}, IncludePeers: []tg.InputPeerClass{&tg.InputPeerChat{ChatID: 1}, &tg.InputPeerChat{ChatID: 3}}},
		&tg.DialogFilterChatlist{ID: 3, Title: tg.TextWithEntities{Text: "Work"}, IncludePeers: []tg.InputPeerClass{&tg.InputPeerChannel{ChannelID: 2}}},
	}
	c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.MessagesGetDialogFiltersRequest); !ok {
			t.Fatalf("unexpected request %T", in)
		}
		out.(*tg.MessagesDialogFilters).Filters = filters
		return nil
	}))}
	got, err := c.Folders(t.Context())
	if err != nil || len(got) != 2 || got[0] != (domain.Folder{ID: 2, Title: "Work", Count: 3}) || got[1] != (domain.Folder{ID: 3, Title: "Work", Count: 1}) {
		t.Fatalf("folders: %+v, %v", got, err)
	}
	filters = filters[:1]
	got, err = c.Folders(t.Context())
	if err != nil || len(got) != 0 {
		t.Fatalf("default-only folders: %+v, %v", got, err)
	}
}

// historyClient models Telegram's descending IDs, offset_id + add_offset slicing
// and min_id post-filtering for history and chat search, where every message matches.
// dialog is the chat's dialog for read-state lookups; nil means the account has none.
func historyClient(history []int, dialog *tg.Dialog) *Client {
	return &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		var offsetID, addOffset, limit, minID int
		switch req := in.(type) {
		case *tg.ContactsResolveUsernameRequest:
			*out.(*tg.ContactsResolvedPeer) = tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: 1}, Chats: []tg.ChatClass{&tg.Channel{ID: 1, AccessHash: 2, Title: "test"}}}
			return nil
		case *tg.MessagesGetPeerDialogsRequest:
			if dialog != nil {
				out.(*tg.MessagesPeerDialogs).Dialogs = []tg.DialogClass{dialog}
			}
			return nil
		case *tg.MessagesGetHistoryRequest:
			offsetID, addOffset, limit, minID = req.OffsetID, req.AddOffset, req.Limit, req.MinID
		case *tg.MessagesSearchRequest:
			offsetID, limit = req.OffsetID, req.Limit
		default:
			return fmt.Errorf("unexpected request %T", in)
		}
		start := 0
		if offsetID > 0 {
			start = len(history)
			for i, id := range history {
				if id < offsetID {
					start = i
					break
				}
			}
		}
		start = max(0, start+addOffset)
		var messages []tg.MessageClass
		for _, id := range history[start:min(start+limit, len(history))] {
			if id > minID {
				if id%9 == 0 {
					messages = append(messages, &tg.MessageService{ID: id, PeerID: &tg.PeerChannel{ChannelID: 1}, Action: &tg.MessageActionPinMessage{}})
				} else {
					messages = append(messages, &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: 1}, Message: fmt.Sprint(id)})
				}
			}
		}
		out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesChannelMessages{Messages: messages}
		return nil
	}))}
}

// gappedHistory returns descending IDs with gaps, spanning several server pages.
func gappedHistory() []int {
	var history []int
	for id := 400; id > 0; id-- {
		if id%7 != 0 {
			history = append(history, id)
		}
	}
	return history
}

func TestHistoryPaginationDoesNotLoseBacklog(t *testing.T) {
	history := gappedHistory()
	c := historyClient(history, nil)
	var got []int
	cursor := 0
	for range 5 {
		msgs, err := c.Messages(t.Context(), domain.MessageQuery{Chat: "test", Since: true, SinceID: cursor, Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) == 0 {
			break
		}
		for _, m := range msgs {
			if m.ID <= cursor {
				t.Fatalf("non-advancing cursor: %d <= %d", m.ID, cursor)
			}
			got = append(got, m.ID)
			cursor = m.ID
		}
	}
	want := slices.Clone(history)
	slices.Reverse(want)
	if !slices.Equal(got, want) {
		t.Fatalf("lost or duplicated messages: got %d, want %d; last %d", len(got), len(want), cursor)
	}
	msgs, err := c.Messages(t.Context(), domain.MessageQuery{Chat: "test", Limit: 200})
	if err != nil || len(msgs) != 200 || msgs[0].ID != history[0] || msgs[199].ID != history[199] {
		t.Fatalf("recent pagination: %d messages, %v", len(msgs), err)
	}
}

func TestUnreadMessagesStartAfterReadCursor(t *testing.T) {
	history := []int{15, 14, 13, 12, 11, 10}
	c := historyClient(history, &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 1}, ReadInboxMaxID: 12})
	msgs, err := c.Messages(t.Context(), domain.MessageQuery{Chat: "test", Unread: true, Limit: 2})
	if err != nil || len(msgs) != 2 || msgs[0].ID != 13 || msgs[1].ID != 14 {
		t.Fatalf("unread: %+v, %v", msgs, err)
	}
	if _, err := historyClient(history, nil).Messages(t.Context(), domain.MessageQuery{Chat: "test", Unread: true, Limit: 50}); err == nil {
		t.Fatal("missing dialog read history without a read cursor")
	}
}

func TestUnreadBacklogContinuesWithFetchSince(t *testing.T) {
	history := gappedHistory()
	c := historyClient(history, &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 1}, ReadInboxMaxID: 100})
	var want []int
	for _, id := range slices.Backward(history) {
		if id > 100 {
			want = append(want, id)
		}
	}
	msgs, err := c.Messages(t.Context(), domain.MessageQuery{Chat: "test", Unread: true, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, m := range msgs {
		got = append(got, m.ID)
	}
	if !slices.Equal(got, want[:50]) {
		t.Fatalf("first unread page: got %v, want %v", got, want[:50])
	}
	for range 10 {
		msgs, err := c.Messages(t.Context(), domain.MessageQuery{Chat: "test", Since: true, SinceID: got[len(got)-1], Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) == 0 {
			break
		}
		for _, m := range msgs {
			got = append(got, m.ID)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("lost or duplicated unread messages: got %d, want %d", len(got), len(want))
	}
}

func TestUnreadFollowsReadCursor(t *testing.T) {
	history := []int{15, 14, 13, 12, 11, 10}
	dialog := &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 1}, ReadInboxMaxID: 15, UnreadMark: true}
	c := historyClient(history, dialog)
	unread := func() []domain.Message {
		t.Helper()
		msgs, err := c.Messages(t.Context(), domain.MessageQuery{Chat: "test", Unread: true, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		return msgs
	}
	if msgs := unread(); len(msgs) != 0 {
		t.Fatalf("fully read chat returned %+v", msgs)
	}
	// mark_read moves Telegram's cursor; the next unread read starts after it.
	dialog.ReadInboxMaxID = 12
	if msgs := unread(); len(msgs) != 1 || msgs[0].ID != 13 {
		t.Fatalf("after cursor 12: %+v", msgs)
	}
	dialog.ReadInboxMaxID = 13
	if msgs := unread(); len(msgs) != 1 || msgs[0].ID != 14 {
		t.Fatalf("after cursor 13: %+v", msgs)
	}
	// markReadClient fails the test on any RPC other than the dialog lookup.
	failing, _ := markReadClient(t, &tg.InputPeerChat{ChatID: 5}, nil, "dialogs unavailable")
	if _, err := failing.Messages(t.Context(), domain.MessageQuery{Chat: "-5", Unread: true, Limit: 50}); err == nil {
		t.Fatal("dialog lookup failure was not reported")
	}
}

func TestBackwardPaginationDoesNotLoseHistory(t *testing.T) {
	history := gappedHistory()
	c := historyClient(history, nil)
	for _, search := range []string{"", "hit"} {
		var got []int
		before := 0
		for range 10 {
			msgs, err := c.Messages(t.Context(), domain.MessageQuery{Chat: "test", Search: search, BeforeID: before, Limit: 150})
			if err != nil {
				t.Fatal(err)
			}
			if len(msgs) == 0 {
				break
			}
			for _, m := range msgs {
				if before > 0 && m.ID >= before {
					t.Fatalf("search %q: id %d not below beforeId %d", search, m.ID, before)
				}
				got = append(got, m.ID)
			}
			before = slices.MinFunc(msgs, func(a, b domain.Message) int { return a.ID - b.ID }).ID
		}
		if !slices.Equal(got, history) {
			t.Fatalf("search %q: lost or duplicated messages: got %d, want %d; last beforeId %d", search, len(got), len(history), before)
		}
	}
}

func TestNumericResolutionFindsArchivedPeer(t *testing.T) {
	folders := []int{}
	c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		r := in.(*tg.MessagesGetDialogsRequest)
		folders = append(folders, r.FolderID)
		result := &tg.MessagesDialogs{}
		if r.FolderID == 1 {
			result.Chats = []tg.ChatClass{&tg.Channel{ID: 42, AccessHash: 99, Title: "archive"}}
			result.Dialogs = []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 42}}}
		}
		out.(*tg.MessagesDialogsBox).Dialogs = result
		return nil
	}))}
	for range 2 {
		r, err := c.resolve(t.Context(), "-1000000000042")
		if err != nil || r.input.(*tg.InputPeerChannel).AccessHash != 99 || !slices.Equal(folders, []int{0, 1}) {
			t.Fatalf("resolve: %+v, %v, folders %v", r, err, folders)
		}
	}
	// Positive-ID channel fallback is cached under the requested name too.
	if _, err := c.resolve(t.Context(), "42"); err != nil {
		t.Fatal(err)
	}
	folders = nil
	if r, err := c.resolve(t.Context(), "42"); err != nil || r.info.ID != "-1000000000042" || len(folders) != 0 {
		t.Fatalf("fallback rescanned: %+v, %v, folders %v", r, err, folders)
	}
}

func TestGetMessageRejectsAnotherChat(t *testing.T) {
	c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.ContactsResolveUsernameRequest:
			*out.(*tg.ContactsResolvedPeer) = tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: 1}, Users: []tg.UserClass{&tg.User{ID: 1, AccessHash: 2}}}
		case *tg.MessagesGetMessagesRequest:
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 2}}}}
		default:
			t.Fatalf("unexpected %T", in)
		}
		return nil
	}))}
	if _, err := c.Message(t.Context(), "user", 5); err == nil {
		t.Fatal("returned a message from another chat")
	}
}

func TestMediaMetadataAndPreview(t *testing.T) {
	m := &tg.Message{ID: 1, GroupedID: 14285256815022453, ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 5}, Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 10, DCID: 2, Size: 1 << 30, MimeType: "video/mp4", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{Duration: 12.5}, &tg.DocumentAttributeFilename{FileName: "../video.mp4"}}, Thumbs: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "m", W: 320, H: 180, Size: 18000}}}}}
	r := convertMessage(m, peer.Entities{})
	if r.AlbumID != "14285256815022453" || r.ReplyTo != 5 || r.Media.Type != "video" || r.Media.Duration != 12.5 {
		t.Fatalf("bad metadata: %+v", r)
	}
	f, err := fileFor(m, true)
	if err != nil || f.size != 18000 || f.location.(*tg.InputDocumentFileLocation).ThumbSize != "m" {
		t.Fatalf("preview downloads full file: %+v, %v", f, err)
	}
	f, err = fileFor(m, false)
	if err != nil || f.size != 1<<30 || f.location.(*tg.InputDocumentFileLocation).ThumbSize != "" {
		t.Fatalf("document: %+v, %v", f, err)
	}
	photo := &tg.Message{Media: &tg.MessageMediaPhoto{Photo: &tg.Photo{Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "x", W: 1280, H: 720, Size: 999999}, &tg.PhotoSize{Type: "m", W: 320, H: 180, Size: 9000}}}}}
	f, err = fileFor(photo, true)
	if err != nil || f.size != 9000 {
		t.Fatalf("photo preview: %+v, %v", f, err)
	}
}

func TestDownloadFilesArePrivateBoundedAndCleanedUp(t *testing.T) {
	dir := t.TempDir()
	for _, fail := range []bool{false, true} {
		_, err := saveDownload(dir, "../../escape", 3, 0, func(w io.Writer) error {
			if _, err := w.Write([]byte("abc")); err != nil {
				return err
			}
			if fail {
				return errors.New("network failed")
			}
			_, err := w.Write([]byte("d"))
			return err
		})
		if err == nil {
			t.Fatal("expected failure")
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("partial files left: %v, %v", entries, err)
		}
	}
	var paths []string
	for range 2 {
		f, err := saveDownload(dir, "../../same.txt", 0, 0, func(w io.Writer) error { _, err := io.WriteString(w, "hello"); return err })
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, f.Path)
		info, err := os.Stat(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(f.Path) != dir || info.Mode().Perm() != 0600 || f.Bytes != 5 || strings.HasSuffix(f.Path, ".part") {
			t.Fatalf("unsafe file: %+v, %v", f, info.Mode())
		}
	}
	if paths[0] == paths[1] {
		t.Fatal("download overwrote an existing file")
	}
}

func pdfDoc(size int64, thumbs ...tg.PhotoSizeClass) *tg.Document {
	return &tg.Document{ID: 1, Size: size, MimeType: "application/pdf", Thumbs: thumbs, Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "report.pdf"}}}
}

func downloadClient(t *testing.T, dir string, doc *tg.Document, cachedProtected, protected bool) (*Client, *int) {
	fetched := 0
	return &Client{downloadDir: dir, api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.ContactsResolveUsernameRequest:
			*out.(*tg.ContactsResolvedPeer) = tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: 7}, Chats: []tg.ChatClass{&tg.Channel{ID: 7, AccessHash: 8, Noforwards: cachedProtected}}}
		case *tg.ChannelsGetMessagesRequest:
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesChannelMessages{
				Messages: []tg.MessageClass{&tg.Message{ID: 3, PeerID: &tg.PeerChannel{ChannelID: 7}, Media: &tg.MessageMediaDocument{Document: doc}}},
				Chats:    []tg.ChatClass{&tg.Channel{ID: 7, AccessHash: 8, Noforwards: protected}},
			}
		case *tg.UploadGetFileRequest:
			fetched++
			if r.Offset == 0 {
				out.(*tg.UploadFileBox).File = &tg.UploadFile{Type: &tg.StorageFilePdf{}, Bytes: []byte("hello")}
			} else {
				out.(*tg.UploadFileBox).File = &tg.UploadFile{Type: &tg.StorageFilePdf{}}
			}
		default:
			t.Fatalf("unexpected %T", in)
		}
		return nil
	}))}, &fetched
}

func TestDownloadUsesAPIClient(t *testing.T) {
	dir := t.TempDir()
	c, fetched := downloadClient(t, dir, pdfDoc(5), true, false) // protection lifted since resolution
	f, err := c.Download(t.Context(), "chan", 3, false, 0)
	if err != nil || *fetched == 0 {
		t.Fatalf("download: %+v, %v", f, err)
	}
	data, err := os.ReadFile(f.Path)
	if err != nil || string(data) != "hello" || !strings.HasSuffix(f.Path, ".pdf") {
		t.Fatalf("saved %q at %s: %v", data, f.Path, err)
	}
}

func TestDownloadRejectsTruncatedDocument(t *testing.T) {
	dir := t.TempDir()
	c, fetched := downloadClient(t, dir, pdfDoc(10), false, false) // RPC returns 5 bytes, then EOF
	if f, err := c.Download(t.Context(), "chan", 3, false, 0); err == nil || *fetched == 0 {
		t.Fatalf("truncated download: %+v, %v, fetched %d", f, err, *fetched)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("partial files left: %v", entries)
	}
}

func TestOversizedDownloadFetchesNothing(t *testing.T) {
	dir := t.TempDir()
	c, fetched := downloadClient(t, dir, pdfDoc(5), false, false)
	if _, err := c.Download(t.Context(), "chan", 3, false, 4); err == nil || *fetched != 0 {
		t.Fatalf("oversized download: %v, fetched %d", err, *fetched)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files created: %v", entries)
	}
}

func TestEmbeddedThumbnailFetchesNothing(t *testing.T) {
	for name, thumb := range map[string]tg.PhotoSizeClass{
		"cached":   &tg.PhotoCachedSize{Type: "s", W: 90, H: 90, Bytes: []byte("thumb")},
		"stripped": &tg.PhotoStrippedSize{Type: "i", Bytes: []byte{1, 8, 8, 0xaa}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			c, fetched := downloadClient(t, dir, pdfDoc(1<<30, thumb), false, false)
			f, err := c.Download(t.Context(), "chan", 3, true, 0)
			if err != nil || *fetched != 0 || f.Bytes == 0 {
				t.Fatalf("thumbnail: %+v, %v, fetched %d", f, err, *fetched)
			}
		})
	}
}

func TestPreviewReturnsImageInMemory(t *testing.T) {
	jpeg := []byte("\xff\xd8\xff\xe0 cached")
	for name, thumb := range map[string]tg.PhotoSizeClass{
		"cached":   &tg.PhotoCachedSize{Type: "s", W: 90, H: 90, Bytes: jpeg},
		"stripped": &tg.PhotoStrippedSize{Type: "i", Bytes: []byte{1, 8, 8, 0xaa}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			c, fetched := downloadClient(t, dir, pdfDoc(1<<30, thumb), false, false)
			img, err := c.Preview(t.Context(), "chan", 3, 0)
			if err != nil || *fetched != 0 || img.MIME != "image/jpeg" || len(img.Data) == 0 {
				t.Fatalf("preview: %q, %v, fetched %d", img.MIME, err, *fetched)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatalf("files created: %v", entries)
			}
		})
	}
	for name, tc := range map[string]struct {
		thumb     tg.PhotoSizeClass
		protected bool
		maxBytes  int64
	}{
		"protected": {thumb: &tg.PhotoSize{Type: "m", W: 320, H: 320, Size: 5}, protected: true},
		"oversized": {thumb: &tg.PhotoSize{Type: "m", W: 320, H: 320, Size: 5}, maxBytes: 2},
		// The fake transfer streams "hello", which is not an image.
		"not image": {thumb: &tg.PhotoSize{Type: "m", W: 320, H: 320, Size: 5}},
	} {
		t.Run(name, func(t *testing.T) {
			c, fetched := downloadClient(t, t.TempDir(), pdfDoc(1<<30, tc.thumb), false, tc.protected)
			img, err := c.Preview(t.Context(), "chan", 3, tc.maxBytes)
			if err == nil || img.Data != nil || (name != "not image" && *fetched != 0) {
				t.Fatalf("preview: %q, %v, fetched %d", img.MIME, err, *fetched)
			}
		})
	}
}

func TestDownloadHonorsFreshProtection(t *testing.T) {
	dir := t.TempDir()
	c, fetched := downloadClient(t, dir, pdfDoc(5), false, true)
	if _, err := c.Download(t.Context(), "chan", 3, false, 0); err == nil || *fetched != 0 {
		t.Fatalf("protected download: %v, fetched %d", err, *fetched)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files created: %v", entries)
	}
}

func TestLongFilenameKeepsExtension(t *testing.T) {
	name := strings.Repeat("a", 76) + ".pdf"
	f, err := saveDownload(t.TempDir(), name, 0, 0, func(w io.Writer) error { _, err := io.WriteString(w, "x"); return err })
	if err != nil || !strings.HasSuffix(f.Path, ".pdf") {
		t.Fatalf("saved %s: %v", f.Path, err)
	}
	if got := safeName(name); len([]rune(got)) != 50 || !strings.HasSuffix(got, ".pdf") {
		t.Fatalf("safeName = %q", got)
	}
}

func TestSelfChatHasAbout(t *testing.T) {
	c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.MessagesGetDialogsRequest:
			box := out.(*tg.MessagesDialogsBox)
			if r.FolderID != 0 {
				box.Dialogs = &tg.MessagesDialogs{}
				return nil
			}
			box.Dialogs = &tg.MessagesDialogs{
				Users:   []tg.UserClass{&tg.User{ID: 5, AccessHash: 6, Self: true, FirstName: "Me"}},
				Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerUser{UserID: 5}}},
			}
		case *tg.UsersGetFullUserRequest:
			if u, ok := r.ID.(*tg.InputUser); !ok || u.UserID != 5 || u.AccessHash != 6 {
				t.Fatalf("unexpected user %#v", r.ID)
			}
			out.(*tg.UsersUserFull).FullUser = tg.UserFull{About: "bio"}
		default:
			t.Fatalf("unexpected %T", in)
		}
		return nil
	}))}
	// Self resolves through dialogs as a regular user peer, so no InputPeerSelf branch is needed.
	chat, err := c.Chat(t.Context(), "5")
	if err != nil || chat.About != "bio" {
		t.Fatalf("self chat: %+v, %v", chat, err)
	}
}

func TestChatRefreshesCachedMetadata(t *testing.T) {
	c := &Client{}
	c.peers.Store("-1000000000042", resolved{input: &tg.InputPeerChannel{ChannelID: 42, AccessHash: 99}, info: domain.Chat{ID: "-1000000000042", Title: "Old", Type: "Channel"}, known: true})
	c.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.ChannelsGetFullChannelRequest); !ok {
			t.Fatalf("unexpected %T", in)
		}
		*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChannelFull{About: "bio"}, Chats: []tg.ChatClass{&tg.Channel{ID: 42, Title: "New", Username: "renamed"}}}
		return nil
	}))
	chat, err := c.Chat(t.Context(), "-1000000000042")
	if err != nil || chat.Title != "New" || chat.Username != "@renamed" || chat.About != "bio" || chat.ID != "-1000000000042" {
		t.Fatalf("chat: %+v, %v", chat, err)
	}
}

func TestDialogs(t *testing.T) {
	var folders []int
	c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		req, ok := in.(*tg.MessagesGetDialogsRequest)
		if !ok {
			return fmt.Errorf("unexpected request %T", in)
		}
		folders = append(folders, req.FolderID)
		out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{
			Dialogs: []tg.DialogClass{
				&tg.Dialog{Peer: &tg.PeerUser{UserID: 1}, UnreadCount: 3},
				&tg.DialogFolder{Folder: tg.Folder{ID: 1}, Peer: &tg.PeerUser{UserID: 1}},
				&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 2}},
			},
			Users: []tg.UserClass{&tg.User{ID: 1, FirstName: "Ann", Username: "ann"}},
			Chats: []tg.ChatClass{&tg.Channel{ID: 2, Title: "News"}},
		}
		return nil
	}))}
	got, err := c.Dialogs(t.Context(), 10, false, false)
	want := []domain.Chat{
		{ID: "1", Title: "Ann", Username: "@ann", Type: "User", Unread: 3},
		{ID: "-1000000000002", Title: "News", Type: "Channel"},
	}
	if err != nil || !slices.EqualFunc(got, want, func(a, b domain.Chat) bool { return a == b }) {
		t.Fatalf("dialogs: %+v, %v", got, err)
	}
	if got, err := c.Dialogs(t.Context(), 1, true, false); err != nil || len(got) != 1 || folders[len(folders)-1] != 1 {
		t.Fatalf("archived dialogs: %+v, %v, folders %v", got, err, folders)
	}
	if folders[0] != 0 {
		t.Fatalf("main list requested folder %d", folders[0])
	}
}

func TestSearchRPCs(t *testing.T) {
	var got []string
	c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.ContactsResolveUsernameRequest:
			*out.(*tg.ContactsResolvedPeer) = tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: 1}, Chats: []tg.ChatClass{&tg.Channel{ID: 1, AccessHash: 2, Title: "test"}}}
			return nil
		case *tg.MessagesSearchRequest:
			got = append(got, fmt.Sprintf("search %T %q", req.Filter, req.Q))
		case *tg.MessagesSearchGlobalRequest:
			got = append(got, fmt.Sprintf("global %q", req.Q))
		default:
			return fmt.Errorf("unexpected request %T", in)
		}
		out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{
			Messages: []tg.MessageClass{&tg.Message{ID: 5, PeerID: &tg.PeerChannel{ChannelID: 1}, Message: "hit"}},
			Chats:    []tg.ChatClass{&tg.Channel{ID: 1, Title: "test"}},
		}
		return nil
	}))}
	for _, q := range []domain.MessageQuery{
		{Chat: "test", Search: "hit", Limit: 10},
		{Chat: "test", Pinned: true, Limit: 10},
		{Search: "hit", Global: true, Limit: 10},
	} {
		msgs, err := c.Messages(t.Context(), q)
		if err != nil || len(msgs) != 1 || msgs[0].ID != 5 || msgs[0].Text != "hit" || msgs[0].Chat != "test" {
			t.Fatalf("%+v: %+v, %v", q, msgs, err)
		}
	}
	want := []string{
		`search *tg.InputMessagesFilterEmpty "hit"`,
		`search *tg.InputMessagesFilterPinned ""`,
		`global "hit"`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("RPCs %q, want %q", got, want)
	}
}

func TestUsernameResolutionIsCached(t *testing.T) {
	calls, fail := 0, false
	c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		req, ok := in.(*tg.ContactsResolveUsernameRequest)
		if !ok {
			return fmt.Errorf("unexpected request %T", in)
		}
		calls++
		if fail {
			return errors.New("USERNAME_NOT_OCCUPIED")
		}
		if req.Username != "Durov" {
			t.Errorf("username %q", req.Username)
		}
		*out.(*tg.ContactsResolvedPeer) = tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: 1}, Chats: []tg.ChatClass{&tg.Channel{ID: 1, AccessHash: 2, Title: "Durov"}}}
		return nil
	}))}
	for _, name := range []string{"@Durov", "t.me/durov", "-1000000000001"} {
		if r, err := c.resolve(t.Context(), name); err != nil || r.input.(*tg.InputPeerChannel).AccessHash != 2 {
			t.Fatalf("%s: %+v, %v", name, r, err)
		}
	}
	if calls != 1 {
		t.Fatalf("username resolved %d times, want 1", calls)
	}
	fail, calls = true, 0
	for range 2 {
		if _, err := c.resolve(t.Context(), "@missing"); err == nil {
			t.Fatal("missing username resolved")
		}
	}
	if calls != 2 {
		t.Fatalf("failed lookup reused: %d calls", calls)
	}
}

func TestListedDialogResolvesWithoutRescan(t *testing.T) {
	scans := 0
	c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.MessagesGetDialogsRequest); !ok {
			return fmt.Errorf("unexpected request %T", in)
		}
		scans++
		out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{
			Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 2}}},
			Chats:   []tg.ChatClass{&tg.Channel{ID: 2, AccessHash: 7, Title: "News"}},
		}
		return nil
	}))}
	if _, err := c.Dialogs(t.Context(), 10, false, false); err != nil {
		t.Fatal(err)
	}
	scans = 0
	if r, err := c.resolve(t.Context(), "-1000000000002"); err != nil || r.input.(*tg.InputPeerChannel).AccessHash != 7 || scans != 0 {
		t.Fatalf("resolve: %+v, %v, scans %d", r, err, scans)
	}
}

func TestDialogsUnreadOnly(t *testing.T) {
	var folders []int
	pages := map[int]int{}
	fail := false
	c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		req, ok := in.(*tg.MessagesGetDialogsRequest)
		if !ok {
			return fmt.Errorf("unexpected request %T", in)
		}
		if fail {
			return errors.New("telegram failed")
		}
		folders = append(folders, req.FolderID)
		page := pages[req.FolderID]
		pages[req.FolderID]++
		box := out.(*tg.MessagesDialogsBox)
		switch {
		case req.FolderID == 1:
			box.Dialogs = &tg.MessagesDialogs{
				Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerUser{UserID: 50}}, &tg.Dialog{Peer: &tg.PeerUser{UserID: 51}, UnreadCount: 1}},
				Users:   []tg.UserClass{&tg.User{ID: 50}, &tg.User{ID: 51}},
			}
		case page == 0:
			// A full page of read chats must not end the unread scan.
			users := make([]tg.UserClass, 100)
			dialogs := make([]tg.DialogClass, 100)
			for i := range 100 {
				users[i] = &tg.User{ID: int64(100 + i)}
				dialogs[i] = &tg.Dialog{Peer: &tg.PeerUser{UserID: int64(100 + i)}}
			}
			box.Dialogs = &tg.MessagesDialogsSlice{Count: 104, Dialogs: dialogs, Users: users}
		default:
			box.Dialogs = &tg.MessagesDialogs{
				Dialogs: []tg.DialogClass{
					&tg.Dialog{Peer: &tg.PeerUser{UserID: 1}, UnreadCount: 3},
					&tg.Dialog{Peer: &tg.PeerUser{UserID: 2}},
					&tg.Dialog{Peer: &tg.PeerUser{UserID: 3}, UnreadMark: true},
					&tg.Dialog{Peer: &tg.PeerUser{UserID: 4}, UnreadCount: 1},
				},
				Users: []tg.UserClass{&tg.User{ID: 1}, &tg.User{ID: 2}, &tg.User{ID: 3}, &tg.User{ID: 4}},
			}
		}
		return nil
	}))}
	ids := func(chats []domain.Chat) []string {
		var out []string
		for _, chat := range chats {
			out = append(out, chat.ID)
		}
		return out
	}
	got, err := c.Dialogs(t.Context(), 2, false, true)
	if err != nil || !slices.Equal(ids(got), []string{"1", "3"}) || got[0].Unread != 3 || !got[1].UnreadMark {
		t.Fatalf("unread dialogs: %+v, %v", got, err)
	}
	clear(pages)
	got, err = c.Dialogs(t.Context(), 104, false, false)
	if err != nil || len(got) != 104 || got[0].ID != "100" || !got[102].UnreadMark {
		t.Fatalf("all dialogs: %d %v", len(got), err)
	}
	folders = nil
	got, err = c.Dialogs(t.Context(), 10, true, true)
	if err != nil || !slices.Equal(ids(got), []string{"51"}) || len(folders) == 0 || slices.ContainsFunc(folders, func(f int) bool { return f != 1 }) {
		t.Fatalf("unread archive: %+v, %v, folders %v", got, err, folders)
	}
	fail = true
	if _, err := c.Dialogs(t.Context(), 10, false, true); err == nil {
		t.Fatal("telegram error was swallowed")
	}
}

func TestSelf(t *testing.T) {
	var fail error
	c := &Client{api: tg.NewClient(invokeFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		req, ok := in.(*tg.UsersGetUsersRequest)
		if !ok || len(req.ID) != 1 {
			t.Fatalf("unexpected RPC %#v", in)
		}
		if _, ok := req.ID[0].(*tg.InputUserSelf); !ok {
			t.Fatalf("requested %T", req.ID[0])
		}
		if fail != nil {
			return fail
		}
		out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 42, Self: true, FirstName: "Ann", LastName: "Lee", Username: "annlee", Phone: "491234567890"}}
		return nil
	}))}
	me, err := c.Self(t.Context())
	if err != nil || me != (domain.Chat{ID: "42", Title: "Ann Lee", Username: "@annlee", Type: "User"}) {
		t.Fatalf("self: %+v, %v", me, err)
	}
	if strings.Contains(fmt.Sprintf("%+v", me), "491234567890") {
		t.Fatal("phone number leaked")
	}
	fail = errors.New("telegram failed")
	if _, err := c.Self(t.Context()); !errors.Is(err, fail) {
		t.Fatalf("telegram error: %v", err)
	}
	fail = nil
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.Self(ctx); err == nil {
		t.Fatal("cancelled request succeeded")
	}
}
