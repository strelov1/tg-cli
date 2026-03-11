package main

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
)

// withTelegram creates a client with session storage and calls fn inside a connected session.
func withTelegram(c config, fn func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	appID, err := c.appIDInt()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(c.accountDir(), 0700); err != nil {
		return fmt.Errorf("create account dir: %w", err)
	}

	client := telegram.NewClient(appID, c.apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: c.sessFile()},
	})

	return client.Run(ctx, func(ctx context.Context) error {
		api := client.API()
		pm := peers.Options{}.Build(api)
		return fn(ctx, client, api, pm)
	})
}

// cryptoRandInt63 returns a cryptographically random int64 >= 0.
func cryptoRandInt63() int64 {
	var v int64
	if err := binary.Read(cryptorand.Reader, binary.LittleEndian, &v); err != nil {
		panic(fmt.Sprintf("crypto/rand read: %v", err))
	}
	if v < 0 {
		v = -v
	}
	return v
}

// ── Entity maps ──

// entityMaps holds indexed Telegram entities for fast lookup by ID.
type entityMaps struct {
	users    map[int64]*tg.User
	chats    map[int64]*tg.Chat
	channels map[int64]*tg.Channel
}

func buildEntityMaps(users []tg.UserClass, chats []tg.ChatClass) entityMaps {
	em := entityMaps{
		users:    make(map[int64]*tg.User),
		chats:    make(map[int64]*tg.Chat),
		channels: make(map[int64]*tg.Channel),
	}
	for _, u := range users {
		if v, ok := u.(*tg.User); ok {
			em.users[v.ID] = v
		}
	}
	for _, c := range chats {
		switch v := c.(type) {
		case *tg.Chat:
			em.chats[v.ID] = v
		case *tg.Channel:
			em.channels[v.ID] = v
		}
	}
	return em
}

func (em entityMaps) senderName(fromID tg.PeerClass) string {
	if fromID == nil {
		return ""
	}
	switch p := fromID.(type) {
	case *tg.PeerUser:
		if u, ok := em.users[p.UserID]; ok {
			return strings.TrimSpace(u.FirstName + " " + u.LastName)
		}
		return fmt.Sprintf("user:%d", p.UserID)
	case *tg.PeerChannel:
		if ch, ok := em.channels[p.ChannelID]; ok {
			return ch.Title
		}
	case *tg.PeerChat:
		if c, ok := em.chats[p.ChatID]; ok {
			return c.Title
		}
	}
	return "?"
}

// ── Message helpers ──

func extractHistoryMessages(r tg.MessagesMessagesClass) ([]tg.MessageClass, []tg.ChatClass, []tg.UserClass, error) {
	switch v := r.(type) {
	case *tg.MessagesMessages:
		return v.Messages, v.Chats, v.Users, nil
	case *tg.MessagesMessagesSlice:
		return v.Messages, v.Chats, v.Users, nil
	case *tg.MessagesChannelMessages:
		return v.Messages, v.Chats, v.Users, nil
	case *tg.MessagesMessagesNotModified:
		return nil, nil, nil, nil
	default:
		return nil, nil, nil, fmt.Errorf("unexpected messages type: %T", r)
	}
}

// tgMsg is the JSON-serializable representation of a single Telegram message.
type tgMsg struct {
	ID   int    `json:"id"`
	Who  string `json:"who"`
	When string `json:"when"`
	Text string `json:"text"`
}

func formatMessages(msgs []tg.MessageClass, em entityMaps) []tgMsg {
	var out []tgMsg
	for _, m := range msgs {
		msg, ok := m.(*tg.Message)
		if !ok {
			continue
		}
		text := msg.Message
		if text == "" {
			continue // skip service messages and empty
		}
		sender := ""
		if msg.FromID != nil {
			sender = em.senderName(msg.FromID)
		}
		out = append(out, tgMsg{
			ID:   msg.ID,
			Who:  sender,
			When: time.Unix(int64(msg.Date), 0).UTC().Format(time.RFC3339),
			Text: text,
		})
	}
	return out
}

// ── Peer resolution ──

// resolvePeer resolves a username, phone number, or t.me URL to a Telegram peer.
func resolvePeer(ctx context.Context, pm *peers.Manager, name string) (peers.Peer, error) {
	// Strip https://t.me/<username> → <username> for non-invite links
	query := name
	if strings.HasPrefix(query, "https://t.me/") && !strings.Contains(query, "/+") && !strings.Contains(query, "/joinchat/") {
		query = strings.TrimPrefix(query, "https://t.me/")
	}
	if !strings.HasPrefix(query, "https://") && !strings.HasPrefix(query, "t.me/") {
		query = strings.TrimPrefix(query, "@")
	}
	p, err := pm.Resolve(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("cannot find %q: %w", name, err)
	}
	return p, nil
}

// extractInviteHash extracts the hash from a t.me invite link.
// Returns "" if the target is not an invite link.
func extractInviteHash(target string) string {
	for _, prefix := range []string{"https://t.me/+", "t.me/+", "https://t.me/joinchat/", "t.me/joinchat/"} {
		if strings.HasPrefix(target, prefix) {
			return strings.TrimPrefix(target, prefix)
		}
	}
	return ""
}

// ── Output ──

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
