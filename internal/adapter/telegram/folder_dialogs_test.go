package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestFolderDialogsExplicitAndShared(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprint("shared=", shared), func(t *testing.T) {
			pinned := []tg.InputPeerClass{&tg.InputPeerChannel{ChannelID: 7}}
			included := []tg.InputPeerClass{&tg.InputPeerChat{ChatID: 7}, &tg.InputPeerChannel{ChannelID: 7}, &tg.InputPeerUser{UserID: 9}}
			var filter tg.DialogFilterClass = &tg.DialogFilter{ID: 2, PinnedPeers: pinned, IncludePeers: included, ExcludeArchived: true}
			if shared {
				filter = &tg.DialogFilterChatlist{ID: 2, PinnedPeers: pinned, IncludePeers: included}
			}
			calls := 0
			c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch req := in.(type) {
				case *tg.MessagesGetDialogFiltersRequest:
					out.(*tg.MessagesDialogFilters).Filters = []tg.DialogFilterClass{&tg.DialogFilterDefault{}, filter}
				case *tg.MessagesGetPeerDialogsRequest:
					calls++
					if len(req.Peers) != 3 {
						t.Fatalf("wrong peer lookup: %+v", req.Peers)
					}
					*out.(*tg.MessagesPeerDialogs) = tg.MessagesPeerDialogs{
						Dialogs: []tg.DialogClass{
							&tg.Dialog{Peer: &tg.PeerChat{ChatID: 7}, UnreadCount: 2},
							&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 7}, UnreadCount: 3},
						},
						Chats: []tg.ChatClass{&tg.Chat{ID: 7, Title: "Group"}, &tg.Channel{ID: 7, Title: "Archived", Broadcast: true}},
					}
				default:
					t.Fatalf("unexpected RPC %T", in)
				}
				return nil
			}))}
			got, err := c.FolderDialogs(t.Context(), 2, 10)
			if err != nil || calls != 1 || len(got) != 2 || got[0].ID != "-1000000000007" || got[0].Unread != 3 || got[1].ID != "-7" {
				t.Fatalf("explicit folder: %+v, calls=%d, err=%v", got, calls, err)
			}
		})
	}
}

func TestFolderDialogsUnknownAndEmpty(t *testing.T) {
	c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesGetDialogFiltersRequest:
			out.(*tg.MessagesDialogFilters).Filters = []tg.DialogFilterClass{&tg.DialogFilter{ID: 2}}
		default:
			t.Fatalf("unexpected RPC %T", in)
		}
		return nil
	}))}
	got, err := c.FolderDialogs(t.Context(), 2, 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty folder: %+v, %v", got, err)
	}
	if _, err := c.FolderDialogs(t.Context(), 3, 10); err == nil {
		t.Fatal("accepted unknown folder")
	}
}

