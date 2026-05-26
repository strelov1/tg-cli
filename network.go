package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
)

var _ = telegram.RunUntilCanceled
var _ peers.Manager

func cmdDownloadNetwork(
	c config,
	urlOrSlug string,
	outDir string,
	perChannelLimit int,
	skipMedia bool,
	skipExisting bool,
	peerFilter []string,
	publicOnly bool,
	resume bool,
	autoJoin bool,
	pauseSec int,
) error {
	if outDir == "" {
		return fmt.Errorf("--out is required")
	}
	slug := extractChatlistSlug(urlOrSlug)
	if slug == "" {
		return fmt.Errorf("empty slug")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	filterSet := make(map[string]bool)
	for _, p := range peerFilter {
		filterSet[strings.ToLower(strings.TrimPrefix(p, "@"))] = true
	}

	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		resp, err := api.ChatlistsCheckChatlistInvite(ctx, slug)
		if err != nil {
			return fmt.Errorf("check chatlist invite: %w", err)
		}
		title, _, channels, alreadyJoined, _ := resolveChatlistInvite(resp)

		var picked []*tg.Channel
		for _, ch := range channels {
			if publicOnly && ch.Username == "" {
				continue
			}
			if len(filterSet) > 0 && !filterSet[strings.ToLower(ch.Username)] {
				continue
			}
			picked = append(picked, ch)
		}
		sort.Slice(picked, func(i, j int) bool {
			a, b := picked[i].Username, picked[j].Username
			if a == b {
				return picked[i].ID < picked[j].ID
			}
			return a < b
		})

		networkPath := filepath.Join(outDir, "network.json")
		if err := writeJSONAtomic(networkPath, map[string]any{
			"slug":           slug,
			"title":          title,
			"already_joined": alreadyJoined,
			"channels":       summarizeChannels(picked),
			"count":          len(picked),
			"dumped_by":      c.account,
			"dumped_at":      time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "Network %q: %d channels (out of %d in folder)\n", title, len(picked), len(channels))

		if autoJoin && !alreadyJoined && len(picked) > 0 {
			var ips []tg.InputPeerClass
			for _, ch := range picked {
				ips = append(ips, &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash})
			}
			if _, jerr := api.ChatlistsJoinChatlistInvite(ctx, &tg.ChatlistsJoinChatlistInviteRequest{
				Slug:  slug,
				Peers: ips,
			}); jerr != nil {
				fmt.Fprintf(os.Stderr, "Warning: join chatlist failed: %v\n", jerr)
			} else {
				fmt.Fprintf(os.Stderr, "Joined %d channels\n", len(ips))
			}
		}

		_ = pm
		_ = client
		d := downloader.NewDownloader().WithPartSize(512 * 1024)
		report := []map[string]any{}
		for i, ch := range picked {
			label := ch.Username
			if label == "" {
				label = fmt.Sprintf("id_%d", ch.ID)
			}
			channelDir := filepath.Join(outDir, label)
			messagesPath := filepath.Join(channelDir, "messages.json")
			if skipExisting && !resume {
				if _, statErr := os.Stat(messagesPath); statErr == nil {
					fmt.Fprintf(os.Stderr, "[%d/%d] %s — skip (already dumped)\n", i+1, len(picked), label)
					report = append(report, map[string]any{"channel": label, "status": "skipped"})
					continue
				}
			}

			fmt.Fprintf(os.Stderr, "[%d/%d] %s — dumping…\n", i+1, len(picked), label)
			err := dumpOneChannel(ctx, c, api, d, ch, channelDir, perChannelLimit, skipMedia, resume)
			entry := map[string]any{"channel": label}
			if err != nil {
				entry["status"] = "error"
				entry["error"] = err.Error()
				fmt.Fprintf(os.Stderr, "  ! %s: %v\n", label, err)
			} else {
				entry["status"] = "ok"
			}
			report = append(report, entry)
			_ = writeJSONAtomic(filepath.Join(outDir, "report.json"), report)

			if pauseSec > 0 && i < len(picked)-1 {
				time.Sleep(time.Duration(pauseSec) * time.Second)
			}
		}

		return printJSON(map[string]any{
			"status":   "done",
			"network":  title,
			"channels": len(picked),
			"out_dir":  outDir,
		})
	})
}

func summarizeChannels(chs []*tg.Channel) []map[string]any {
	out := make([]map[string]any, 0, len(chs))
	for _, ch := range chs {
		out = append(out, summarizeChannel(ch))
	}
	return out
}

