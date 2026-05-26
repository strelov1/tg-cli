package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
)

// extractChatlistSlug parses a chat folder share link or bare slug.
// Accepted forms:
//
//	https://t.me/addlist/<slug>
//	t.me/addlist/<slug>
//	addlist/<slug>
//	<slug>
func extractChatlistSlug(s string) string {
	s = strings.TrimSpace(s)
	for _, prefix := range []string{"https://", "http://"} {
		s = strings.TrimPrefix(s, prefix)
	}
	s = strings.TrimPrefix(s, "t.me/")
	s = strings.TrimPrefix(s, "telegram.me/")
	s = strings.TrimPrefix(s, "addlist/")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

func summarizeChannel(ch *tg.Channel) map[string]any {
	t := "channel"
	if ch.Megagroup {
		t = "supergroup"
	} else if ch.Gigagroup {
		t = "gigagroup"
	}
	out := map[string]any{
		"id":          ch.ID,
		"username":    ch.Username,
		"title":       ch.Title,
		"type":        t,
		"verified":    ch.Verified,
		"scam":        ch.Scam,
		"fake":        ch.Fake,
		"access_hash": ch.AccessHash,
	}
	if alts, ok := ch.GetUsernames(); ok {
		var names []string
		for _, u := range alts {
			names = append(names, u.Username)
		}
		if len(names) > 0 {
			out["usernames"] = names
		}
	}
	if ch.ParticipantsCount != 0 {
		out["members"] = ch.ParticipantsCount
	}
	return out
}

// resolveChatlistInvite walks a checkChatlistInvite response and returns
// (title, channels []map, alreadyJoined bool, filterID int).
func resolveChatlistInvite(resp tg.ChatlistsChatlistInviteClass) (string, []map[string]any, []*tg.Channel, bool, int) {
	var (
		title         string
		alreadyJoined bool
		filterID      int
		peerList      []tg.PeerClass
		chats         []tg.ChatClass
	)
	switch v := resp.(type) {
	case *tg.ChatlistsChatlistInvite:
		title = v.Title.Text
		peerList = v.Peers
		chats = v.Chats
	case *tg.ChatlistsChatlistInviteAlready:
		alreadyJoined = true
		filterID = v.FilterID
		// missing first, then already
		peerList = append([]tg.PeerClass{}, v.MissingPeers...)
		peerList = append(peerList, v.AlreadyPeers...)
		chats = v.Chats
	}

	chanLookup := make(map[int64]*tg.Channel)
	for _, ch := range chats {
		if c, ok := ch.(*tg.Channel); ok {
			chanLookup[c.ID] = c
		}
	}

	var items []map[string]any
	var channels []*tg.Channel
	for _, p := range peerList {
		pc, ok := p.(*tg.PeerChannel)
		if !ok {
			continue
		}
		ch, ok := chanLookup[pc.ChannelID]
		if !ok {
			continue
		}
		items = append(items, summarizeChannel(ch))
		channels = append(channels, ch)
	}
	return title, items, channels, alreadyJoined, filterID
}

func cmdChatlistPreview(c config, urlOrSlug string) error {
	slug := extractChatlistSlug(urlOrSlug)
	if slug == "" {
		return fmt.Errorf("empty slug — pass an addlist URL or slug")
	}
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		resp, err := api.ChatlistsCheckChatlistInvite(ctx, slug)
		if err != nil {
			return err
		}
		title, items, _, alreadyJoined, filterID := resolveChatlistInvite(resp)
		out := map[string]any{
			"slug":           slug,
			"title":          title,
			"already_joined": alreadyJoined,
			"channels":       items,
			"count":          len(items),
		}
		if alreadyJoined {
			out["filter_id"] = filterID
		}
		return printJSON(out)
	})
}

func cmdChatlistJoin(c config, urlOrSlug string, peerFilter []string, dryRun bool) error {
	slug := extractChatlistSlug(urlOrSlug)
	if slug == "" {
		return fmt.Errorf("empty slug — pass an addlist URL or slug")
	}
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		resp, err := api.ChatlistsCheckChatlistInvite(ctx, slug)
		if err != nil {
			return err
		}
		title, _, channels, alreadyJoined, filterID := resolveChatlistInvite(resp)

		// optional username filter
		filterSet := make(map[string]bool)
		for _, p := range peerFilter {
			filterSet[strings.ToLower(strings.TrimPrefix(p, "@"))] = true
		}

		var inputPeers []tg.InputPeerClass
		var picked []map[string]any
		for _, ch := range channels {
			if len(filterSet) > 0 && !filterSet[strings.ToLower(ch.Username)] {
				continue
			}
			inputPeers = append(inputPeers, &tg.InputPeerChannel{
				ChannelID:  ch.ID,
				AccessHash: ch.AccessHash,
			})
			picked = append(picked, summarizeChannel(ch))
		}

		if len(inputPeers) == 0 {
			return fmt.Errorf("no channels matched filter")
		}

		out := map[string]any{
			"slug":           slug,
			"title":          title,
			"already_joined": alreadyJoined,
			"count":          len(inputPeers),
			"channels":       picked,
		}
		if alreadyJoined {
			out["filter_id"] = filterID
		}
		if dryRun {
			out["status"] = "dry-run"
			return printJSON(out)
		}

		_, err = api.ChatlistsJoinChatlistInvite(ctx, &tg.ChatlistsJoinChatlistInviteRequest{
			Slug:  slug,
			Peers: inputPeers,
		})
		if err != nil {
			return err
		}
		out["status"] = "joined"
		return printJSON(out)
	})
}