func TestFolderDialogsCategories(t *testing.T) {
	cases := []struct {
		name   string
		user   *tg.User
		chat   tg.ChatClass
		peer   tg.PeerClass
		filter tg.DialogFilter
		want   bool
	}{
		{"contact", &tg.User{ID: 1, Contact: true}, nil, &tg.PeerUser{UserID: 1}, tg.DialogFilter{Contacts: true}, true},
		{"non-contact", &tg.User{ID: 1}, nil, &tg.PeerUser{UserID: 1}, tg.DialogFilter{NonContacts: true}, true},
		{"bot-is-not-contact", &tg.User{ID: 1, Bot: true, Contact: true}, nil, &tg.PeerUser{UserID: 1}, tg.DialogFilter{Contacts: true}, false},
		{"bot", &tg.User{ID: 1, Bot: true}, nil, &tg.PeerUser{UserID: 1}, tg.DialogFilter{Bots: true}, true},
		{"basic-group", nil, &tg.Chat{ID: 1}, &tg.PeerChat{ChatID: 1}, tg.DialogFilter{Groups: true}, true},
		{"supergroup", nil, &tg.Channel{ID: 1, Megagroup: true}, &tg.PeerChannel{ChannelID: 1}, tg.DialogFilter{Groups: true}, true},
		{"supergroup-is-not-broadcast", nil, &tg.Channel{ID: 1, Megagroup: true}, &tg.PeerChannel{ChannelID: 1}, tg.DialogFilter{Broadcasts: true}, false},
		{"broadcast", nil, &tg.Channel{ID: 1, Broadcast: true}, &tg.PeerChannel{ChannelID: 1}, tg.DialogFilter{Broadcasts: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filter := tc.filter
			filter.ID = 2
			c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.MessagesGetDialogFiltersRequest:
					out.(*tg.MessagesDialogFilters).Filters = []tg.DialogFilterClass{&filter}
				case *tg.MessagesGetDialogsRequest:
					r := &tg.MessagesDialogs{Dialogs: []tg.DialogClass{&tg.Dialog{Peer: tc.peer}}}
					if tc.user != nil {
						r.Users = []tg.UserClass{tc.user}
					} else {
						r.Chats = []tg.ChatClass{tc.chat}
					}
					out.(*tg.MessagesDialogsBox).Dialogs = r
				default:
					t.Fatalf("unexpected RPC %T", in)
				}
				return nil
			}))}
			got, err := c.FolderDialogs(t.Context(), 2, 1)
			if err != nil || (len(got) == 1) != tc.want {
				t.Fatalf("category got %+v, want %t, err %v", got, tc.want, err)
			}
		})
	}
}

func TestFolderDialogsExplicitOverrideAndExclude(t *testing.T) {
	c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesGetDialogFiltersRequest:
			out.(*tg.MessagesDialogFilters).Filters = []tg.DialogFilterClass{&tg.DialogFilter{
				ID: 2, Groups: true, ExcludeRead: true, ExcludeArchived: true,
				PinnedPeers:  []tg.InputPeerClass{&tg.InputPeerChat{ChatID: 1}, &tg.InputPeerChat{ChatID: 2}},
				ExcludePeers: []tg.InputPeerClass{&tg.InputPeerChat{ChatID: 2}},
			}}
		case *tg.MessagesGetPeerDialogsRequest:
			*out.(*tg.MessagesPeerDialogs) = tg.MessagesPeerDialogs{Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChat{ChatID: 1}}}, Chats: []tg.ChatClass{&tg.Chat{ID: 1}}}
		case *tg.MessagesGetDialogsRequest:
			out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{}
		default:
			t.Fatalf("unexpected RPC %T", in)
		}
		return nil
	}))}
	got, err := c.FolderDialogs(t.Context(), 2, 10)
	if err != nil || len(got) != 1 || got[0].ID != "-1" {
		t.Fatalf("explicit override: %+v, %v", got, err)
	}
}

func TestFolderDialogsSkipsInaccessibleExplicitPeer(t *testing.T) {
	queries := 0
	c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.MessagesGetDialogFiltersRequest:
			out.(*tg.MessagesDialogFilters).Filters = []tg.DialogFilterClass{&tg.DialogFilter{ID: 2, IncludePeers: []tg.InputPeerClass{&tg.InputPeerChat{ChatID: 1}, &tg.InputPeerChat{ChatID: 2}}}}
		case *tg.MessagesGetPeerDialogsRequest:
			queries++
			if len(req.Peers) == 2 {
				return tgerr.New(400, tg.ErrPeerIDInvalid)
			}
			peer := req.Peers[0].(*tg.InputDialogPeer).Peer.(*tg.InputPeerChat)
			if peer.ChatID == 2 {
				return tgerr.New(400, tg.ErrPeerIDInvalid)
			}
			*out.(*tg.MessagesPeerDialogs) = tg.MessagesPeerDialogs{Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChat{ChatID: 1}}}, Chats: []tg.ChatClass{&tg.Chat{ID: 1}}}
		default:
			t.Fatalf("unexpected RPC %T", in)
		}
		return nil
	}))}
	got, err := c.FolderDialogs(t.Context(), 2, 10)
	if err != nil || queries != 3 || len(got) != 1 || got[0].ID != "-1" {
		t.Fatalf("inaccessible peer: %+v, queries=%d, err=%v", got, queries, err)
	}
}