// dumpOneChannel mirrors cmdDownloadChannel logic but reuses the active session.
func dumpOneChannel(
	ctx context.Context,
	c config,
	api *tg.Client,
	d *downloader.Downloader,
	ch *tg.Channel,
	outDir string,
	limit int,
	skipMedia bool,
	resume bool,
) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	mediaDir := filepath.Join(outDir, "media")
	if !skipMedia {
		if err := os.MkdirAll(mediaDir, 0o755); err != nil {
			return err
		}
	}

	inputCh := &tg.InputChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}
	inputPeer := &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}

	meta := map[string]any{
		"id":        ch.ID,
		"title":     ch.Title,
		"username":  ch.Username,
		"type":      "channel",
		"members":   ch.ParticipantsCount,
		"dumped_by": c.account,
		"dumped_at": time.Now().UTC().Format(time.RFC3339),
	}
	if ch.Megagroup {
		meta["type"] = "supergroup"
	}
	if full, ferr := api.ChannelsGetFullChannel(ctx, inputCh); ferr == nil {
		if fc, ok := full.FullChat.(*tg.ChannelFull); ok {
			meta["description"] = fc.About
			meta["members"] = fc.ParticipantsCount
			meta["pinned_msg_id"] = fc.PinnedMsgID
			meta["read_inbox_max_id"] = fc.ReadInboxMaxID
		}
	}
	if err := writeJSON(filepath.Join(outDir, "meta.json"), meta); err != nil {
		return err
	}

	messagesPath := filepath.Join(outDir, "messages.json")
	seen := map[int]bool{}
	var dumped []dumpedMsg
	if resume {
		if data, err := os.ReadFile(messagesPath); err == nil {
			_ = json.Unmarshal(data, &dumped)
			for _, m := range dumped {
				seen[m.ID] = true
			}
		}
	}

	offsetID := 0
	fetched := 0
	mediaCount := 0
	const batchSize = 100

	for {
		req := &tg.MessagesGetHistoryRequest{
			Peer:     inputPeer,
			OffsetID: offsetID,
			Limit:    batchSize,
		}
		result, err := api.MessagesGetHistory(ctx, req)
		if err != nil {
			return fmt.Errorf("get history offset=%d: %w", offsetID, err)
		}
		msgs, _, _, err := extractHistoryMessages(result)
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			break
		}

		for _, mc := range msgs {
			msg, ok := mc.(*tg.Message)
			if !ok || msg.ID == 0 {
				continue
			}
			if seen[msg.ID] {
				continue
			}
			seen[msg.ID] = true

			dm := dumpedMsg{
				ID:        msg.ID,
				UnixDate:  msg.Date,
				Date:      time.Unix(int64(msg.Date), 0).UTC().Format(time.RFC3339),
				Text:      msg.Message,
				Entities:  serializeEntities(msg.Entities),
				ReplyTo:   msgReplyTo(msg),
				Reactions: msgReactions(msg),
				Pinned:    msg.Pinned,
			}
			if v, ok := msg.GetViews(); ok {
				dm.Views = v
			}
			if f, ok := msg.GetForwards(); ok {
				dm.Forwards = f
			}
			if pa, ok := msg.GetPostAuthor(); ok {
				dm.PostAuthor = pa
			}
			if g, ok := msg.GetGroupedID(); ok {
				dm.GroupedID = g
			}
			if fwd, ok := msg.GetFwdFrom(); ok {
				fm := map[string]any{"date": time.Unix(int64(fwd.Date), 0).UTC().Format(time.RFC3339)}
				if fwd.FromName != "" {
					fm["from_name"] = fwd.FromName
				}
				if fwd.PostAuthor != "" {
					fm["post_author"] = fwd.PostAuthor
				}
				if fwd.ChannelPost != 0 {
					fm["channel_post"] = fwd.ChannelPost
				}
				dm.Forward = fm
			}
			if msg.Media != nil {
				mediaInfo, mErr := describeAndDownloadMedia(ctx, api, d, msg.ID, msg.Media, mediaDir, skipMedia)
				if mErr != nil {
					if mediaInfo == nil {
						mediaInfo = map[string]any{}
					}
					mediaInfo["error"] = mErr.Error()
				}
				if mediaInfo != nil {
					dm.Media = mediaInfo
					if _, ok := mediaInfo["file"]; ok {
						mediaCount++
					}
				}
			}

			dumped = append(dumped, dm)
			fetched++

			if limit > 0 && fetched >= limit {
				break
			}
		}

		minID := 0
		for _, mc := range msgs {
			if msg, ok := mc.(*tg.Message); ok && msg.ID != 0 {
				if minID == 0 || msg.ID < minID {
					minID = msg.ID
				}
			}
		}
		if minID == 0 || minID == offsetID {
			break
		}
		offsetID = minID

		fmt.Fprintf(os.Stderr, "  fetched=%d media=%d offset=%d\n", fetched, mediaCount, offsetID)
		_ = writeJSONAtomic(messagesPath, dumped)

		if limit > 0 && fetched >= limit {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	_ = writeJSONAtomic(messagesPath, dumped)
	fmt.Fprintf(os.Stderr, "  done: messages=%d media=%d\n", len(dumped), mediaCount)
	return nil
}
