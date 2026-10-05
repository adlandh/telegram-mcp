package telegram

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/adlandh/telegram-mcp/internal/domain"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// folderInputKey returns the marked ID, matching describe's info.ID; marked IDs
// keep users, basic groups, and channels distinct even when raw IDs overlap.
func folderInputKey(p tg.InputPeerClass) string {
	var key tg.PeerClass
	switch p := p.(type) {
	case *tg.InputPeerUser:
		key = &tg.PeerUser{UserID: p.UserID}
	case *tg.InputPeerUserFromMessage:
		key = &tg.PeerUser{UserID: p.UserID}
	case *tg.InputPeerChat:
		key = &tg.PeerChat{ChatID: p.ChatID}
	case *tg.InputPeerChannel:
		key = &tg.PeerChannel{ChannelID: p.ChannelID}
	case *tg.InputPeerChannelFromMessage:
		key = &tg.PeerChannel{ChannelID: p.ChannelID}
	case *tg.InputPeerSelf:
		return "self"
	}
	return describe(key, peer.Entities{}).info.ID
}

type folderDialog struct {
	resolved
	dialog *tg.Dialog
	users  peer.Entities
}

func folderDialogFrom(d *tg.Dialog, entities peer.Entities) folderDialog {
	r := describe(d.Peer, entities)
	r.info.Unread = d.UnreadCount
	return folderDialog{resolved: r, dialog: d, users: entities}
}

func (d folderDialog) isSelf() bool {
	p, ok := d.dialog.Peer.(*tg.PeerUser)
	if !ok {
		return false
	}
	u, ok := d.users.User(p.UserID)
	return ok && u.Self
}

