package main

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
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

// senderID returns the numeric ID of the sender peer.
func senderID(fromID tg.PeerClass) int64 {
	if fromID == nil {
		return 0
	}
	switch p := fromID.(type) {
	case *tg.PeerUser:
		return p.UserID
	case *tg.PeerChannel:
		return p.ChannelID
	case *tg.PeerChat:
		return p.ChatID
	}
	return 0
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

// reactionInfo is a single emoji reaction with its count.
type reactionInfo struct {
	Emoji string `json:"emoji"`
	Count int    `json:"count"`
}

// reactionEmoji extracts a string label from a ReactionClass.
func reactionEmoji(r tg.ReactionClass) string {
	switch re := r.(type) {
	case *tg.ReactionEmoji:
		return re.Emoticon
	case *tg.ReactionCustomEmoji:
		return fmt.Sprintf("custom:%d", re.DocumentID)
	default:
		return ""
	}
}

// tgMsg is the JSON-serializable representation of a single Telegram message.
type tgMsg struct {
	ID         int            `json:"id"`
	Who        string         `json:"who"`
	WhoID      int64          `json:"who_id,omitempty"`
	When       string         `json:"when"`
	Text       string         `json:"text"`
	Views      int            `json:"views,omitempty"`
	Forwards   int            `json:"forwards,omitempty"`
	ReplyTo    int            `json:"reply_to,omitempty"`
	PostAuthor string         `json:"post_author,omitempty"`
	Reactions  []reactionInfo `json:"reactions,omitempty"`
}

// tgMsgFull is a richer message representation that includes media info and metadata.
type tgMsgFull struct {
	ID         int            `json:"id"`
	Who        string         `json:"who,omitempty"`
	WhoID      int64          `json:"who_id,omitempty"`
	When       string         `json:"when"`
	Text       string         `json:"text,omitempty"`
	MediaType  string         `json:"media_type,omitempty"`
	Views      int            `json:"views,omitempty"`
	Forwards   int            `json:"forwards,omitempty"`
	ReplyTo    int            `json:"reply_to,omitempty"`
	PostAuthor string         `json:"post_author,omitempty"`
	Reactions  []reactionInfo `json:"reactions,omitempty"`
}

// formatMessagesFull formats all messages (including media-only), with media type and metadata.
func formatMessagesFull(msgs []tg.MessageClass, em entityMaps) []tgMsgFull {
	var out []tgMsgFull
	for _, m := range msgs {
		msg, ok := m.(*tg.Message)
		if !ok || msg.ID == 0 {
			continue
		}
		item := tgMsgFull{
			ID:   msg.ID,
			When: time.Unix(int64(msg.Date), 0).UTC().Format(time.RFC3339),
			Text: msg.Message,
		}
		if msg.FromID != nil {
			item.Who = em.senderName(msg.FromID)
			item.WhoID = senderID(msg.FromID)
		}
		if msg.Media != nil {
			item.MediaType = mediaTypeName(msg.Media)
		}
		if item.Text == "" && item.MediaType == "" {
			continue
		}
		if v, ok := msg.GetViews(); ok {
			item.Views = v
		}
		if f, ok := msg.GetForwards(); ok {
			item.Forwards = f
		}
		if pa, ok := msg.GetPostAuthor(); ok {
			item.PostAuthor = pa
		}
		item.ReplyTo = msgReplyTo(msg)
		item.Reactions = msgReactions(msg)
		out = append(out, item)
	}
	return out
}

// mediaTypeName returns a short label for a Telegram media type.
func mediaTypeName(m tg.MessageMediaClass) string {
	switch m.(type) {
	case *tg.MessageMediaPhoto:
		return "photo"
	case *tg.MessageMediaDocument:
		return "document"
	case *tg.MessageMediaGeo:
		return "geo"
	case *tg.MessageMediaContact:
		return "contact"
	case *tg.MessageMediaPoll:
		return "poll"
	case *tg.MessageMediaWebPage:
		return "webpage"
	case *tg.MessageMediaDice:
		return "dice"
	case *tg.MessageMediaVenue:
		return "venue"
	case *tg.MessageMediaGeoLive:
		return "geo_live"
	case *tg.MessageMediaStory:
		return "story"
	case *tg.MessageMediaEmpty, nil:
		return ""
	default:
		return "other"
	}
}

// msgReactions extracts reaction info from a tg.Message.
func msgReactions(msg *tg.Message) []reactionInfo {
	reactions, ok := msg.GetReactions()
	if !ok {
		return nil
	}
	var out []reactionInfo
	for _, r := range reactions.Results {
		if e := reactionEmoji(r.Reaction); e != "" {
			out = append(out, reactionInfo{Emoji: e, Count: r.Count})
		}
	}
	return out
}

// msgReplyTo extracts the reply-to message ID from a tg.Message.
func msgReplyTo(msg *tg.Message) int {
	if msg.ReplyTo == nil {
		return 0
	}
	if rh, ok := msg.ReplyTo.(*tg.MessageReplyHeader); ok {
		return rh.ReplyToMsgID
	}
	return 0
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
		var senderIDVal int64
		if msg.FromID != nil {
			sender = em.senderName(msg.FromID)
			senderIDVal = senderID(msg.FromID)
		}
		item := tgMsg{
			ID:    msg.ID,
			Who:   sender,
			WhoID: senderIDVal,
			When:  time.Unix(int64(msg.Date), 0).UTC().Format(time.RFC3339),
			Text:  text,
		}
		if v, ok := msg.GetViews(); ok {
			item.Views = v
		}
		if f, ok := msg.GetForwards(); ok {
			item.Forwards = f
		}
		if pa, ok := msg.GetPostAuthor(); ok {
			item.PostAuthor = pa
		}
		item.ReplyTo = msgReplyTo(msg)
		item.Reactions = msgReactions(msg)
		out = append(out, item)
	}
	return out
}