func TestFolderDialogsSelfPeerIsDeduplicated(t *testing.T) {
	c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesGetDialogFiltersRequest:
			out.(*tg.MessagesDialogFilters).Filters = []tg.DialogFilterClass{&tg.DialogFilter{ID: 2, PinnedPeers: []tg.InputPeerClass{&tg.InputPeerSelf{}}, IncludePeers: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 5}}}}
		case *tg.MessagesGetPeerDialogsRequest:
			*out.(*tg.MessagesPeerDialogs) = tg.MessagesPeerDialogs{Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerUser{UserID: 5}}}, Users: []tg.UserClass{&tg.User{ID: 5, Self: true, FirstName: "Saved"}}}
		default:
			t.Fatalf("unexpected RPC %T", in)
		}
		return nil
	}))}
	got, err := c.FolderDialogs(t.Context(), 2, 10)
	if err != nil || len(got) != 1 || got[0].ID != "5" {
		t.Fatalf("self peer: %+v, %v", got, err)
	}
}

func TestFolderDialogsRulesAndPagination(t *testing.T) {
	archived := []int{}
	requests := 0
	now := int(time.Now().Unix())
	c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.MessagesGetDialogFiltersRequest:
			out.(*tg.MessagesDialogFilters).Filters = []tg.DialogFilterClass{&tg.DialogFilter{ID: 2, Groups: true, Broadcasts: true, ExcludeRead: true, ExcludeMuted: true, ExcludePeers: []tg.InputPeerClass{&tg.InputPeerChannel{ChannelID: 4}}}}
		case *tg.MessagesGetDialogsRequest:
			archived = append(archived, req.FolderID)
			requests++
			if req.FolderID == 0 && requests == 1 {
				users := make([]tg.UserClass, 100)
				dialogs := make([]tg.DialogClass, 100)
				for i := range 100 {
					users[i] = &tg.User{ID: int64(100 + i)}
					dialogs[i] = &tg.Dialog{Peer: &tg.PeerUser{UserID: int64(100 + i)}}
				}
				*out.(*tg.MessagesDialogsBox) = tg.MessagesDialogsBox{Dialogs: &tg.MessagesDialogsSlice{Count: 102, Dialogs: dialogs, Users: users}}
			} else if req.FolderID == 0 {
				d1 := &tg.Dialog{Peer: &tg.PeerChat{ChatID: 1}, UnreadCount: 1}
				d1.NotifySettings.SetMuteUntil(now - 1)
				d2 := &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 2}, UnreadMentionsCount: 1}
				d2.NotifySettings.SetMuteUntil(now + 3600)
				d3 := &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 4}, UnreadCount: 1}
				*out.(*tg.MessagesDialogsBox) = tg.MessagesDialogsBox{Dialogs: &tg.MessagesDialogs{Dialogs: []tg.DialogClass{d1, d2, d3}, Chats: []tg.ChatClass{&tg.Chat{ID: 1, Title: "Group"}, &tg.Channel{ID: 2, Title: "News", Broadcast: true}, &tg.Channel{ID: 4, Title: "Excluded", Broadcast: true}}}}
			} else {
				d := &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 3}, UnreadMark: true}
				*out.(*tg.MessagesDialogsBox) = tg.MessagesDialogsBox{Dialogs: &tg.MessagesDialogs{Dialogs: []tg.DialogClass{d}, Chats: []tg.ChatClass{&tg.Channel{ID: 3, Title: "Archive", Megagroup: true}}}}
			}
		case *tg.AccountGetNotifySettingsRequest:
			// The archived group has no per-peer mute value; default is not muted.
			out.(*tg.PeerNotifySettings).SetMuteUntil(now - 1)
		default:
			t.Fatalf("unexpected RPC %T", in)
		}
		return nil
	}))}
	got, err := c.FolderDialogs(t.Context(), 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(got))
	for _, chat := range got {
		ids = append(ids, chat.ID)
	}
	if !slices.Equal(ids, []string{"-1", "-1000000000002", "-1000000000003"}) || len(archived) < 3 || !slices.Contains(archived, 1) {
		t.Fatalf("folder dialogs = %v, scanned = %v", ids, archived)
	}
	for _, folderID := range archived {
		if folderID != 0 && folderID != 1 {
			t.Fatalf("custom filter ID passed to getDialogs: %d", folderID)
		}
	}
}

