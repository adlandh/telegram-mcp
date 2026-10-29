package domain

import "testing"

func TestChatHasUnread(t *testing.T) {
	for _, tc := range []struct {
		chat Chat
		want bool
	}{
		{Chat{Unread: 2}, true},
		{Chat{UnreadMark: true}, true},
		{Chat{}, false},
	} {
		if got := tc.chat.HasUnread(); got != tc.want {
			t.Errorf("%+v: HasUnread() = %t", tc.chat, got)
		}
	}
}