// ── Peer resolution ──

// resolvePeer resolves a username, phone number, t.me URL, or numeric ID to a Telegram peer.
// Numeric IDs are tried as regular chat, user, and channel in order.
// Falls back to loading recent dialogs to find entities not yet in the local peer store.
func resolvePeer(ctx context.Context, pm *peers.Manager, api *tg.Client, name string) (peers.Peer, error) {
	// Strip https://t.me/<username> → <username> for non-invite links
	query := name
	if strings.HasPrefix(query, "https://t.me/") && !strings.Contains(query, "/+") && !strings.Contains(query, "/joinchat/") {
		query = strings.TrimPrefix(query, "https://t.me/")
	}
	if !strings.HasPrefix(query, "https://") && !strings.HasPrefix(query, "t.me/") {
		query = strings.TrimPrefix(query, "@")
	}

	// If it looks like a bare numeric ID, try all peer types.
	if id, err := strconv.ParseInt(query, 10, 64); err == nil {
		// Try as regular chat (works without access hash via MessagesGetChats)
		if chat, err := pm.GetChat(ctx, id); err == nil {
			return chat, nil
		}
		// Try as user (uses stored access hash if available)
		if user, err := pm.ResolveUserID(ctx, id); err == nil {
			return user, nil
		}
		// Try as channel (uses stored access hash if available)
		if channel, err := pm.ResolveChannelID(ctx, id); err == nil {
			return channel, nil
		}
		// Fallback: fetch recent dialogs to populate peer cache, then search by ID
		if peer, err := resolveIDViaDialogs(ctx, pm, api, id); err == nil {
			return peer, nil
		}
	}

	p, err := pm.Resolve(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("cannot find %q: %w", name, err)
	}
	return p, nil
}

// resolveIDViaDialogs fetches up to 200 recent dialogs to find a peer by numeric ID.
// It also populates the peer manager's cache so subsequent lookups succeed.
func resolveIDViaDialogs(ctx context.Context, pm *peers.Manager, api *tg.Client, id int64) (peers.Peer, error) {
	resp, err := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		Limit:      200,
		OffsetPeer: &tg.InputPeerEmpty{},
	})
	if err != nil {
		return nil, err
	}

	var users []tg.UserClass
	var chats []tg.ChatClass
	switch d := resp.(type) {
	case *tg.MessagesDialogs:
		users, chats = d.Users, d.Chats
	case *tg.MessagesDialogsSlice:
		users, chats = d.Users, d.Chats
	default:
		return nil, fmt.Errorf("unexpected dialogs type %T", resp)
	}

	// Populate the peer manager's cache
	if err := pm.Apply(ctx, users, chats); err != nil {
		return nil, err
	}

	// Search for the entity with matching ID
	for _, u := range users {
		if usr, ok := u.(*tg.User); ok && usr.ID == id {
			return pm.User(usr), nil
		}
	}
	for _, ch := range chats {
		switch c := ch.(type) {
		case *tg.Channel:
			if c.ID == id {
				return pm.Channel(c), nil
			}
		case *tg.Chat:
			if c.ID == id {
				return pm.Chat(c), nil
			}
		}
	}

	return nil, fmt.Errorf("peer with ID %d not found in recent dialogs (try @username)", id)
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