func TestFolderDialogsReadAndMuteState(t *testing.T) {
	cases := []struct {
		name         string
		unread       int
		unreadMark   bool
		mentions     int
		muteRelative int
		inherited    bool
		want         bool
	}{
		{"read", 0, false, 0, -1, false, false},
		{"manually-unread", 0, true, 0, -1, false, true},
		{"muted", 1, false, 0, 3600, false, false},
		{"inherited-mute", 1, false, 0, 3600, true, false},
		{"expired-mute", 1, false, 0, -1, false, true},
		{"mention-exception", 0, false, 1, 3600, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{api: tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.MessagesGetDialogFiltersRequest:
					out.(*tg.MessagesDialogFilters).Filters = []tg.DialogFilterClass{&tg.DialogFilter{ID: 2, Groups: true, ExcludeRead: true, ExcludeMuted: true, ExcludeArchived: true}}
				case *tg.MessagesGetDialogsRequest:
					d := &tg.Dialog{Peer: &tg.PeerChat{ChatID: 1}, UnreadCount: tc.unread, UnreadMark: tc.unreadMark, UnreadMentionsCount: tc.mentions}
					if !tc.inherited {
						d.NotifySettings.SetMuteUntil(int(time.Now().Unix()) + tc.muteRelative)
					}
					out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{Dialogs: []tg.DialogClass{d}, Chats: []tg.ChatClass{&tg.Chat{ID: 1}}}
				case *tg.AccountGetNotifySettingsRequest:
					out.(*tg.PeerNotifySettings).SetMuteUntil(int(time.Now().Unix()) + tc.muteRelative)
				default:
					t.Fatalf("unexpected RPC %T", in)
				}
				return nil
			}))}
			got, err := c.FolderDialogs(t.Context(), 2, 10)
			if err != nil || (len(got) == 1) != tc.want {
				t.Fatalf("folder state got %+v, want %t, err=%v", got, tc.want, err)
			}
		})
	}
}

func TestFolderDialogsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	c := &Client{api: tg.NewClient(invokeFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesGetDialogFiltersRequest:
			out.(*tg.MessagesDialogFilters).Filters = []tg.DialogFilterClass{&tg.DialogFilter{ID: 2, Groups: true}}
		case *tg.MessagesGetDialogsRequest:
			cancel()
			return ctx.Err()
		default:
			t.Fatalf("unexpected RPC %T", in)
		}
		return nil
	}))}
	if got, err := c.FolderDialogs(ctx, 2, 10); err == nil || got != nil {
		t.Fatalf("cancelled folder returned %+v, %v", got, err)
	}
}

func TestFolderDialogsPropagatesFailures(t *testing.T) {
	for _, fail := range []string{"filters", "pages", "settings"} {
		t.Run(fail, func(t *testing.T) {
			c := &Client{api: tg.NewClient(invokeFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				switch in.(type) {
				case *tg.MessagesGetDialogFiltersRequest:
					if fail == "filters" {
						return errors.New("filters failed")
					}
					out.(*tg.MessagesDialogFilters).Filters = []tg.DialogFilterClass{&tg.DialogFilter{ID: 2, Groups: true, ExcludeMuted: true}}
				case *tg.MessagesGetDialogsRequest:
					if fail == "pages" {
						return errors.New("pages failed")
					}
					out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChat{ChatID: 1}}}, Chats: []tg.ChatClass{&tg.Chat{ID: 1}}}
				case *tg.AccountGetNotifySettingsRequest:
					return errors.New("settings failed")
				}
				return nil
			}))}
			if got, err := c.FolderDialogs(t.Context(), 2, 10); err == nil || got != nil {
				t.Fatalf("failure returned %+v, %v", got, err)
			}
		})
	}
}
