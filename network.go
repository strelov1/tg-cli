package main

import (
	"context"
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

	return withTelegram(c, func(ctx context.Context, _ *telegram.Client, api *tg.Client, _ *peers.Manager) error {
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

		d := downloader.NewDownloader().WithPartSize(dumpPartSize)
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

// dumpOneChannel writes meta.json + messages.json (+ media) for one channel.
// Used by cmdDownloadNetwork to walk every channel of a chatlist share.
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

	stats, err := dumpHistory(ctx, api, d, inputPeer, dumpOpts{
		OutDir:    outDir,
		Limit:     limit,
		SkipMedia: skipMedia,
		Resume:    resume,
		LogPrefix: "  ",
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "  done: messages=%d media=%d\n", stats.Total, stats.Media)
	return nil
}
