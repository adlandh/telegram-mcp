package telegram

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/adlandh/telegram-mcp/internal/domain"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/dialogs"
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
	users  peer.Entities
	dialog *tg.Dialog
	resolved
}

func folderDialogFrom(d *tg.Dialog, entities peer.Entities) folderDialog {
	r := describe(d.Peer, entities)
	r.info.Unread, r.info.UnreadMark = d.UnreadCount, d.UnreadMark

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
		return d.userCategory(p, f)
	case *tg.PeerChat:
		if _, ok := d.users.Chat(p.ChatID); !ok {
			return nil, false, fmt.Errorf("chat metadata unavailable for folder matching")
		}

		return &tg.InputNotifyChats{}, f.Groups, nil
	case *tg.PeerChannel:
		return d.channelCategory(p, f)
	}

	return nil, false, fmt.Errorf("unsupported dialog peer for folder matching")
}

func (d folderDialog) userCategory(p *tg.PeerUser, f *tg.DialogFilter) (tg.InputNotifyPeerClass, bool, error) {
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
}

func (d folderDialog) channelCategory(p *tg.PeerChannel, f *tg.DialogFilter) (tg.InputNotifyPeerClass, bool, error) {
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

func (c *Client) FolderDialogs(ctx context.Context, folderID, limit int, unreadOnly bool) ([]domain.Chat, error) {
	filters, err := c.api.MessagesGetDialogFilters(ctx)
	if err != nil {
		return nil, fmt.Errorf("get folder filters: %w", err)
	}

	peers, regular, found := findFolder(filters.Filters, folderID)
	if !found {
		return nil, fmt.Errorf("folder %d not found; refresh list_folders", folderID)
	}

	deny, err := denied(regular)
	if err != nil {
		return nil, err
	}

	ordered, err := orderPeers(peers, deny)
	if err != nil {
		return nil, err
	}

	s := &folderScan{
		c: c, regular: regular, deny: deny, emitted: map[string]bool{}, available: map[string]folderDialog{},
		result: make([]domain.Chat, 0, min(limit, len(ordered)+100)), limit: limit, unreadOnly: unreadOnly,
	}

	if err := s.explicit(ctx, ordered); err != nil {
		return nil, err
	}

	if s.full() || !hasCategories(regular) {
		return s.result, nil
	}

	if err := s.categories(ctx); err != nil {
		return nil, err
	}

	return s.result, nil
}

// findFolder returns the folder's pinned and included peers and, for a regular folder, its filter.
func findFolder(filters []tg.DialogFilterClass, folderID int) ([]tg.InputPeerClass, *tg.DialogFilter, bool) {
	for _, candidate := range filters {
		switch f := candidate.(type) {
		case *tg.DialogFilter:
			if f.ID == folderID {
				return slices.Concat(f.PinnedPeers, f.IncludePeers), f, true
			}
		case *tg.DialogFilterChatlist:
			if f.ID == folderID {
				return slices.Concat(f.PinnedPeers, f.IncludePeers), nil, true
			}
		}
	}

	return nil, nil, false
}

// denied returns the keys of a regular folder's excluded peers.
func denied(regular *tg.DialogFilter) (map[string]bool, error) {
	deny := map[string]bool{}
	if regular == nil {
		return deny, nil
	}

	for _, p := range regular.ExcludePeers {
		key := folderInputKey(p)
		if key == "" {
			return nil, fmt.Errorf("unsupported excluded peer %T", p)
		}

		deny[key] = true
	}

	return deny, nil
}

// orderPeers drops duplicate and excluded peers, keeping pinned peers first.
func orderPeers(peers []tg.InputPeerClass, deny map[string]bool) ([]tg.InputPeerClass, error) {
	seen := make(map[string]bool)

	ordered := make([]tg.InputPeerClass, 0, len(peers))
	for _, p := range peers {
		key := folderInputKey(p)
		if key == "" {
			return nil, fmt.Errorf("unsupported included peer %T", p)
		}

		if !seen[key] && !deny[key] {
			ordered = append(ordered, p)
			seen[key] = true
		}
	}

	return ordered, nil
}

func hasCategories(f *tg.DialogFilter) bool {
	return f != nil && (f.Contacts || f.NonContacts || f.Bots || f.Groups || f.Broadcasts)
}

// folderScan holds the state of one FolderDialogs walk.
type folderScan struct {
	c          *Client
	regular    *tg.DialogFilter
	deny       map[string]bool
	emitted    map[string]bool
	available  map[string]folderDialog
	defaults   map[string]*tg.PeerNotifySettings
	result     []domain.Chat
	limit      int
	now        int
	unreadOnly bool
}

func (s *folderScan) full() bool {
	return len(s.result) >= s.limit
}

// emit marks key as handled and keeps info unless unreadOnly filters it out.
func (s *folderScan) emit(key string, info domain.Chat) {
	s.emitted[key] = true

	if s.unreadOnly && !info.HasUnread() {
		return
	}

	s.result = append(s.result, info)
}

// explicit emits the folder's pinned and included peers in order.
func (s *folderScan) explicit(ctx context.Context, ordered []tg.InputPeerClass) error {
	for offset := 0; offset < len(ordered) && !s.full(); offset += 100 {
		batch := ordered[offset:min(offset+100, len(ordered))]

		responses, err := s.c.peerDialogs(ctx, batch)
		if err != nil {
			return err
		}

		s.collect(responses)

		for _, p := range batch {
			if s.addExplicit(p); s.full() {
				return nil
			}
		}
	}

	return nil
}

// peerDialogs fetches the dialogs of peers, skipping peers Telegram no longer exposes.
func (c *Client) peerDialogs(ctx context.Context, peers []tg.InputPeerClass) ([]*tg.MessagesPeerDialogs, error) {
	requests := make([]tg.InputDialogPeerClass, 0, len(peers))
	for _, p := range peers {
		requests = append(requests, &tg.InputDialogPeer{Peer: p})
	}

	r, err := c.api.MessagesGetPeerDialogs(ctx, requests)

	switch {
	case unavailableFolderPeer(err):
		// A stale peer can reject the whole batch; isolate it without hiding other failures.
		responses := make([]*tg.MessagesPeerDialogs, 0, len(requests))

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

		return responses, nil
	case err != nil:
		return nil, fmt.Errorf("get folder peers: %w", err)
	}

	return []*tg.MessagesPeerDialogs{r}, nil
}

func (s *folderScan) collect(responses []*tg.MessagesPeerDialogs) {
	for _, response := range responses {
		entities := peer.EntitiesFromResult(response)
		for _, item := range response.Dialogs {
			if d, ok := item.(*tg.Dialog); ok {
				if fd := folderDialogFrom(d, entities); fd.info.ID != "" {
					s.available[fd.info.ID] = fd
				}
			}
		}
	}
}

// selfKey returns the marked ID of the account's own dialog, or "self" when it was not fetched.
func (s *folderScan) selfKey() string {
	for k, d := range s.available {
		if d.isSelf() {
			return k
		}
	}

	return "self"
}

func (s *folderScan) addExplicit(p tg.InputPeerClass) {
	key := folderInputKey(p)
	if key == "self" {
		key = s.selfKey()
	}

	d, ok := s.available[key]
	if !ok || !d.known || s.emitted[key] || s.deny[key] || (s.deny["self"] && d.isSelf()) {
		return
	}
	// Folder entries may be InputPeerSelf or *FromMessage, which remember skips;
	// the response entities give the plain peer with its access hash.
	d.input, _ = d.users.ExtractPeer(d.dialog.Peer)
	s.c.remember(d.resolved)
	s.emit(key, d.info)
}

// categories emits main-list and, unless excluded, archived dialogs matching the folder's chat types.
func (s *folderScan) categories(ctx context.Context) error {
	s.defaults = map[string]*tg.PeerNotifySettings{}
	s.now = int(time.Now().Unix())

	for peerFolder := range 2 {
		archived := peerFolder == 1
		if archived && s.regular.ExcludeArchived {
			return nil
		}

		iter := query.GetDialogs(s.c.api).FolderID(peerFolder).BatchSize(100).Iter()
		for !s.full() && iter.Next(ctx) {
			if err := s.addCategory(ctx, iter.Value(), archived); err != nil {
				return err
			}
		}

		if err := iter.Err(); err != nil {
			return fmt.Errorf("get folder dialogs: %w", err)
		}

		if s.full() {
			return nil
		}
	}

	return nil
}

func (s *folderScan) addCategory(ctx context.Context, e dialogs.Elem, archived bool) error {
	d, ok := e.Dialog.(*tg.Dialog)
	if !ok {
		return nil
	}

	candidate := folderDialogFrom(d, e.Entities)

	key := candidate.info.ID
	if key == "" || s.emitted[key] || s.deny[key] || (s.deny["self"] && candidate.isSelf()) {
		return nil
	}

	match, err := s.matches(ctx, candidate, archived)
	if err != nil || !match {
		return err
	}

	candidate.input = e.Peer
	s.c.remember(candidate.resolved)
	s.emit(key, candidate.info)

	return nil
}

// matches applies the folder's chat-type, read, and muted rules to a dialog.
func (s *folderScan) matches(ctx context.Context, candidate folderDialog, archived bool) (bool, error) {
	settingKind, categoryMatch, err := candidate.category(s.regular)
	if err != nil || !categoryMatch {
		return false, err
	}

	d := candidate.dialog
	if s.regular.ExcludeRead && !unread(d) {
		return false, nil
	}

	if !s.regular.ExcludeMuted {
		return true, nil
	}

	muteUntil, err := s.muteUntil(ctx, d, settingKind)
	if err != nil {
		return false, err
	}

	return muteUntil <= s.now || (!archived && d.UnreadMentionsCount > 0), nil
}

func unread(d *tg.Dialog) bool {
	return d.UnreadCount > 0 || d.UnreadMark || d.UnreadMentionsCount > 0
}

// muteUntil returns the dialog's mute deadline, falling back to the default for its chat type.
func (s *folderScan) muteUntil(ctx context.Context, d *tg.Dialog, settingKind tg.InputNotifyPeerClass) (int, error) {
	if muteUntil, ok := d.NotifySettings.GetMuteUntil(); ok {
		return muteUntil, nil
	}

	kind := fmt.Sprintf("%T", settingKind)

	settings := s.defaults[kind]
	if settings == nil {
		var err error

		settings, err = s.c.api.AccountGetNotifySettings(ctx, settingKind)
		if err != nil {
			return 0, fmt.Errorf("get default notification settings: %w", err)
		}

		s.defaults[kind] = settings
	}

	muteUntil, _ := settings.GetMuteUntil()

	return muteUntil, nil
}

func unavailableFolderPeer(err error) bool {
	return tgerr.Is(err, tg.ErrPeerIDInvalid, tg.ErrChatIDInvalid, tg.ErrChannelPrivate)
}