func (d folderDialog) category(f *tg.DialogFilter) (tg.InputNotifyPeerClass, bool, error) {
	switch p := d.dialog.Peer.(type) {
	case *tg.PeerUser:
		u, ok := d.users.User(p.UserID)
		if !ok || u.Min {
			return nil, false, fmt.Errorf("user metadata unavailable for folder matching")
		}
		if u.Bot {
			return &tg.InputNotifyUsers{}, f.Bots, nil
		}
		if u.Contact {
			return &tg.InputNotifyUsers{}, f.Contacts, nil
		}
		return &tg.InputNotifyUsers{}, f.NonContacts, nil
	case *tg.PeerChat:
		if _, ok := d.users.Chat(p.ChatID); !ok {
			return nil, false, fmt.Errorf("chat metadata unavailable for folder matching")
		}
		return &tg.InputNotifyChats{}, f.Groups, nil
	case *tg.PeerChannel:
		c, ok := d.users.Channel(p.ChannelID)
		if !ok || c.Min {
			return nil, false, fmt.Errorf("channel metadata unavailable for folder matching")
		}
		if c.Broadcast {
			return &tg.InputNotifyBroadcasts{}, f.Broadcasts, nil
		}
		if c.Megagroup {
			return &tg.InputNotifyChats{}, f.Groups, nil
		}
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("unsupported dialog peer for folder matching")
}

func (c *Client) FolderDialogs(ctx context.Context, folderID, limit int) ([]domain.Chat, error) {
	filters, err := c.api.MessagesGetDialogFilters(ctx)
	if err != nil {
		return nil, fmt.Errorf("get folder filters: %w", err)
	}
	var pinned, included, excluded []tg.InputPeerClass
	var regular *tg.DialogFilter
	found := false
	for _, candidate := range filters.Filters {
		switch f := candidate.(type) {
		case *tg.DialogFilter:
			if f.ID == folderID {
				pinned, included, excluded, regular, found = f.PinnedPeers, f.IncludePeers, f.ExcludePeers, f, true
			}
		case *tg.DialogFilterChatlist:
			if f.ID == folderID {
				pinned, included, found = f.PinnedPeers, f.IncludePeers, true
			}
		}
		if found {
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("folder %d not found; refresh list_folders", folderID)
	}
	deny := make(map[string]bool, len(excluded))
	for _, p := range excluded {
		key := folderInputKey(p)
		if key == "" {
			return nil, fmt.Errorf("unsupported excluded peer %T", p)
		}
		deny[key] = true
	}
	seen := make(map[string]bool)
	ordered := make([]tg.InputPeerClass, 0, len(pinned)+len(included))
	for _, p := range slices.Concat(pinned, included) {
		key := folderInputKey(p)
		if key == "" {
			return nil, fmt.Errorf("unsupported included peer %T", p)
		}
		if !seen[key] && !deny[key] {
			ordered = append(ordered, p)
			seen[key] = true
		}
	}
	result := make([]domain.Chat, 0, min(limit, len(ordered)+100))
	available := map[string]folderDialog{}
	for offset := 0; offset < len(ordered) && len(result) < limit; offset += 100 {
		batch := ordered[offset:min(offset+100, len(ordered))]
		requests := make([]tg.InputDialogPeerClass, 0, len(batch))
		for _, p := range batch {
			requests = append(requests, &tg.InputDialogPeer{Peer: p})
		}
		r, err := c.api.MessagesGetPeerDialogs(ctx, requests)
		responses := []*tg.MessagesPeerDialogs{}
		if unavailableFolderPeer(err) {
			// A stale peer can reject the whole batch; isolate it without hiding other failures.
			for _, request := range requests {
				one, oneErr := c.api.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{request})
				if unavailableFolderPeer(oneErr) {
					continue
				}
				if oneErr != nil {
					return nil, fmt.Errorf("get folder peer: %w", oneErr)
				}
				responses = append(responses, one)
			}
		} else if err != nil {
			return nil, fmt.Errorf("get folder peers: %w", err)
		} else {
			responses = append(responses, r)
		}
		for _, response := range responses {
			entities := peer.EntitiesFromResult(response)
			for _, item := range response.Dialogs {
				if d, ok := item.(*tg.Dialog); ok {
					if fd := folderDialogFrom(d, entities); fd.info.ID != "" {
						available[fd.info.ID] = fd
					}
				}
			}
		}
		for _, p := range batch {
			key := folderInputKey(p)
			if key == "self" {
				for k, d := range available {
					if d.isSelf() {
						key = k
						break
					}
				}
			}
			d, ok := available[key]
			if !ok || !d.known || seen["output:"+key] || deny[key] || (deny["self"] && d.isSelf()) {
				continue
			}
			result = append(result, d.info)
			seen["output:"+key] = true
			if len(result) == limit {
				return result, nil
			}
		}
	}
	if regular == nil || (!regular.Contacts && !regular.NonContacts && !regular.Bots && !regular.Groups && !regular.Broadcasts) {
		return result, nil
	}
	defaults := map[string]*tg.PeerNotifySettings{}
	now := int(time.Now().Unix())
	for peerFolder := range 2 {
		archived := peerFolder == 1
		if archived && regular.ExcludeArchived {
			break
		}
		iter := query.GetDialogs(c.api).FolderID(peerFolder).BatchSize(100).Iter()
		for len(result) < limit && iter.Next(ctx) {
			e := iter.Value()
			d, ok := e.Dialog.(*tg.Dialog)
			if !ok {
				continue
			}
			candidate := folderDialogFrom(d, e.Entities)
			key := candidate.info.ID
			if key == "" || seen["output:"+key] || deny[key] {
				continue
			}
			if deny["self"] && candidate.isSelf() {
				continue
			}
			settingKind, categoryMatch, err := candidate.category(regular)
			if err != nil {
				return nil, err
			}
			if !categoryMatch {
				continue
			}
			if regular.ExcludeRead && d.UnreadCount == 0 && !d.UnreadMark && d.UnreadMentionsCount == 0 {
				continue
			}
			if regular.ExcludeMuted {
				muteUntil, present := d.NotifySettings.GetMuteUntil()
				if !present {
					kind := fmt.Sprintf("%T", settingKind)
					settings := defaults[kind]
					if settings == nil {
						settings, err = c.api.AccountGetNotifySettings(ctx, settingKind)
						if err != nil {
							return nil, fmt.Errorf("get default notification settings: %w", err)
						}
						defaults[kind] = settings
					}
					muteUntil, _ = settings.GetMuteUntil()
				}
				if muteUntil > now && (archived || d.UnreadMentionsCount == 0) {
					continue
				}
			}
			result = append(result, candidate.info)
			seen["output:"+key] = true
		}
		if err := iter.Err(); err != nil {
			return nil, fmt.Errorf("get folder dialogs: %w", err)
		}
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func unavailableFolderPeer(err error) bool {
	return tgerr.Is(err, tg.ErrPeerIDInvalid, tg.ErrChatIDInvalid, tg.ErrChannelPrivate)
}
