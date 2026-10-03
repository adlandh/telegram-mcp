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
		&tg.DialogFilter{ID: 2, Title: tg.TextWithEntities{Text: "Work"}, PinnedPeers: []tg.InputPeerClass{&tg.InputPeerChat{ChatID: 3}, &tg.InputPeerChat{ChatID: 4}}, IncludePeers: []tg.InputPeerClass{&tg.InputPeerChat{ChatID: 1}}},
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

func TestHistoryPaginationDoesNotLoseBacklog(t *testing.T) {
	// Model Telegram's descending IDs, offset_id + add_offset slicing and min_id
	// post-filtering. Includes ID gaps and more than one server page.
	var history []int
	for id := 400; id > 0; id-- {
		if id%7 != 0 {
			history = append(history, id)
		}
	}
	c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.ContactsResolveUsernameRequest:
			*out.(*tg.ContactsResolvedPeer) = tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: 1}, Chats: []tg.ChatClass{&tg.Channel{ID: 1, AccessHash: 2, Title: "test"}}}
		case *tg.MessagesGetHistoryRequest:
			start := 0
			if req.OffsetID > 0 {
				start = len(history)
				for i, id := range history {
					if id < req.OffsetID {
						start = i
						break
					}
				}
			}
			start = max(0, start+req.AddOffset)
			var messages []tg.MessageClass
			for _, id := range history[start:min(start+req.Limit, len(history))] {
				if id > req.MinID {
					if id%9 == 0 {
						messages = append(messages, &tg.MessageService{ID: id, PeerID: &tg.PeerChannel{ChannelID: 1}, Action: &tg.MessageActionPinMessage{}})
					} else {
						messages = append(messages, &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: 1}, Message: fmt.Sprint(id)})
					}
				}
			}
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesChannelMessages{Messages: messages}
		default:
			return fmt.Errorf("unexpected request %T", in)
		}
		return nil
	}))}
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
		_, err := saveDownload(dir, "../../escape", 3, func(w io.Writer) error {
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
		f, err := saveDownload(dir, "../../same.txt", 0, func(w io.Writer) error { _, err := io.WriteString(w, "hello"); return err })
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

func downloadClient(t *testing.T, dir string, protected bool) (*Client, *int) {
	fetched := 0
	return &Client{downloadDir: dir, api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.ContactsResolveUsernameRequest:
			*out.(*tg.ContactsResolvedPeer) = tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: 7}, Chats: []tg.ChatClass{&tg.Channel{ID: 7, AccessHash: 8}}}
		case *tg.ChannelsGetMessagesRequest:
			doc := &tg.Document{ID: 1, Size: 5, MimeType: "application/pdf", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "report.pdf"}}}
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
	c, fetched := downloadClient(t, dir, false)
	f, err := c.Download(t.Context(), "chan", 3, false, 0)
	if err != nil || *fetched == 0 {
		t.Fatalf("download: %+v, %v", f, err)
	}
	data, err := os.ReadFile(f.Path)
	if err != nil || string(data) != "hello" || !strings.HasSuffix(f.Path, ".pdf") {
		t.Fatalf("saved %q at %s: %v", data, f.Path, err)
	}
}

func TestDownloadHonorsFreshProtection(t *testing.T) {
	dir := t.TempDir()
	c, fetched := downloadClient(t, dir, true)
	if _, err := c.Download(t.Context(), "chan", 3, false, 0); err == nil || *fetched != 0 {
		t.Fatalf("protected download: %v, fetched %d", err, *fetched)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files created: %v", entries)
	}
}

func TestLongFilenameKeepsExtension(t *testing.T) {
	name := strings.Repeat("a", 76) + ".pdf"
	f, err := saveDownload(t.TempDir(), name, 0, func(w io.Writer) error { _, err := io.WriteString(w, "x"); return err })
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
