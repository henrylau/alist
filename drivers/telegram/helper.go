package telegram

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/tg"
)

type helper struct{}

var Helper helper

func (h helper) GetInputPeer(ctx context.Context, manager *peers.Manager, from string) (peers.Peer, error) {
	id, err := strconv.ParseInt(from, 10, 64)
	if err != nil {
		// from is username
		p, err := manager.Resolve(ctx, from)
		if err != nil {
			return nil, err
		}

		return p, nil
	}

	var p peers.Peer
	if p, err = manager.ResolveChannelID(ctx, id); err == nil {
		return p, nil
	}
	if p, err = manager.ResolveUserID(ctx, id); err == nil {
		return p, nil
	}
	if p, err = manager.ResolveChatID(ctx, id); err == nil {
		return p, nil
	}

	return nil, fmt.Errorf("failed to get result from %d：%v", id, err)
}

func (h helper) GetPeerID(peer tg.PeerClass) int64 {
	switch p := peer.(type) {
	case *tg.PeerUser:
		return p.UserID
	case *tg.PeerChat:
		return p.ChatID
	case *tg.PeerChannel:
		return p.ChannelID
	}
	return 0
}

func (h helper) GetInputPeerID(peer tg.InputPeerClass) int64 {
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		return p.UserID
	case *tg.InputPeerChat:
		return p.ChatID
	case *tg.InputPeerChannel:
		return p.ChannelID
	}

	return 0
}

func (t helper) GetBlockedDialogs(ctx context.Context, client *tg.Client) (map[int64]struct{}, error) {
	blocks, err := query.GetBlocked(client).BatchSize(100).Collect(ctx)
	if err != nil {
		return nil, err
	}

	blockids := make(map[int64]struct{})
	for _, b := range blocks {
		blockids[t.GetPeerID(b.Contact.PeerID)] = struct{}{}
	}
	return blockids, nil
}

func (t helper) GetName(first, last, username string) string {
	if name := first + " " + last; name != " " {
		return name
	}
	return username
}

func (t helper) GetPeerName(id int64, e peer.Entities) string {
	if n, ok := e.Users()[id]; ok {
		return t.GetName(n.FirstName, n.LastName, n.Username)
	}

	if n, ok := e.Channels()[id]; ok {
		return n.Title
	}

	if n, ok := e.Chats()[id]; ok {
		return n.Title
	}

	return ""
}

func (t helper) GetPeerPhoto(id int64, e peer.Entities) []byte {

	if n, ok := e.User(id); ok {
		switch p := n.Photo.(type) {
		case *tg.UserProfilePhoto:
			return p.StrippedThumb
		}
	}

	if n, ok := e.Channel(id); ok {
		switch p := n.Photo.(type) {
		case *tg.ChatPhoto:
			return p.StrippedThumb
		}
	}

	if n, ok := e.Chat(id); ok {
		switch p := n.Photo.(type) {
		case *tg.ChatPhoto:
			return p.StrippedThumb
		}
	}
	return nil

}

func (t helper) GetPeerType(id int64, e peer.Entities) string {
	if _, ok := e.User(id); ok {
		return "ChatPrivate"
	}

	if n, ok := e.Channel(id); ok {
		if n.Megagroup || n.Gigagroup {
			return "ChatGroup"
		}
		return "ChatChannel"
	}

	if _, ok := e.Chat(id); ok {
		return "ChatGroup"
	}

	return "ChatUnknown"
}

func (t helper) FileExists(msg tg.MessageClass) bool {
	m, ok := msg.(*tg.Message)
	if !ok {
		return false
	}

	md, ok := m.GetMedia()
	if !ok {
		return false
	}

	switch md.(type) {
	case *tg.MessageMediaDocument, *tg.MessageMediaPhoto:
		return true
	default:
		return false
	}
}

func RoundUp(num, multiple int) int {
	return (num + multiple - 1) / multiple * multiple
}
