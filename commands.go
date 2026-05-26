package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func cmdMe(c config) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		self, err := client.Self(ctx)
		if err != nil {
			return err
		}
		return printJSON(map[string]any{
			"id":         self.ID,
			"phone":      self.Phone,
			"username":   self.Username,
			"first_name": self.FirstName,
			"last_name":  self.LastName,
		})
	})
}

func cmdDialogs(c config, onlyUnread bool, limit int, typeFilter string, archived bool, since time.Time) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		// Accumulated entity maps across pages
		usersMap := make(map[int64]*tg.User)
		chatsMap := make(map[int64]*tg.Chat)
		channelsMap := make(map[int64]*tg.Channel)

		var allDialogs []tg.DialogClass
		// topMsgDate maps topMessage ID → unix timestamp (for --since filter)
		topMsgDate := make(map[int]int)

		offsetDate := 0
		offsetID := 0
		var offsetPeer tg.InputPeerClass = &tg.InputPeerEmpty{}

		// FolderID 1 = archived, 0 = main (default)
		folderID := 0
		if archived {
			folderID = 1
		}

		for {
			batchSize := 100
			if limit > 0 && typeFilter == "" {
				// Only apply limit to raw fetch when no type filter is active.
				// With a type filter we keep fetching until we have enough
				// filtered results (see cutoff in the output loop below).
				remaining := limit - len(allDialogs)
				if remaining <= 0 {
					break
				}
				if remaining < batchSize {
					batchSize = remaining
				}
			}

			result, err := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
				Limit:      batchSize,
				FolderID:   folderID,
				OffsetDate: offsetDate,
				OffsetID:   offsetID,
				OffsetPeer: offsetPeer,
			})
			if err != nil {
				return err
			}

			var dialogs []tg.DialogClass
			var messages []tg.MessageClass
			var chats []tg.ChatClass
			var users []tg.UserClass
			done := false

			switch d := result.(type) {
			case *tg.MessagesDialogs:
				dialogs, messages, chats, users = d.Dialogs, d.Messages, d.Chats, d.Users
				done = true // server returned all dialogs at once
			case *tg.MessagesDialogsSlice:
				dialogs, messages, chats, users = d.Dialogs, d.Messages, d.Chats, d.Users
			default:
				return fmt.Errorf("unexpected dialogs type: %T", result)
			}

			for _, u := range users {
				if v, ok := u.(*tg.User); ok {
					usersMap[v.ID] = v
				}
			}
			for _, ch := range chats {
				switch v := ch.(type) {
				case *tg.Chat:
					chatsMap[v.ID] = v
				case *tg.Channel:
					channelsMap[v.ID] = v
				}
			}

			if len(dialogs) == 0 {
				break
			}
			allDialogs = append(allDialogs, dialogs...)

			// Collect top message dates for --since filter
			for _, m := range messages {
				if msg, ok := m.(*tg.Message); ok {
					topMsgDate[msg.ID] = msg.Date
				}
			}

			if done {
				break
			}

			// Build message map for pagination offsets
			msgMap := make(map[int]*tg.Message, len(messages))
			for _, m := range messages {
				if msg, ok := m.(*tg.Message); ok {
					msgMap[msg.ID] = msg
				}
			}
			lastDlg, ok := dialogs[len(dialogs)-1].(*tg.Dialog)
			if !ok {
				break
			}
			lastMsg, ok := msgMap[lastDlg.TopMessage]
			if !ok {
				break
			}
			offsetDate = lastMsg.Date
			offsetID = lastDlg.TopMessage
			switch p := lastDlg.Peer.(type) {
			case *tg.PeerUser:
				if u, ok := usersMap[p.UserID]; ok {
					offsetPeer = &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash}
				} else {
					break
				}
			case *tg.PeerChat:
				offsetPeer = &tg.InputPeerChat{ChatID: p.ChatID}
			case *tg.PeerChannel:
				if ch, ok := channelsMap[p.ChannelID]; ok {
					offsetPeer = &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}
				} else {
					break
				}
			}
		}

		type dialogInfo struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Username    string `json:"username,omitempty"`
			Type        string `json:"type"`
			UnreadCount int    `json:"unread_count"`
			Members     int    `json:"members,omitempty"`
		}

		sinceCutoff := int64(0)
		if !since.IsZero() {
			sinceCutoff = since.Unix()
		}

		var out []dialogInfo
		for _, d := range allDialogs {
			dlg, ok := d.(*tg.Dialog)
			if !ok {
				continue
			}
			if onlyUnread && dlg.UnreadCount == 0 {
				continue
			}
			// --since filter: skip dialogs whose top message is older than cutoff
			if sinceCutoff > 0 {
				if date, ok := topMsgDate[dlg.TopMessage]; !ok || int64(date) < sinceCutoff {
					continue
				}
			}

			info := dialogInfo{UnreadCount: dlg.UnreadCount}
			switch p := dlg.Peer.(type) {
			case *tg.PeerUser:
				info.ID = p.UserID
				info.Type = "user"
				if u, ok := usersMap[p.UserID]; ok {
					info.Name = strings.TrimSpace(u.FirstName + " " + u.LastName)
					info.Username = u.Username
				}
			case *tg.PeerChat:
				info.ID = p.ChatID
				info.Type = "group"
				if c, ok := chatsMap[p.ChatID]; ok {
					info.Name = c.Title
					info.Members = c.ParticipantsCount
				}
			case *tg.PeerChannel:
				info.ID = p.ChannelID
				if ch, ok := channelsMap[p.ChannelID]; ok {
					info.Name = ch.Title
					info.Username = ch.Username
					info.Members = ch.ParticipantsCount
					if ch.Broadcast {
						info.Type = "channel"
					} else {
						info.Type = "supergroup"
					}
				}
			}
			if info.Name == "" {
				continue
			}
			// Apply --type filter (client-side)
			if typeFilter != "" && info.Type != typeFilter {
				continue
			}
			out = append(out, info)
			// Honour --limit when type filter is active (count filtered results)
			if limit > 0 && typeFilter != "" && len(out) >= limit {
				break
			}
		}
		return printJSON(out)
	})
}

func cmdRead(c config, name string, offsetID int, since time.Time, format string, mediaOnly bool, fromMe bool) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}

		if !since.IsZero() {
			return readSince(ctx, api, p, since, format, mediaOnly, fromMe)
		}

		result, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:     p.InputPeer(),
			Limit:    50,
			OffsetID: offsetID,
		})
		if err != nil {
			return err
		}

		msgs, chats, users, err := extractHistoryMessages(result)
		if err != nil {
			return err
		}

		em := buildEntityMaps(users, chats)

		// Offset for pagination = ID of last message
		offset := 0
		if len(msgs) > 0 {
			if last, ok := msgs[len(msgs)-1].(*tg.Message); ok {
				offset = last.ID
			}
		}

		// apply --from-me filter before media/text formatting
		if fromMe {
			var mine []tg.MessageClass
			for _, m := range msgs {
				if msg, ok := m.(*tg.Message); ok && msg.Out {
					mine = append(mine, m)
				}
			}
			msgs = mine
		}

		if mediaOnly {
			full := formatMessagesFull(msgs, em)
			var filtered []tgMsgFull
			for _, m := range full {
				if m.MediaType != "" {
					filtered = append(filtered, m)
				}
			}
			return printJSON(map[string]any{"messages": filtered, "offset": offset})
		}

		formatted := formatMessages(msgs, em)
		if format == "text" {
			printMessagesText(formatted)
			return nil
		}

		return printJSON(map[string]any{
			"messages": formatted,
			"offset":   offset,
		})
	})
}

// printMessagesText prints messages as human-readable plain text to stdout.
func printMessagesText(msgs []tgMsg) {
	for _, m := range msgs {
		who := m.Who
		if who == "" {
			who = "?"
		}
		fmt.Printf("[%s] %s: %s\n", m.When, who, m.Text)
	}
}

// readSince fetches all messages newer than the cutoff time (paginating as needed).
func readSince(ctx context.Context, api *tg.Client, p peers.Peer, since time.Time, format string, mediaOnly bool, fromMe bool) error {
	cutoff := int(since.Unix())
	var all []tgMsg
	var allFull []tgMsgFull
	offsetID := 0

	for {
		result, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:     p.InputPeer(),
			Limit:    100,
			OffsetID: offsetID,
		})
		if err != nil {
			return err
		}
		msgs, chats, users, err := extractHistoryMessages(result)
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			break
		}

		em := buildEntityMaps(users, chats)
		done := false
		for _, m := range msgs {
			msg, ok := m.(*tg.Message)
			if !ok {
				continue
			}
			if msg.Date < cutoff {
				done = true
				break
			}
			if fromMe && !msg.Out {
				continue
			}
			if mediaOnly {
				if msg.Media == nil {
					continue
				}
				mt := mediaTypeName(msg.Media)
				if mt == "" {
					continue
				}
				sender := ""
				if msg.FromID != nil {
					sender = em.senderName(msg.FromID)
				}
				item := tgMsgFull{
					ID:        msg.ID,
					Who:       sender,
					When:      time.Unix(int64(msg.Date), 0).UTC().Format(time.RFC3339),
					Text:      msg.Message,
					MediaType: mt,
				}
				if v, ok := msg.GetViews(); ok {
					item.Views = v
				}
				item.Reactions = msgReactions(msg)
				allFull = append(allFull, item)
			} else {
				if msg.Message == "" {
					continue
				}
				sender := ""
				if msg.FromID != nil {
					sender = em.senderName(msg.FromID)
				}
				all = append(all, tgMsg{
					ID:   msg.ID,
					Who:  sender,
					When: time.Unix(int64(msg.Date), 0).UTC().Format(time.RFC3339),
					Text: msg.Message,
				})
			}
			if offsetID == 0 || msg.ID < offsetID {
				offsetID = msg.ID
			}
		}
		if done || len(msgs) < 100 {
			break
		}
	}

	sinceStr := since.UTC().Format(time.RFC3339)
	if mediaOnly {
		for i, j := 0, len(allFull)-1; i < j; i, j = i+1, j-1 {
			allFull[i], allFull[j] = allFull[j], allFull[i]
		}
		return printJSON(map[string]any{"messages": allFull, "total": len(allFull), "since": sinceStr})
	}

	// Reverse to chronological order (getHistory returns newest first)
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}

	if format == "text" {
		printMessagesText(all)
		return nil
	}
	return printJSON(map[string]any{
		"messages": all,
		"total":    len(all),
		"since":    sinceStr,
	})
}

func cmdReply(c config, name string, msgID int, text string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		_, err = api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
			Peer:     p.InputPeer(),
			Message:  text,
			RandomID: cryptoRandInt63(),
			ReplyTo:  &tg.InputReplyToMessage{ReplyToMsgID: msgID},
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Reply sent")
		return nil
	})
}

func cmdSearchAll(c config, query string, limit int) error {
	if limit <= 0 {
		limit = 50
	}
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		result, err := api.MessagesSearchGlobal(ctx, &tg.MessagesSearchGlobalRequest{
			Q:          query,
			Filter:     &tg.InputMessagesFilterEmpty{},
			OffsetPeer: &tg.InputPeerEmpty{},
			Limit:      limit,
		})
		if err != nil {
			return err
		}
		msgs, chats, users, err := extractHistoryMessages(result)
		if err != nil {
			return err
		}
		em := buildEntityMaps(users, chats)
		formatted := formatMessages(msgs, em)
		return printJSON(map[string]any{
			"results": formatted,
			"total":   len(formatted),
			"query":   query,
		})
	})
}

func cmdSend(c config, name, text string, scheduleAt time.Time, parseMode string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}

		msgText := text
		var entities []tg.MessageEntityClass
		switch strings.ToLower(parseMode) {
		case "html":
			msgText, entities = parseHTMLEntities(text)
		case "markdown", "md":
			msgText, entities = parseMarkdownEntities(text)
		}

		req := &tg.MessagesSendMessageRequest{
			Peer:     p.InputPeer(),
			Message:  msgText,
			RandomID: cryptoRandInt63(),
		}
		if len(entities) > 0 {
			req.Entities = entities
		}
		if !scheduleAt.IsZero() {
			req.ScheduleDate = int(scheduleAt.Unix())
		}
		_, err = api.MessagesSendMessage(ctx, req)
		if err != nil {
			return err
		}
		if !scheduleAt.IsZero() {
			fmt.Fprintf(os.Stderr, "Message scheduled for %s\n", scheduleAt.Format("2006-01-02 15:04"))
		} else {
			fmt.Fprintln(os.Stderr, "Message sent")
		}
		return nil
	})
}

func cmdMarkRead(c config, name string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}

		if ch, ok := p.(peers.Channel); ok {
			_, err = api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{
				Channel: ch.InputChannel(),
				MaxID:   0,
			})
		} else {
			_, err = api.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{
				Peer:  p.InputPeer(),
				MaxID: 0,
			})
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Marked as read")
		return nil
	})
}

func cmdSearchGroups(c config, query string, limit int) error {
	if limit <= 0 {
		limit = 20
	}
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		result, err := api.ContactsSearch(ctx, &tg.ContactsSearchRequest{
			Q:     query,
			Limit: limit,
		})
		if err != nil {
			return err
		}

		em := buildEntityMaps(result.Users, result.Chats)

		type groupInfo struct {
			ID       int64  `json:"id"`
			Title    string `json:"title"`
			Username string `json:"username,omitempty"`
			Type     string `json:"type"`
			Members  int    `json:"members,omitempty"`
		}

		var out []groupInfo
		for _, r := range result.Results {
			switch p := r.(type) {
			case *tg.PeerChannel:
				if ch, ok := em.channels[p.ChannelID]; ok {
					t := "supergroup"
					if ch.Broadcast {
						t = "channel"
					}
					out = append(out, groupInfo{
						ID:       ch.ID,
						Title:    ch.Title,
						Username: ch.Username,
						Type:     t,
						Members:  ch.ParticipantsCount,
					})
				}
			case *tg.PeerChat:
				if chat, ok := em.chats[p.ChatID]; ok {
					out = append(out, groupInfo{
						ID:      chat.ID,
						Title:   chat.Title,
						Type:    "group",
						Members: chat.ParticipantsCount,
					})
				}
			}
		}

		return printJSON(map[string]any{
			"results": out,
			"total":   len(out),
		})
	})
}

func cmdJoin(c config, target string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		if hash := extractInviteHash(target); hash != "" {
			result, err := api.MessagesImportChatInvite(ctx, hash)
			if err != nil {
				if tgerr.Is(err, "USER_ALREADY_PARTICIPANT") {
					fmt.Fprintln(os.Stderr, "Already a member")
					return nil
				}
				return err
			}
			_ = result
			fmt.Fprintln(os.Stderr, "Joined via invite link")
			return nil
		}

		p, err := resolvePeer(ctx, pm, api, target)
		if err != nil {
			return err
		}
		ch, ok := p.(peers.Channel)
		if !ok {
			return fmt.Errorf("%s is not a channel or supergroup", target)
		}
		result, err := api.ChannelsJoinChannel(ctx, ch.InputChannel())
		if err != nil {
			if tgerr.Is(err, "USER_ALREADY_PARTICIPANT") {
				fmt.Fprintln(os.Stderr, "Already a member")
				return nil
			}
			return err
		}
		_ = result
		fmt.Fprintln(os.Stderr, "Joined")
		return nil
	})
}

func cmdLeave(c config, name string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		switch peer := p.(type) {
		case peers.Channel:
			_, err = api.ChannelsLeaveChannel(ctx, peer.InputChannel())
		case peers.Chat:
			_, err = api.MessagesDeleteChatUser(ctx, &tg.MessagesDeleteChatUserRequest{
				ChatID: peer.Raw().ID,
				UserID: &tg.InputUserSelf{},
			})
		default:
			return fmt.Errorf("%s is not a group or channel", name)
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Left")
		return nil
	})
}

func cmdSearch(c config, dialog, query string, limit int) error {
	if limit <= 0 {
		limit = 50
	}
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, dialog)
		if err != nil {
			return err
		}

		result, err := api.MessagesSearch(ctx, &tg.MessagesSearchRequest{
			Peer:   p.InputPeer(),
			Q:      query,
			Filter: &tg.InputMessagesFilterEmpty{},
			Limit:  limit,
		})
		if err != nil {
			return err
		}

		msgs, chats, users, err := extractHistoryMessages(result)
		if err != nil {
			return err
		}

		em := buildEntityMaps(users, chats)
		formatted := formatMessages(msgs, em)

		return printJSON(map[string]any{
			"results": formatted,
			"total":   len(formatted),
			"query":   query,
			"dialog":  dialog,
		})
	})
}

func cmdEdit(c config, name string, msgID int, text string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		_, err = api.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{
			Peer:    p.InputPeer(),
			ID:      msgID,
			Message: text,
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Message edited")
		return nil
	})
}

func cmdDelete(c config, name string, msgIDs []int) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		if ch, ok := p.(peers.Channel); ok {
			_, err = api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
				Channel: ch.InputChannel(),
				ID:      msgIDs,
			})
		} else {
			_, err = api.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{
				Revoke: true,
				ID:     msgIDs,
			})
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Deleted %d message(s)\n", len(msgIDs))
		return nil
	})
}

func cmdReact(c config, name string, msgID int, emoji string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		_, err = api.MessagesSendReaction(ctx, &tg.MessagesSendReactionRequest{
			Peer:  p.InputPeer(),
			MsgID: msgID,
			Reaction: []tg.ReactionClass{
				&tg.ReactionEmoji{Emoticon: emoji},
			},
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Reaction sent")
		return nil
	})
}

func cmdForward(c config, fromName string, msgID int, toName string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		from, err := resolvePeer(ctx, pm, api, fromName)
		if err != nil {
			return fmt.Errorf("source: %w", err)
		}
		to, err := resolvePeer(ctx, pm, api, toName)
		if err != nil {
			return fmt.Errorf("destination: %w", err)
		}
		_, err = api.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
			FromPeer: from.InputPeer(),
			ID:       []int{msgID},
			ToPeer:   to.InputPeer(),
			RandomID: []int64{cryptoRandInt63()},
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Message forwarded")
		return nil
	})
}

func cmdSendFile(c config, name, filePath string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		f, err := os.Open(filePath)
		if err != nil {
			return err
		}
		defer f.Close()

		stat, err := f.Stat()
		if err != nil {
			return err
		}

		u := uploader.NewUploader(api)
		uploaded, err := u.Upload(ctx, uploader.NewUpload(filepath.Base(filePath), f, stat.Size()))
		if err != nil {
			return fmt.Errorf("upload: %w", err)
		}

		mimeType := mime.TypeByExtension(filepath.Ext(filePath))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}

		_, err = api.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
			Peer: p.InputPeer(),
			Media: &tg.InputMediaUploadedDocument{
				File:     uploaded,
				MimeType: mimeType,
				Attributes: []tg.DocumentAttributeClass{
					&tg.DocumentAttributeFilename{FileName: filepath.Base(filePath)},
				},
			},
			RandomID: cryptoRandInt63(),
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "File sent")
		return nil
	})
}

func cmdInfo(c config, name string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		switch peer := p.(type) {
		case peers.User:
			u := peer.Raw()
			about := ""
			if pu, ok := peer.InputPeer().(*tg.InputPeerUser); ok {
				if full, ferr := api.UsersGetFullUser(ctx, &tg.InputUser{UserID: pu.UserID, AccessHash: pu.AccessHash}); ferr == nil {
					about = full.FullUser.About
				}
			}
			return printJSON(map[string]any{
				"type":       "user",
				"id":         u.ID,
				"username":   u.Username,
				"first_name": u.FirstName,
				"last_name":  u.LastName,
				"phone":      u.Phone,
				"bio":        about,
			})
		case peers.Channel:
			ch := peer.Raw()
			t := "supergroup"
			if ch.Broadcast {
				t = "channel"
			}
			about := ""
			members := ch.ParticipantsCount
			if full, ferr := api.ChannelsGetFullChannel(ctx, peer.InputChannel()); ferr == nil {
				if fc, ok := full.FullChat.(*tg.ChannelFull); ok {
					about = fc.About
					members = fc.ParticipantsCount
				}
			}
			// Get current user's role in this channel
			isCreator, isAdmin := false, false
			var adminRights map[string]bool
			if part, perr := api.ChannelsGetParticipant(ctx, &tg.ChannelsGetParticipantRequest{
				Channel:     peer.InputChannel(),
				Participant: &tg.InputPeerSelf{},
			}); perr == nil {
				switch pt := part.Participant.(type) {
				case *tg.ChannelParticipantCreator:
					isCreator, isAdmin = true, true
					if pt.AdminRights != (tg.ChatAdminRights{}) {
						adminRights = adminRightsMap(&pt.AdminRights)
					}
				case *tg.ChannelParticipantAdmin:
					isAdmin = true
					adminRights = adminRightsMap(&pt.AdminRights)
				}
			}
			out := map[string]any{
				"type":        t,
				"id":          ch.ID,
				"title":       ch.Title,
				"username":    ch.Username,
				"members":     members,
				"description": about,
				"is_creator":  isCreator,
				"is_admin":    isAdmin,
			}
			if adminRights != nil {
				out["admin_rights"] = adminRights
			}
			return printJSON(out)
		case peers.Chat:
			ch := peer.Raw()
			about := ""
			isCreator, isAdmin := false, false
			if full, ferr := api.MessagesGetFullChat(ctx, ch.ID); ferr == nil {
				if fc, ok := full.FullChat.(*tg.ChatFull); ok {
					about = fc.About
					if self, serr := client.Self(ctx); serr == nil {
						if parts, ok := fc.Participants.(*tg.ChatParticipants); ok {
							for _, part := range parts.Participants {
								switch pt := part.(type) {
								case *tg.ChatParticipantCreator:
									if pt.UserID == self.ID {
										isCreator, isAdmin = true, true
									}
								case *tg.ChatParticipantAdmin:
									if pt.UserID == self.ID {
										isAdmin = true
									}
								}
							}
						}
					}
				}
			}
			return printJSON(map[string]any{
				"type":        "group",
				"id":          ch.ID,
				"title":       ch.Title,
				"members":     ch.ParticipantsCount,
				"description": about,
				"is_creator":  isCreator,
				"is_admin":    isAdmin,
			})
		}
		return fmt.Errorf("unknown peer type: %T", p)
	})
}

func cmdMembers(c config, name string, limit int) error {
	if limit <= 0 {
		limit = 100
	}
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}

		type memberInfo struct {
			ID        int64  `json:"id"`
			Username  string `json:"username,omitempty"`
			FirstName string `json:"first_name,omitempty"`
			LastName  string `json:"last_name,omitempty"`
		}
		var out []memberInfo

		switch peer := p.(type) {
		case peers.Channel:
			offset := 0
			for len(out) < limit {
				batchSize := limit - len(out)
				if batchSize > 200 {
					batchSize = 200
				}
				result, err := api.ChannelsGetParticipants(ctx, &tg.ChannelsGetParticipantsRequest{
					Channel: peer.InputChannel(),
					Filter:  &tg.ChannelParticipantsRecent{},
					Offset:  offset,
					Limit:   batchSize,
				})
				if err != nil {
					return err
				}
				v, ok := result.(*tg.ChannelsChannelParticipants)
				if !ok || len(v.Users) == 0 {
					break
				}
				for _, u := range v.Users {
					if usr, ok := u.(*tg.User); ok {
						out = append(out, memberInfo{
							ID:        usr.ID,
							Username:  usr.Username,
							FirstName: usr.FirstName,
							LastName:  usr.LastName,
						})
					}
				}
				offset += len(v.Users)
				if len(v.Users) < batchSize {
					break
				}
			}
			if len(out) > limit {
				out = out[:limit]
			}
		case peers.Chat:
			full, err := api.MessagesGetFullChat(ctx, peer.Raw().ID)
			if err != nil {
				return err
			}
			for _, u := range full.Users {
				if usr, ok := u.(*tg.User); ok {
					out = append(out, memberInfo{
						ID:        usr.ID,
						Username:  usr.Username,
						FirstName: usr.FirstName,
						LastName:  usr.LastName,
					})
					if len(out) >= limit {
						break
					}
				}
			}
		default:
			return fmt.Errorf("%s is not a group or channel", name)
		}

		return printJSON(map[string]any{
			"members": out,
			"total":   len(out),
		})
	})
}

func cmdWatch(c config, name string, intervalSecs int, keywords []string, events []string) error {
	if intervalSecs <= 0 {
		intervalSecs = 5
	}
	// Build event set: if empty, default to "new"
	eventSet := make(map[string]bool)
	for _, e := range events {
		eventSet[strings.ToLower(strings.TrimSpace(e))] = true
	}
	wantNew := len(eventSet) == 0 || eventSet["new"] || eventSet["all"]
	wantEdit := eventSet["edit"] || eventSet["all"]
	wantDelete := eventSet["delete"] || eventSet["all"]
	_ = wantDelete // deletion detection not yet implemented

	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}

		// Seed lastID from the most recent message so we only emit new ones
		lastID := 0
		// For edit detection: snapshot of recent message texts
		msgSnapshot := make(map[int]string)

		result, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:  p.InputPeer(),
			Limit: 50,
		})
		if err != nil {
			return err
		}
		if msgs, _, _, _ := extractHistoryMessages(result); len(msgs) > 0 {
			if msg, ok := msgs[0].(*tg.Message); ok {
				lastID = msg.ID
			}
			if wantEdit {
				for _, m := range msgs {
					if msg, ok := m.(*tg.Message); ok {
						msgSnapshot[msg.ID] = msg.Message
					}
				}
			}
		}

		fmt.Fprintf(os.Stderr, "Watching %q from msg %d, polling every %ds. Ctrl+C to stop.\n", name, lastID, intervalSecs)

		ticker := time.NewTicker(time.Duration(intervalSecs) * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				res, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
					Peer:  p.InputPeer(),
					MinID: lastID,
					Limit: 100,
				})
				if err != nil {
					fmt.Fprintf(os.Stderr, "Poll error: %v\n", err)
					continue
				}
				msgs, chats, users, _ := extractHistoryMessages(res)
				em := buildEntityMaps(users, chats)
				formatted := formatMessages(msgs, em)

				// reverse to chronological order (getHistory returns newest first)
				for i, j := 0, len(formatted)-1; i < j; i, j = i+1, j-1 {
					formatted[i], formatted[j] = formatted[j], formatted[i]
				}
				for _, msg := range formatted {
					if msg.ID > lastID {
						lastID = msg.ID
						if wantNew {
							// Apply keyword filter
							if len(keywords) > 0 {
								matched := false
								for _, kw := range keywords {
									if strings.Contains(strings.ToLower(msg.Text), strings.ToLower(kw)) {
										matched = true
										break
									}
								}
								if !matched {
									if wantEdit {
										msgSnapshot[msg.ID] = msg.Text
									}
									continue
								}
							}
							_ = printJSON(map[string]any{"event": "new", "message": msg})
						}
						if wantEdit {
							msgSnapshot[msg.ID] = msg.Text
						}
					}
				}

				// Check for edits (compare current snapshot with previous)
				if wantEdit && len(msgs) > 0 {
					for _, m := range msgs {
						msg, ok := m.(*tg.Message)
						if !ok || msg.Message == "" {
							continue
						}
						if msg.ID >= lastID {
							continue // already handled above as new
						}
						if prev, seen := msgSnapshot[msg.ID]; seen && prev != msg.Message {
							_ = printJSON(map[string]any{
								"event":    "edit",
								"id":       msg.ID,
								"who":      em.senderName(msg.FromID),
								"when":     time.Unix(int64(msg.Date), 0).UTC().Format(time.RFC3339),
								"new_text": msg.Message,
								"old_text": prev,
							})
							msgSnapshot[msg.ID] = msg.Message
						}
					}
				}
			}
		}
	})
}

// inputMediaFromMessage converts a received MessageMediaClass to InputMediaClass for re-sending.
// Returns (nil, false) for unsupported media types.
func inputMediaFromMessage(media tg.MessageMediaClass) (tg.InputMediaClass, bool) {
	switch m := media.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := m.Photo.(*tg.Photo)
		if !ok {
			return nil, false
		}
		return &tg.InputMediaPhoto{
			ID: &tg.InputPhoto{
				ID:            photo.ID,
				AccessHash:    photo.AccessHash,
				FileReference: photo.FileReference,
			},
			Spoiler: m.Spoiler,
		}, true
	case *tg.MessageMediaDocument:
		doc, ok := m.Document.(*tg.Document)
		if !ok {
			return nil, false
		}
		return &tg.InputMediaDocument{
			ID: &tg.InputDocument{
				ID:            doc.ID,
				AccessHash:    doc.AccessHash,
				FileReference: doc.FileReference,
			},
			Spoiler: m.Spoiler,
		}, true
	default:
		return nil, false
	}
}

// cmdForwardCopy copies a message to another dialog without the "Forwarded from" attribution.
func cmdForwardCopy(c config, fromName string, msgID int, toName string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		from, err := resolvePeer(ctx, pm, api, fromName)
		if err != nil {
			return fmt.Errorf("source: %w", err)
		}
		to, err := resolvePeer(ctx, pm, api, toName)
		if err != nil {
			return fmt.Errorf("destination: %w", err)
		}

		ids := []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}}
		var result tg.MessagesMessagesClass
		if ch, ok := from.(peers.Channel); ok {
			result, err = api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
				Channel: ch.InputChannel(),
				ID:      ids,
			})
		} else {
			result, err = api.MessagesGetMessages(ctx, ids)
		}
		if err != nil {
			return err
		}

		msgs, _, _, _ := extractHistoryMessages(result)
		var msg *tg.Message
		for _, m := range msgs {
			if mm, ok := m.(*tg.Message); ok && mm.ID == msgID {
				msg = mm
				break
			}
		}
		if msg == nil {
			return fmt.Errorf("message %d not found", msgID)
		}

		if msg.Media != nil {
			if inputMedia, ok := inputMediaFromMessage(msg.Media); ok {
				_, err = api.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
					Peer:     to.InputPeer(),
					Media:    inputMedia,
					Message:  msg.Message,
					RandomID: cryptoRandInt63(),
				})
				if err != nil {
					return err
				}
				fmt.Fprintln(os.Stderr, "Message copied (with media)")
				return nil
			}
			// Unsupported media — fall through to text-only if available
		}

		if msg.Message == "" {
			return fmt.Errorf("message %d has no copyable content (unsupported media type)", msgID)
		}
		_, err = api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
			Peer:     to.InputPeer(),
			Message:  msg.Message,
			RandomID: cryptoRandInt63(),
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Message copied")
		return nil
	})
}

// cmdReactions reads the reaction counts on a specific message.
func cmdReactions(c config, name string, msgID int) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}

		ids := []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}}
		var result tg.MessagesMessagesClass
		if ch, ok := p.(peers.Channel); ok {
			result, err = api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
				Channel: ch.InputChannel(),
				ID:      ids,
			})
		} else {
			result, err = api.MessagesGetMessages(ctx, ids)
		}
		if err != nil {
			return err
		}

		msgs, _, _, _ := extractHistoryMessages(result)
		var msg *tg.Message
		for _, m := range msgs {
			if mm, ok := m.(*tg.Message); ok && mm.ID == msgID {
				msg = mm
				break
			}
		}
		if msg == nil {
			return fmt.Errorf("message %d not found", msgID)
		}

		reactions := msgReactions(msg)
		return printJSON(map[string]any{
			"message_id": msgID,
			"reactions":  reactions,
			"total":      len(reactions),
		})
	})
}

// cmdAdmins lists administrators of a group or channel with their permissions.
func cmdAdmins(c config, name string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}

		type adminInfo struct {
			ID         int64           `json:"id"`
			Username   string          `json:"username,omitempty"`
			FirstName  string          `json:"first_name,omitempty"`
			LastName   string          `json:"last_name,omitempty"`
			Role       string          `json:"role"`
			CustomTitle string         `json:"custom_title,omitempty"`
			Rights     map[string]bool `json:"rights,omitempty"`
		}

		usersMap := make(map[int64]*tg.User)
		var out []adminInfo

		switch peer := p.(type) {
		case peers.Channel:
			result, err := api.ChannelsGetParticipants(ctx, &tg.ChannelsGetParticipantsRequest{
				Channel: peer.InputChannel(),
				Filter:  &tg.ChannelParticipantsAdmins{},
				Offset:  0,
				Limit:   200,
			})
			if err != nil {
				return err
			}
			v, ok := result.(*tg.ChannelsChannelParticipants)
			if !ok {
				return fmt.Errorf("unexpected result type: %T", result)
			}
			for _, u := range v.Users {
				if usr, ok := u.(*tg.User); ok {
					usersMap[usr.ID] = usr
				}
			}
			for _, part := range v.Participants {
				info := adminInfo{}
				var rights *tg.ChatAdminRights
				switch pt := part.(type) {
				case *tg.ChannelParticipantCreator:
					info.ID = pt.UserID
					info.Role = "creator"
					if rank, ok := pt.GetRank(); ok {
						info.CustomTitle = rank
					}
					rights = &pt.AdminRights
				case *tg.ChannelParticipantAdmin:
					info.ID = pt.UserID
					info.Role = "admin"
					if rank, ok := pt.GetRank(); ok {
						info.CustomTitle = rank
					}
					rights = &pt.AdminRights
				default:
					continue
				}
				if u, ok := usersMap[info.ID]; ok {
					info.Username = u.Username
					info.FirstName = u.FirstName
					info.LastName = u.LastName
				}
				if rights != nil {
					info.Rights = map[string]bool{
						"change_info":     rights.ChangeInfo,
						"post_messages":   rights.PostMessages,
						"edit_messages":   rights.EditMessages,
						"delete_messages": rights.DeleteMessages,
						"ban_users":       rights.BanUsers,
						"invite_users":    rights.InviteUsers,
						"pin_messages":    rights.PinMessages,
						"add_admins":      rights.AddAdmins,
						"anonymous":       rights.Anonymous,
						"manage_call":     rights.ManageCall,
						"manage_topics":   rights.ManageTopics,
					}
				}
				out = append(out, info)
			}

		case peers.Chat:
			full, err := api.MessagesGetFullChat(ctx, peer.Raw().ID)
			if err != nil {
				return err
			}
			for _, u := range full.Users {
				if usr, ok := u.(*tg.User); ok {
					usersMap[usr.ID] = usr
				}
			}
			if fc, ok := full.FullChat.(*tg.ChatFull); ok {
				if parts, ok := fc.Participants.(*tg.ChatParticipants); ok {
					for _, part := range parts.Participants {
						var userID int64
						role := ""
						switch pt := part.(type) {
						case *tg.ChatParticipantCreator:
							userID = pt.UserID
							role = "creator"
						case *tg.ChatParticipantAdmin:
							userID = pt.UserID
							role = "admin"
						default:
							continue
						}
						info := adminInfo{ID: userID, Role: role}
						if u, ok := usersMap[userID]; ok {
							info.Username = u.Username
							info.FirstName = u.FirstName
							info.LastName = u.LastName
						}
						out = append(out, info)
					}
				}
			}

		default:
			return fmt.Errorf("%s is not a group or channel", name)
		}

		return printJSON(map[string]any{"admins": out, "total": len(out)})
	})
}

// cmdGetMessage fetches one or more messages by ID.
func cmdGetMessage(c config, name string, msgIDs []int) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}

		ids := make([]tg.InputMessageClass, len(msgIDs))
		for i, id := range msgIDs {
			ids[i] = &tg.InputMessageID{ID: id}
		}

		var result tg.MessagesMessagesClass
		if ch, ok := p.(peers.Channel); ok {
			result, err = api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
				Channel: ch.InputChannel(),
				ID:      ids,
			})
		} else {
			result, err = api.MessagesGetMessages(ctx, ids)
		}
		if err != nil {
			return err
		}

		msgs, chats, users, err := extractHistoryMessages(result)
		if err != nil {
			return err
		}
		em := buildEntityMaps(users, chats)
		return printJSON(formatMessagesFull(msgs, em))
	})
}

// cmdScan iterates message IDs in [fromID, toID] in batches of 100 and returns all found messages.
func cmdScan(c config, name string, fromID, toID int) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		ch, isChannel := p.(peers.Channel)

		var all []tgMsgFull
		for start := fromID; start <= toID; start += 100 {
			end := start + 99
			if end > toID {
				end = toID
			}
			var ids []tg.InputMessageClass
			for id := start; id <= end; id++ {
				ids = append(ids, &tg.InputMessageID{ID: id})
			}

			var result tg.MessagesMessagesClass
			if isChannel {
				result, err = api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
					Channel: ch.InputChannel(),
					ID:      ids,
				})
			} else {
				result, err = api.MessagesGetMessages(ctx, ids)
			}
			if err != nil {
				return fmt.Errorf("batch %d-%d: %w", start, end, err)
			}
			msgs, chats, users, err := extractHistoryMessages(result)
			if err != nil {
				return err
			}
			em := buildEntityMaps(users, chats)
			batch := formatMessagesFull(msgs, em)
			all = append(all, batch...)
			fmt.Fprintf(os.Stderr, "  scanned %d-%d: %d messages\n", start, end, len(batch))
			time.Sleep(200 * time.Millisecond)
		}

		return printJSON(map[string]any{
			"from_id":  fromID,
			"to_id":    toID,
			"total":    len(all),
			"messages": all,
		})
	})
}

// cmdCommonChats returns chats shared between the current user and another user.
func cmdCommonChats(c config, name string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		u, ok := p.(peers.User)
		if !ok {
			return fmt.Errorf("%s is not a user", name)
		}
		raw := u.Raw()
		inputUser := &tg.InputUser{UserID: raw.ID, AccessHash: raw.AccessHash}

		result, err := api.MessagesGetCommonChats(ctx, &tg.MessagesGetCommonChatsRequest{
			UserID: inputUser,
			MaxID:  0,
			Limit:  100,
		})
		if err != nil {
			return err
		}

		type chatInfo struct {
			ID      int64  `json:"id"`
			Title   string `json:"title"`
			Type    string `json:"type"`
			Members int    `json:"members,omitempty"`
		}

		var chats []tg.ChatClass
		switch v := result.(type) {
		case *tg.MessagesChats:
			chats = v.Chats
		case *tg.MessagesChatsSlice:
			chats = v.Chats
		}

		var out []chatInfo
		for _, chat := range chats {
			switch c := chat.(type) {
			case *tg.Chat:
				out = append(out, chatInfo{ID: c.ID, Title: c.Title, Type: "group", Members: c.ParticipantsCount})
			case *tg.Channel:
				t := "supergroup"
				if c.Broadcast {
					t = "channel"
				}
				out = append(out, chatInfo{ID: c.ID, Title: c.Title, Type: t, Members: c.ParticipantsCount})
			}
		}

		return printJSON(map[string]any{"common_chats": out, "total": len(out)})
	})
}

// cmdUserPhotos lists (and optionally downloads) a user's profile photos.
func cmdUserPhotos(c config, name, saveDir string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		u, ok := p.(peers.User)
		if !ok {
			return fmt.Errorf("%s is not a user", name)
		}
		raw := u.Raw()
		inputUser := &tg.InputUser{UserID: raw.ID, AccessHash: raw.AccessHash}

		result, err := api.PhotosGetUserPhotos(ctx, &tg.PhotosGetUserPhotosRequest{
			UserID: inputUser,
			Offset: 0,
			MaxID:  0,
			Limit:  100,
		})
		if err != nil {
			return err
		}

		var photos []tg.PhotoClass
		switch v := result.(type) {
		case *tg.PhotosPhotos:
			photos = v.Photos
		case *tg.PhotosPhotosSlice:
			photos = v.Photos
		}

		type photoInfo struct {
			ID      int64  `json:"id"`
			Date    string `json:"date"`
			Sizes   int    `json:"sizes"`
			SavedTo string `json:"saved_to,omitempty"`
		}

		if saveDir != "" {
			if err := os.MkdirAll(saveDir, 0755); err != nil {
				return fmt.Errorf("create dir: %w", err)
			}
		}

		d := downloader.NewDownloader()
		var out []photoInfo
		for _, ph := range photos {
			photo, ok := ph.(*tg.Photo)
			if !ok {
				continue
			}
			info := photoInfo{
				ID:    photo.ID,
				Date:  time.Unix(int64(photo.Date), 0).UTC().Format(time.RFC3339),
				Sizes: len(photo.Sizes),
			}
			if saveDir != "" {
				bestType, bestSize := "", 0
				for _, sz := range photo.Sizes {
					if ps, ok := sz.(*tg.PhotoSize); ok && ps.Size > bestSize {
						bestSize = ps.Size
						bestType = ps.Type
					}
				}
				if bestType == "" {
					bestType = "y"
				}
				loc := &tg.InputPhotoFileLocation{
					ID:            photo.ID,
					AccessHash:    photo.AccessHash,
					FileReference: photo.FileReference,
					ThumbSize:     bestType,
				}
				outPath := filepath.Join(saveDir, fmt.Sprintf("%d.jpg", photo.ID))
				if _, err := d.Download(api, loc).ToPath(ctx, outPath); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to download photo %d: %v\n", photo.ID, err)
				} else {
					info.SavedTo = outPath
				}
			}
			out = append(out, info)
		}

		return printJSON(map[string]any{"user": name, "photos": out, "total": len(out)})
	})
}

// cmdDownloadMedia downloads the media from a specific message.
func cmdDownloadMedia(c config, name string, msgID int, outDir string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}

		ids := []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}}
		var result tg.MessagesMessagesClass
		if ch, ok := p.(peers.Channel); ok {
			result, err = api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
				Channel: ch.InputChannel(),
				ID:      ids,
			})
		} else {
			result, err = api.MessagesGetMessages(ctx, ids)
		}
		if err != nil {
			return err
		}

		msgs, _, _, err := extractHistoryMessages(result)
		if err != nil {
			return err
		}

		var msg *tg.Message
		for _, m := range msgs {
			if mm, ok := m.(*tg.Message); ok && mm.ID == msgID {
				msg = mm
				break
			}
		}
		if msg == nil {
			return fmt.Errorf("message %d not found", msgID)
		}
		if msg.Media == nil {
			return fmt.Errorf("message %d has no media", msgID)
		}

		if err := os.MkdirAll(outDir, 0755); err != nil {
			return fmt.Errorf("create dir: %w", err)
		}

		d := downloader.NewDownloader()
		switch media := msg.Media.(type) {
		case *tg.MessageMediaPhoto:
			photo, ok := media.Photo.(*tg.Photo)
			if !ok {
				return fmt.Errorf("invalid photo in message %d", msgID)
			}
			bestType, bestSize := "", 0
			for _, sz := range photo.Sizes {
				if ps, ok := sz.(*tg.PhotoSize); ok && ps.Size > bestSize {
					bestSize = ps.Size
					bestType = ps.Type
				}
			}
			if bestType == "" {
				bestType = "y"
			}
			loc := &tg.InputPhotoFileLocation{
				ID:            photo.ID,
				AccessHash:    photo.AccessHash,
				FileReference: photo.FileReference,
				ThumbSize:     bestType,
			}
			outPath := filepath.Join(outDir, fmt.Sprintf("%d.jpg", msgID))
			if _, err := d.Download(api, loc).ToPath(ctx, outPath); err != nil {
				return fmt.Errorf("download: %w", err)
			}
			return printJSON(map[string]any{"saved_to": outPath, "type": "photo"})

		case *tg.MessageMediaDocument:
			doc, ok := media.Document.(*tg.Document)
			if !ok {
				return fmt.Errorf("invalid document in message %d", msgID)
			}
			filename := ""
			for _, attr := range doc.Attributes {
				if fn, ok := attr.(*tg.DocumentAttributeFilename); ok {
					filename = fn.FileName
					break
				}
			}
			if filename == "" {
				filename = fmt.Sprintf("%d%s", msgID, extFromMIME(doc.MimeType))
			}
			loc := &tg.InputDocumentFileLocation{
				ID:            doc.ID,
				AccessHash:    doc.AccessHash,
				FileReference: doc.FileReference,
				ThumbSize:     "",
			}
			outPath := filepath.Join(outDir, filename)
			if _, err := d.Download(api, loc).ToPath(ctx, outPath); err != nil {
				return fmt.Errorf("download: %w", err)
			}
			return printJSON(map[string]any{"saved_to": outPath, "type": "document", "mime": doc.MimeType})

		default:
			return fmt.Errorf("message %d has non-downloadable media: %T", msgID, msg.Media)
		}
	})
}

func extFromMIME(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	case "audio/mpeg":
		return ".mp3"
	case "audio/ogg":
		return ".ogg"
	case "audio/wav":
		return ".wav"
	case "application/pdf":
		return ".pdf"
	case "application/zip":
		return ".zip"
	case "application/x-tgsticker":
		return ".tgs"
	default:
		exts, _ := mime.ExtensionsByType(mimeType)
		if len(exts) > 0 {
			return exts[0]
		}
		return ""
	}
}

// cmdPin pins a message in a chat.
func cmdPin(c config, name string, msgID int, silent bool) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		_, err = api.MessagesUpdatePinnedMessage(ctx, &tg.MessagesUpdatePinnedMessageRequest{
			Silent: silent,
			Unpin:  false,
			Peer:   p.InputPeer(),
			ID:     msgID,
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Message pinned")
		return nil
	})
}

// cmdUnpin unpins a message in a chat.
func cmdUnpin(c config, name string, msgID int) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		_, err = api.MessagesUpdatePinnedMessage(ctx, &tg.MessagesUpdatePinnedMessageRequest{
			Unpin: true,
			Peer:  p.InputPeer(),
			ID:    msgID,
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Message unpinned")
		return nil
	})
}

// cmdMute mutes notifications for a dialog. duration=0 means permanent.
func cmdMute(c config, name string, duration time.Duration) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		var muteUntil int
		if duration <= 0 {
			muteUntil = 2147483647 // max int32 = permanent
		} else {
			muteUntil = int(time.Now().Add(duration).Unix())
		}
		settings := tg.InputPeerNotifySettings{}
		settings.Flags.Set(2) // flag bit 2 = MuteUntil present
		settings.MuteUntil = muteUntil
		_, err = api.AccountUpdateNotifySettings(ctx, &tg.AccountUpdateNotifySettingsRequest{
			Peer:     &tg.InputNotifyPeer{Peer: p.InputPeer()},
			Settings: settings,
		})
		if err != nil {
			return err
		}
		if duration <= 0 {
			fmt.Fprintln(os.Stderr, "Muted permanently")
		} else {
			fmt.Fprintf(os.Stderr, "Muted for %v\n", duration)
		}
		return nil
	})
}

// cmdUnmute unmutes notifications for a dialog.
func cmdUnmute(c config, name string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		settings := tg.InputPeerNotifySettings{}
		settings.Flags.Set(2)
		settings.MuteUntil = 0
		_, err = api.AccountUpdateNotifySettings(ctx, &tg.AccountUpdateNotifySettingsRequest{
			Peer:     &tg.InputNotifyPeer{Peer: p.InputPeer()},
			Settings: settings,
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Unmuted")
		return nil
	})
}

// cmdTopics lists forum topics in a supergroup.
func cmdTopics(c config, name string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		if _, ok := p.(peers.Channel); !ok {
			return fmt.Errorf("%s is not a supergroup or channel", name)
		}
		result, err := api.MessagesGetForumTopics(ctx, &tg.MessagesGetForumTopicsRequest{
			Peer:  p.InputPeer(),
			Limit: 100,
		})
		if err != nil {
			return err
		}

		type topicInfo struct {
			ID           int    `json:"id"`
			Title        string `json:"title"`
			Closed       bool   `json:"closed,omitempty"`
			Pinned       bool   `json:"pinned,omitempty"`
			Date         string `json:"date"`
			TopMessage   int    `json:"top_message"`
			UnreadCount  int    `json:"unread_count,omitempty"`
		}

		var out []topicInfo
		for _, t := range result.Topics {
			topic, ok := t.(*tg.ForumTopic)
			if !ok {
				continue
			}
			out = append(out, topicInfo{
				ID:          topic.ID,
				Title:       topic.Title,
				Closed:      topic.Closed,
				Pinned:      topic.Pinned,
				Date:        time.Unix(int64(topic.Date), 0).UTC().Format(time.RFC3339),
				TopMessage:  topic.TopMessage,
				UnreadCount: topic.UnreadCount,
			})
		}

		return printJSON(map[string]any{"topics": out, "total": len(out)})
	})
}

// cmdInviteLink generates (or returns) an invite link for a chat.
func cmdInviteLink(c config, name string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		result, err := api.MessagesExportChatInvite(ctx, &tg.MessagesExportChatInviteRequest{
			Peer: p.InputPeer(),
		})
		if err != nil {
			return err
		}
		inv, ok := result.(*tg.ChatInviteExported)
		if !ok {
			return fmt.Errorf("unexpected result type: %T", result)
		}
		return printJSON(map[string]any{
			"link":            inv.Link,
			"permanent":       inv.Permanent,
			"revoked":         inv.Revoked,
			"request_needed":  inv.RequestNeeded,
			"usage":           inv.Usage,
			"usage_limit":     inv.UsageLimit,
		})
	})
}

// cmdPaidInviteLink creates a Telegram Stars subscription invite link.
// Joining users must pay `stars` Stars every 30 days to retain access.
func cmdPaidInviteLink(c config, name string, stars int64, title string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		req := &tg.MessagesExportChatInviteRequest{
			Peer: p.InputPeer(),
			SubscriptionPricing: tg.StarsSubscriptionPricing{
				Period: 30 * 24 * 60 * 60,
				Amount: stars,
			},
		}
		req.SetSubscriptionPricing(req.SubscriptionPricing)
		if title != "" {
			req.SetTitle(title)
		}
		result, err := api.MessagesExportChatInvite(ctx, req)
		if err != nil {
			return err
		}
		inv, ok := result.(*tg.ChatInviteExported)
		if !ok {
			return fmt.Errorf("unexpected result type: %T", result)
		}
		return printJSON(map[string]any{
			"link":             inv.Link,
			"stars_per_month":  stars,
			"period_seconds":   30 * 24 * 60 * 60,
			"title":            title,
			"permanent":        inv.Permanent,
			"revoked":          inv.Revoked,
			"request_needed":   inv.RequestNeeded,
		})
	})
}

// cmdInvite invites a user or bot to a group/channel.
// groupName may be a username, dialog name, or a raw numeric chat ID for regular groups.
func cmdInvite(c config, groupName, userName string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		// Resolve the target user first.
		userPeer, err := resolvePeer(ctx, pm, api, userName)
		if err != nil {
			return fmt.Errorf("user: %w", err)
		}
		u, ok := userPeer.(peers.User)
		if !ok {
			return fmt.Errorf("%s is not a user", userName)
		}
		raw := u.Raw()
		inputUser := &tg.InputUser{UserID: raw.ID, AccessHash: raw.AccessHash}

		// If groupName is a bare integer, treat as a regular chat ID directly.
		if chatID, convErr := strconv.ParseInt(groupName, 10, 64); convErr == nil {
			_, err = api.MessagesAddChatUser(ctx, &tg.MessagesAddChatUserRequest{
				ChatID:   chatID,
				UserID:   inputUser,
				FwdLimit: 100,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Invited %s to chat %d\n", userName, chatID)
			return nil
		}

		group, err := resolvePeer(ctx, pm, api, groupName)
		if err != nil {
			return fmt.Errorf("group: %w", err)
		}

		switch peer := group.(type) {
		case peers.Channel:
			_, err = api.ChannelsInviteToChannel(ctx, &tg.ChannelsInviteToChannelRequest{
				Channel: peer.InputChannel(),
				Users:   []tg.InputUserClass{inputUser},
			})
		case peers.Chat:
			_, err = api.MessagesAddChatUser(ctx, &tg.MessagesAddChatUserRequest{
				ChatID:   peer.Raw().ID,
				UserID:   inputUser,
				FwdLimit: 100,
			})
		default:
			return fmt.Errorf("%s is not a group or channel", groupName)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Invited %s to %s\n", userName, groupName)
		return nil
	})
}

func cmdExport(c config, name string, limit int) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}

		var all []tgMsg
		offsetID := 0
		incomplete := false

		for batch := 0; ; batch++ {
			result, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
				Peer:     p.InputPeer(),
				Limit:    100,
				OffsetID: offsetID,
			})
			if err != nil {
				if rpcErr, ok := tgerr.AsType(err, "FLOOD_WAIT"); ok {
					wait := time.Duration(rpcErr.Argument)*time.Second + 500*time.Millisecond
					fmt.Fprintf(os.Stderr, "  flood wait %v, sleeping...\n", wait.Round(time.Second))
					select {
					case <-time.After(wait):
						continue
					case <-ctx.Done():
						incomplete = true
						break
					}
				}
				if len(all) > 0 {
					fmt.Fprintln(os.Stderr, "Warning: export interrupted, partial results returned")
					incomplete = true
					break
				}
				return err
			}

			msgs, chats, users, err := extractHistoryMessages(result)
			if err != nil {
				return err
			}
			if len(msgs) == 0 {
				break
			}

			em := buildEntityMaps(users, chats)
			batchMsgs := formatMessages(msgs, em)
			all = append(all, batchMsgs...)

			for _, m := range msgs {
				if msg, ok := m.(*tg.Message); ok {
					if offsetID == 0 || msg.ID < offsetID {
						offsetID = msg.ID
					}
				}
			}

			fmt.Fprintf(os.Stderr, "  batch %d: %d messages (total: %d)\n", batch+1, len(batchMsgs), len(all))

			if limit > 0 && len(all) >= limit {
				all = all[:limit]
				break
			}

			if len(msgs) < 100 {
				break // last batch
			}

			time.Sleep(300 * time.Millisecond)
		}

		// Reverse to chronological order (getHistory returns newest first)
		for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
			all[i], all[j] = all[j], all[i]
		}

		return printJSON(map[string]any{
			"account":        c.account,
			"dialog":         name,
			"total_messages": len(all),
			"incomplete":     incomplete,
			"messages":       all,
		})
	})
}

// cmdCreateGroup creates a new regular group with an optional list of initial members.
func cmdCreateGroup(c config, title string, userNames []string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		var users []tg.InputUserClass
		for _, name := range userNames {
			p, err := resolvePeer(ctx, pm, api, name)
			if err != nil {
				return fmt.Errorf("user %s: %w", name, err)
			}
			u, ok := p.(peers.User)
			if !ok {
				return fmt.Errorf("%s is not a user", name)
			}
			raw := u.Raw()
			users = append(users, &tg.InputUser{UserID: raw.ID, AccessHash: raw.AccessHash})
		}

		result, err := api.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{
			Users: users,
			Title: title,
		})
		if err != nil {
			return err
		}

		// Extract chat ID from the Updates field
		var chatID int64
		if updFull, ok := result.Updates.(*tg.Updates); ok {
			for _, upd := range updFull.Updates {
				if u, ok := upd.(*tg.UpdateChatParticipants); ok {
					chatID = u.Participants.GetChatID()
					break
				}
			}
			if chatID == 0 {
				for _, ch := range updFull.Chats {
					if chat, ok := ch.(*tg.Chat); ok {
						chatID = chat.ID
						break
					}
				}
			}
		}

		fmt.Fprintf(os.Stderr, "Group %q created (id: %d)\n", title, chatID)
		return printJSON(map[string]any{
			"status":  "created",
			"title":   title,
			"chat_id": chatID,
		})
	})
}

// ── adminRightsMap ──────────────────────────────────────────────────────────

// adminRightsMap converts ChatAdminRights to a map of right name → bool (true only).
func adminRightsMap(r *tg.ChatAdminRights) map[string]bool {
	if r == nil {
		return nil
	}
	m := map[string]bool{}
	if r.ChangeInfo {
		m["change_info"] = true
	}
	if r.PostMessages {
		m["post_messages"] = true
	}
	if r.EditMessages {
		m["edit_messages"] = true
	}
	if r.DeleteMessages {
		m["delete_messages"] = true
	}
	if r.BanUsers {
		m["ban_users"] = true
	}
	if r.InviteUsers {
		m["invite_users"] = true
	}
	if r.PinMessages {
		m["pin_messages"] = true
	}
	if r.AddAdmins {
		m["add_admins"] = true
	}
	if r.Anonymous {
		m["anonymous"] = true
	}
	if r.ManageCall {
		m["manage_call"] = true
	}
	if r.ManageTopics {
		m["manage_topics"] = true
	}
	return m
}

// parseAdminPerms builds ChatAdminRights from a slice of perm names.
// Supported: post, edit, delete, ban, invite, pin, add_admins, manage, anonymous, change_info, topics, all.
func parseAdminPerms(perms []string) tg.ChatAdminRights {
	var r tg.ChatAdminRights
	for _, p := range perms {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "all":
			r.ChangeInfo = true
			r.PostMessages = true
			r.EditMessages = true
			r.DeleteMessages = true
			r.BanUsers = true
			r.InviteUsers = true
			r.PinMessages = true
			r.ManageCall = true
			r.ManageTopics = true
		case "post":
			r.PostMessages = true
		case "edit":
			r.EditMessages = true
		case "delete":
			r.DeleteMessages = true
		case "ban":
			r.BanUsers = true
		case "invite":
			r.InviteUsers = true
		case "pin":
			r.PinMessages = true
		case "add_admins":
			r.AddAdmins = true
		case "manage":
			r.ManageCall = true
		case "anonymous":
			r.Anonymous = true
		case "change_info":
			r.ChangeInfo = true
		case "topics":
			r.ManageTopics = true
		}
	}
	return r
}

// resolveInputUser resolves a name to tg.InputUser.
func resolveInputUser(ctx context.Context, pm *peers.Manager, api *tg.Client, name string) (*tg.InputUser, error) {
	p, err := resolvePeer(ctx, pm, api, name)
	if err != nil {
		return nil, err
	}
	u, ok := p.(peers.User)
	if !ok {
		return nil, fmt.Errorf("%s is not a user", name)
	}
	raw := u.Raw()
	return &tg.InputUser{UserID: raw.ID, AccessHash: raw.AccessHash}, nil
}

// ── my-channels ────────────────────────────────────────────────────────────

// cmdMyChannels lists channels/supergroups where the current user is admin or creator.
// onlyOwned=true restricts to creator only.
func cmdMyChannels(c config, onlyOwned bool) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		type chanInfo struct {
			ID          int64            `json:"id"`
			Title       string           `json:"title"`
			Username    string           `json:"username,omitempty"`
			Type        string           `json:"type"`
			Members     int              `json:"members,omitempty"`
			IsOwner     bool             `json:"is_owner"`
			AdminRights map[string]bool  `json:"admin_rights,omitempty"`
		}

		var out []chanInfo

		// Iterate dialogs; channel entities carry Creator/AdminRights without extra API calls.
		var offsetPeer tg.InputPeerClass = &tg.InputPeerEmpty{}
		offsetDate, offsetID := 0, 0

		for {
			result, err := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
				OffsetDate: offsetDate,
				OffsetID:   offsetID,
				OffsetPeer: offsetPeer,
				Limit:      100,
			})
			if err != nil {
				return err
			}

			var dialogs []tg.DialogClass
			var messages []tg.MessageClass
			var chats []tg.ChatClass
			done := false

			switch d := result.(type) {
			case *tg.MessagesDialogs:
				dialogs, messages, chats = d.Dialogs, d.Messages, d.Chats
				done = true
			case *tg.MessagesDialogsSlice:
				dialogs, messages, chats = d.Dialogs, d.Messages, d.Chats
			default:
				return fmt.Errorf("unexpected dialogs type: %T", result)
			}

			channelMap := make(map[int64]*tg.Channel)
			for _, ch := range chats {
				if c, ok := ch.(*tg.Channel); ok {
					channelMap[c.ID] = c
				}
			}

			msgMap := make(map[int]*tg.Message)
			for _, m := range messages {
				if msg, ok := m.(*tg.Message); ok {
					msgMap[msg.ID] = msg
				}
			}

			for _, d := range dialogs {
				dlg, ok := d.(*tg.Dialog)
				if !ok {
					continue
				}
				peer, ok := dlg.Peer.(*tg.PeerChannel)
				if !ok {
					continue
				}
				ch, ok := channelMap[peer.ChannelID]
				if !ok {
					continue
				}
				isOwner := ch.Creator
				isAdmin := ch.Creator || !ch.AdminRights.Zero()
				if !isAdmin {
					continue
				}
				if onlyOwned && !isOwner {
					continue
				}
				t := "supergroup"
				if ch.Broadcast {
					t = "channel"
				}
				item := chanInfo{
					ID:      ch.ID,
					Title:   ch.Title,
					Username: ch.Username,
					Type:    t,
					Members: ch.ParticipantsCount,
					IsOwner: isOwner,
				}
				if !ch.AdminRights.Zero() {
					item.AdminRights = adminRightsMap(&ch.AdminRights)
				}
				out = append(out, item)
			}

			if done || len(dialogs) == 0 {
				break
			}
			lastDlg, ok := dialogs[len(dialogs)-1].(*tg.Dialog)
			if !ok {
				break
			}
			lastMsg, ok := msgMap[lastDlg.TopMessage]
			if !ok {
				break
			}
			offsetDate = lastMsg.Date
			offsetID = lastDlg.TopMessage
			if p, ok := lastDlg.Peer.(*tg.PeerChannel); ok {
				if ch, ok := channelMap[p.ChannelID]; ok {
					offsetPeer = &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}
				}
			}
		}

		return printJSON(map[string]any{"channels": out, "total": len(out)})
	})
}

// ── kick / ban / unban ─────────────────────────────────────────────────────

// cmdKick removes a user from a group or channel without banning.
func cmdKick(c config, groupName, userName string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		group, err := resolvePeer(ctx, pm, api, groupName)
		if err != nil {
			return fmt.Errorf("group: %w", err)
		}
		inputUser, err := resolveInputUser(ctx, pm, api, userName)
		if err != nil {
			return fmt.Errorf("user: %w", err)
		}

		switch peer := group.(type) {
		case peers.Channel:
			_, err = api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
				Channel:     peer.InputChannel(),
				Participant: &tg.InputPeerUser{UserID: inputUser.UserID, AccessHash: inputUser.AccessHash},
				BannedRights: tg.ChatBannedRights{
					ViewMessages: true,
					UntilDate:    int(time.Now().Add(30 * time.Second).Unix()),
				},
			})
			if err != nil {
				return err
			}
			// Immediately unban so they can re-join
			_, err = api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
				Channel:      peer.InputChannel(),
				Participant:  &tg.InputPeerUser{UserID: inputUser.UserID, AccessHash: inputUser.AccessHash},
				BannedRights: tg.ChatBannedRights{},
			})
		case peers.Chat:
			_, err = api.MessagesDeleteChatUser(ctx, &tg.MessagesDeleteChatUserRequest{
				ChatID: peer.Raw().ID,
				UserID: inputUser,
			})
		default:
			return fmt.Errorf("%s is not a group or channel", groupName)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Kicked %s from %s\n", userName, groupName)
		return nil
	})
}

// cmdBan bans a user from a channel. until=zero means permanent.
func cmdBan(c config, groupName, userName string, until time.Time) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		group, err := resolvePeer(ctx, pm, api, groupName)
		if err != nil {
			return fmt.Errorf("group: %w", err)
		}
		inputUser, err := resolveInputUser(ctx, pm, api, userName)
		if err != nil {
			return fmt.Errorf("user: %w", err)
		}
		ch, ok := group.(peers.Channel)
		if !ok {
			return fmt.Errorf("ban requires a channel or supergroup (use kick for regular groups)")
		}
		untilDate := 0
		if !until.IsZero() {
			untilDate = int(until.Unix())
		}
		_, err = api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
			Channel:     ch.InputChannel(),
			Participant: &tg.InputPeerUser{UserID: inputUser.UserID, AccessHash: inputUser.AccessHash},
			BannedRights: tg.ChatBannedRights{
				ViewMessages: true,
				UntilDate:    untilDate,
			},
		})
		if err != nil {
			return err
		}
		if until.IsZero() {
			fmt.Fprintf(os.Stderr, "Banned %s from %s (permanent)\n", userName, groupName)
		} else {
			fmt.Fprintf(os.Stderr, "Banned %s from %s until %s\n", userName, groupName, until.Format("2006-01-02 15:04"))
		}
		return nil
	})
}

// cmdUnban removes a ban from a user in a channel.
func cmdUnban(c config, groupName, userName string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		group, err := resolvePeer(ctx, pm, api, groupName)
		if err != nil {
			return fmt.Errorf("group: %w", err)
		}
		inputUser, err := resolveInputUser(ctx, pm, api, userName)
		if err != nil {
			return fmt.Errorf("user: %w", err)
		}
		ch, ok := group.(peers.Channel)
		if !ok {
			return fmt.Errorf("unban requires a channel or supergroup")
		}
		_, err = api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
			Channel:      ch.InputChannel(),
			Participant:  &tg.InputPeerUser{UserID: inputUser.UserID, AccessHash: inputUser.AccessHash},
			BannedRights: tg.ChatBannedRights{},
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Unbanned %s in %s\n", userName, groupName)
		return nil
	})
}

// ── promote / demote ───────────────────────────────────────────────────────

// cmdPromote makes a user an admin in a channel/supergroup with the given permissions.
// perms is a comma-separated list: post,edit,delete,ban,invite,pin,add_admins,manage,anonymous,change_info,topics,all
func cmdPromote(c config, groupName, userName string, perms []string, rank string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		group, err := resolvePeer(ctx, pm, api, groupName)
		if err != nil {
			return fmt.Errorf("group: %w", err)
		}
		inputUser, err := resolveInputUser(ctx, pm, api, userName)
		if err != nil {
			return fmt.Errorf("user: %w", err)
		}
		ch, ok := group.(peers.Channel)
		if !ok {
			return fmt.Errorf("promote requires a channel or supergroup")
		}
		rights := parseAdminPerms(perms)
		_, err = api.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{
			Channel:     ch.InputChannel(),
			UserID:      inputUser,
			AdminRights: rights,
			Rank:        rank,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Promoted %s in %s\n", userName, groupName)
		return nil
	})
}

// cmdDemote removes admin rights from a user in a channel/supergroup.
func cmdDemote(c config, groupName, userName string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		group, err := resolvePeer(ctx, pm, api, groupName)
		if err != nil {
			return fmt.Errorf("group: %w", err)
		}
		inputUser, err := resolveInputUser(ctx, pm, api, userName)
		if err != nil {
			return fmt.Errorf("user: %w", err)
		}
		ch, ok := group.(peers.Channel)
		if !ok {
			return fmt.Errorf("demote requires a channel or supergroup")
		}
		_, err = api.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{
			Channel:     ch.InputChannel(),
			UserID:      inputUser,
			AdminRights: tg.ChatAdminRights{},
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Demoted %s in %s\n", userName, groupName)
		return nil
	})
}

// ── set-title / set-description / set-photo ────────────────────────────────

func cmdSetTitle(c config, name, title string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		switch peer := p.(type) {
		case peers.Channel:
			_, err = api.ChannelsEditTitle(ctx, &tg.ChannelsEditTitleRequest{
				Channel: peer.InputChannel(),
				Title:   title,
			})
		case peers.Chat:
			_, err = api.MessagesEditChatTitle(ctx, &tg.MessagesEditChatTitleRequest{
				ChatID: peer.Raw().ID,
				Title:  title,
			})
		default:
			return fmt.Errorf("cannot set title for a user")
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Title updated to %q\n", title)
		return nil
	})
}

func cmdSetDescription(c config, name, desc string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		switch peer := p.(type) {
		case peers.Channel:
			_, err = api.MessagesEditChatAbout(ctx, &tg.MessagesEditChatAboutRequest{
				Peer:  peer.InputPeer(),
				About: desc,
			})
		case peers.Chat:
			_, err = api.MessagesEditChatAbout(ctx, &tg.MessagesEditChatAboutRequest{
				Peer:  peer.InputPeer(),
				About: desc,
			})
		default:
			return fmt.Errorf("cannot set description for a user")
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Description updated")
		return nil
	})
}

func cmdSetPhoto(c config, name, filePath string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		f, err := os.Open(filePath)
		if err != nil {
			return err
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil {
			return err
		}
		u := uploader.NewUploader(api)
		uploaded, err := u.Upload(ctx, uploader.NewUpload(filepath.Base(filePath), f, stat.Size()))
		if err != nil {
			return fmt.Errorf("upload: %w", err)
		}
		photo := &tg.InputChatUploadedPhoto{File: uploaded}

		switch peer := p.(type) {
		case peers.Channel:
			_, err = api.ChannelsEditPhoto(ctx, &tg.ChannelsEditPhotoRequest{
				Channel: peer.InputChannel(),
				Photo:   photo,
			})
		case peers.Chat:
			_, err = api.MessagesEditChatPhoto(ctx, &tg.MessagesEditChatPhotoRequest{
				ChatID: peer.Raw().ID,
				Photo:  photo,
			})
		default:
			return fmt.Errorf("cannot set photo for a user")
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Photo updated")
		return nil
	})
}

// ── contacts ───────────────────────────────────────────────────────────────

func cmdContacts(c config) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		result, err := api.ContactsGetContacts(ctx, 0)
		if err != nil {
			return err
		}
		contacts, ok := result.(*tg.ContactsContacts)
		if !ok {
			return printJSON(map[string]any{"contacts": []any{}, "total": 0})
		}
		type contactInfo struct {
			ID        int64  `json:"id"`
			Phone     string `json:"phone,omitempty"`
			Username  string `json:"username,omitempty"`
			FirstName string `json:"first_name,omitempty"`
			LastName  string `json:"last_name,omitempty"`
		}
		var out []contactInfo
		for _, u := range contacts.Users {
			if usr, ok := u.(*tg.User); ok {
				out = append(out, contactInfo{
					ID:        usr.ID,
					Phone:     usr.Phone,
					Username:  usr.Username,
					FirstName: usr.FirstName,
					LastName:  usr.LastName,
				})
			}
		}
		return printJSON(map[string]any{"contacts": out, "total": len(out)})
	})
}

// cmdContactsAdd adds a contact by phone number.
func cmdContactsAdd(c config, phone, firstName, lastName string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		result, err := api.ContactsImportContacts(ctx, []tg.InputPhoneContact{
			{
				ClientID:  cryptoRandInt63(),
				Phone:     phone,
				FirstName: firstName,
				LastName:  lastName,
			},
		})
		if err != nil {
			return err
		}
		if len(result.Users) == 0 {
			return fmt.Errorf("no user found for phone %s", phone)
		}
		u, ok := result.Users[0].(*tg.User)
		if !ok {
			return fmt.Errorf("unexpected user type")
		}
		fmt.Fprintf(os.Stderr, "Contact added: %s %s (id: %d)\n", u.FirstName, u.LastName, u.ID)
		return printJSON(map[string]any{
			"id":         u.ID,
			"phone":      u.Phone,
			"username":   u.Username,
			"first_name": u.FirstName,
			"last_name":  u.LastName,
		})
	})
}

// cmdContactsDelete removes a contact. Accepts @username, phone, or numeric ID.
func cmdContactsDelete(c config, name string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		peer, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		user, ok := peer.(peers.User)
		if !ok {
			return fmt.Errorf("contacts delete only works for users")
		}
		inputUser := user.InputUser()
		_, err = api.ContactsDeleteContacts(ctx, []tg.InputUserClass{inputUser})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Removed contact %s\n", name)
		return printJSON(map[string]any{"status": "deleted", "user": name})
	})
}

// ── HTML / Markdown entity parsers ──────────────────────────────────────────

// utf16RuneLen returns the number of UTF-16 code units needed for a rune.
func utf16RuneLen(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}

// extractHTMLAttr extracts the value of the given attribute from a raw HTML tag string.
func extractHTMLAttr(tag, attr string) string {
	lower := strings.ToLower(tag)
	prefix := strings.ToLower(attr) + "="
	idx := strings.Index(lower, prefix)
	if idx < 0 {
		return ""
	}
	start := idx + len(prefix)
	if start >= len(tag) {
		return ""
	}
	quote := tag[start]
	if quote == '"' || quote == '\'' {
		start++
		end := strings.IndexByte(tag[start:], quote)
		if end < 0 {
			return tag[start:]
		}
		return tag[start : start+end]
	}
	// Unquoted attribute
	end := strings.IndexAny(tag[start:], " >")
	if end < 0 {
		return tag[start:]
	}
	return tag[start : start+end]
}

// normHTMLTag normalises HTML tag aliases to a canonical name.
func normHTMLTag(tag string) string {
	switch tag {
	case "strong":
		return "b"
	case "em":
		return "i"
	case "ins":
		return "u"
	case "del", "strike":
		return "s"
	default:
		return tag
	}
}

// makeHTMLEntity converts a normalised tag name + href to a tg.MessageEntityClass.
func makeHTMLEntity(tag, href string, off, length int) tg.MessageEntityClass {
	switch tag {
	case "b":
		return &tg.MessageEntityBold{Offset: off, Length: length}
	case "i":
		return &tg.MessageEntityItalic{Offset: off, Length: length}
	case "u":
		return &tg.MessageEntityUnderline{Offset: off, Length: length}
	case "s":
		return &tg.MessageEntityStrike{Offset: off, Length: length}
	case "code":
		return &tg.MessageEntityCode{Offset: off, Length: length}
	case "pre":
		return &tg.MessageEntityPre{Offset: off, Length: length}
	case "tg-spoiler":
		return &tg.MessageEntitySpoiler{Offset: off, Length: length}
	case "blockquote":
		return &tg.MessageEntityBlockquote{Offset: off, Length: length}
	case "a":
		if href != "" {
			return &tg.MessageEntityTextURL{Offset: off, Length: length, URL: href}
		}
	}
	return nil
}

// parseHTMLEntities parses a Telegram-compatible HTML string into plain text
// and MessageEntity slice. Supported tags: <b>, <strong>, <i>, <em>, <u>,
// <ins>, <s>, <del>, <strike>, <code>, <pre>, <a href="">, <tg-spoiler>,
// <blockquote>, <br>.
func parseHTMLEntities(input string) (string, []tg.MessageEntityClass) {
	type frame struct {
		tag   string
		href  string
		off16 int
	}

	var buf strings.Builder
	var stack []frame
	var entities []tg.MessageEntityClass
	var utf16pos int

	i := 0
	for i < len(input) {
		b := input[i]
		if b != '<' {
			// Handle HTML entities
			if b == '&' {
				end := strings.Index(input[i:], ";")
				if end > 0 && end < 10 {
					ent := input[i+1 : i+end]
					var ch string
					switch ent {
					case "lt":
						ch = "<"
					case "gt":
						ch = ">"
					case "amp":
						ch = "&"
					case "quot":
						ch = "\""
					case "apos":
						ch = "'"
					case "nbsp":
						ch = "\u00a0"
					}
					if ch != "" {
						buf.WriteString(ch)
						for _, r := range ch {
							utf16pos += utf16RuneLen(r)
						}
						i += end + 1
						continue
					}
				}
			}
			r, size := utf8.DecodeRuneInString(input[i:])
			if r == utf8.RuneError && size == 1 {
				buf.WriteByte(b)
				utf16pos++
				i++
			} else {
				buf.WriteRune(r)
				utf16pos += utf16RuneLen(r)
				i += size
			}
			continue
		}

		// Find end of tag
		end := strings.Index(input[i:], ">")
		if end < 0 {
			r, size := utf8.DecodeRuneInString(input[i:])
			buf.WriteRune(r)
			utf16pos += utf16RuneLen(r)
			i += size
			continue
		}

		rawTag := input[i+1 : i+end]
		i += end + 1

		isClose := strings.HasPrefix(rawTag, "/")
		if isClose {
			rawTag = strings.TrimSpace(rawTag[1:])
			fields := strings.Fields(rawTag)
			if len(fields) == 0 {
				continue
			}
			tagName := normHTMLTag(strings.ToLower(fields[0]))

			// Pop matching frame from stack
			for j := len(stack) - 1; j >= 0; j-- {
				if normHTMLTag(stack[j].tag) == tagName {
					f := stack[j]
					length := utf16pos - f.off16
					if length > 0 {
						if ent := makeHTMLEntity(normHTMLTag(f.tag), f.href, f.off16, length); ent != nil {
							entities = append(entities, ent)
						}
					}
					stack = append(stack[:j], stack[j+1:]...)
					break
				}
			}
			continue
		}

		// Self-closing or void tags
		selfClose := strings.HasSuffix(rawTag, "/")
		if selfClose {
			rawTag = rawTag[:len(rawTag)-1]
		}
		fields := strings.Fields(rawTag)
		if len(fields) == 0 {
			continue
		}
		tagName := strings.ToLower(fields[0])

		if tagName == "br" {
			buf.WriteByte('\n')
			utf16pos++
			continue
		}
		if selfClose {
			continue
		}

		href := ""
		if tagName == "a" {
			href = extractHTMLAttr(rawTag, "href")
		}
		stack = append(stack, frame{tag: tagName, href: href, off16: utf16pos})
	}

	return buf.String(), entities
}

// parseMarkdownEntities parses a simple Telegram-flavoured Markdown string into
// plain text and MessageEntity slice.
// Supported: **bold**, __bold__, *italic*, _italic_, `code`, ```pre```
func parseMarkdownEntities(input string) (string, []tg.MessageEntityClass) {
	var buf strings.Builder
	var entities []tg.MessageEntityClass
	var utf16pos int

	i := 0
	for i < len(input) {
		// Code block: ```...```
		if strings.HasPrefix(input[i:], "```") {
			end := strings.Index(input[i+3:], "```")
			if end >= 0 {
				content := input[i+3 : i+3+end]
				// Skip optional language line
				if nl := strings.IndexByte(content, '\n'); nl >= 0 {
					content = content[nl+1:]
				}
				off := utf16pos
				for _, r := range content {
					buf.WriteRune(r)
					utf16pos += utf16RuneLen(r)
				}
				if length := utf16pos - off; length > 0 {
					entities = append(entities, &tg.MessageEntityPre{Offset: off, Length: length})
				}
				i += 3 + end + 3
				continue
			}
		}

		// Bold: **...** or __...__
		if strings.HasPrefix(input[i:], "**") || strings.HasPrefix(input[i:], "__") {
			delim := input[i : i+2]
			if end := strings.Index(input[i+2:], delim); end >= 0 {
				content := input[i+2 : i+2+end]
				off := utf16pos
				for _, r := range content {
					buf.WriteRune(r)
					utf16pos += utf16RuneLen(r)
				}
				if length := utf16pos - off; length > 0 {
					entities = append(entities, &tg.MessageEntityBold{Offset: off, Length: length})
				}
				i += 2 + end + 2
				continue
			}
		}

		// Inline code: `...`
		if input[i] == '`' {
			if end := strings.IndexByte(input[i+1:], '`'); end >= 0 {
				content := input[i+1 : i+1+end]
				off := utf16pos
				for _, r := range content {
					buf.WriteRune(r)
					utf16pos += utf16RuneLen(r)
				}
				if length := utf16pos - off; length > 0 {
					entities = append(entities, &tg.MessageEntityCode{Offset: off, Length: length})
				}
				i += 1 + end + 1
				continue
			}
		}

		// Italic: *...* or _..._  (only when not doubled)
		if (input[i] == '*' || input[i] == '_') && (i+1 >= len(input) || input[i+1] != input[i]) {
			delim := input[i : i+1]
			if end := strings.Index(input[i+1:], delim); end > 0 {
				content := input[i+1 : i+1+end]
				off := utf16pos
				for _, r := range content {
					buf.WriteRune(r)
					utf16pos += utf16RuneLen(r)
				}
				if length := utf16pos - off; length > 0 {
					entities = append(entities, &tg.MessageEntityItalic{Offset: off, Length: length})
				}
				i += 1 + end + 1
				continue
			}
		}

		// Regular rune
		r, size := utf8.DecodeRuneInString(input[i:])
		buf.WriteRune(r)
		utf16pos += utf16RuneLen(r)
		i += size
	}

	return buf.String(), entities
}

// ── send-album ───────────────────────────────────────────────────────────────

// cmdSendAlbum uploads multiple files and sends them as a grouped media album.
// caption is attached to the first media (shown above the album).
// spoiler=true marks every photo/video as spoilered (blurred until tap).
func cmdSendAlbum(c config, name string, filePaths []string, caption string, spoiler bool, scheduleAt time.Time) error {
	if len(filePaths) == 0 {
		return fmt.Errorf("at least one file is required")
	}
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		peer, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}

		u := uploader.NewUploader(api)
		var media []tg.InputSingleMedia

		for i, path := range filePaths {
			f, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("open %s: %w", path, err)
			}
			defer f.Close() //nolint:gocritic

			fi, err := f.Stat()
			if err != nil {
				return fmt.Errorf("stat %s: %w", path, err)
			}

			uploaded, err := u.Upload(ctx, uploader.NewUpload(filepath.Base(path), f, fi.Size()))
			if err != nil {
				return fmt.Errorf("upload %s: %w", path, err)
			}

			mimeType := mime.TypeByExtension(filepath.Ext(path))
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}

			var uploadedMedia tg.InputMediaClass
			if strings.HasPrefix(mimeType, "image/") && !strings.HasSuffix(strings.ToLower(path), ".gif") {
				uploadedMedia = &tg.InputMediaUploadedPhoto{File: uploaded, Spoiler: spoiler}
			} else {
				attrs := []tg.DocumentAttributeClass{
					&tg.DocumentAttributeFilename{FileName: filepath.Base(path)},
				}
				// Detect video so it shows as inline player (with autoplay) rather than as a file.
				if strings.HasPrefix(mimeType, "video/") {
					if w, h, dur, ok := probeVideo(path); ok {
						attrs = append(attrs, &tg.DocumentAttributeVideo{
							W:                 w,
							H:                 h,
							Duration:          dur,
							SupportsStreaming: true,
						})
					}
				}
				uploadedMedia = &tg.InputMediaUploadedDocument{
					File:       uploaded,
					MimeType:   mimeType,
					Spoiler:    spoiler,
					Attributes: attrs,
				}
			}

			// Albums require resolved photo/document IDs, not raw uploads.
			// Round-trip through MessagesUploadMedia so we get an InputMediaPhoto/Document we can group.
			res, err := api.MessagesUploadMedia(ctx, &tg.MessagesUploadMediaRequest{
				Peer:  peer.InputPeer(),
				Media: uploadedMedia,
			})
			if err != nil {
				return fmt.Errorf("upload-media %s: %w", path, err)
			}

			var groupedMedia tg.InputMediaClass
			switch m := res.(type) {
			case *tg.MessageMediaPhoto:
				p, ok := m.Photo.(*tg.Photo)
				if !ok {
					return fmt.Errorf("%s: unexpected photo type %T", path, m.Photo)
				}
				groupedMedia = &tg.InputMediaPhoto{
					ID:      &tg.InputPhoto{ID: p.ID, AccessHash: p.AccessHash, FileReference: p.FileReference},
					Spoiler: spoiler,
				}
			case *tg.MessageMediaDocument:
				d, ok := m.Document.(*tg.Document)
				if !ok {
					return fmt.Errorf("%s: unexpected document type %T", path, m.Document)
				}
				groupedMedia = &tg.InputMediaDocument{
					ID:      &tg.InputDocument{ID: d.ID, AccessHash: d.AccessHash, FileReference: d.FileReference},
					Spoiler: spoiler,
				}
			default:
				return fmt.Errorf("%s: unexpected uploadMedia result %T", path, res)
			}

			single := tg.InputSingleMedia{
				Media:    groupedMedia,
				RandomID: cryptoRandInt63(),
			}
			if i == 0 && caption != "" {
				single.Message = caption
			}
			media = append(media, single)
			fmt.Fprintf(os.Stderr, "Uploaded %s\n", filepath.Base(path))
		}

		req := &tg.MessagesSendMultiMediaRequest{
			Peer:       peer.InputPeer(),
			MultiMedia: media,
		}
		if !scheduleAt.IsZero() {
			req.ScheduleDate = int(scheduleAt.Unix())
		}
		updResp, err := api.MessagesSendMultiMedia(ctx, req)
		if err != nil {
			return err
		}
		var msgIDs []int
		switch upd := updResp.(type) {
		case *tg.Updates:
			for _, u := range upd.Updates {
				switch x := u.(type) {
				case *tg.UpdateMessageID:
					msgIDs = append(msgIDs, x.ID)
				case *tg.UpdateNewMessage:
					if m, ok := x.Message.(*tg.Message); ok {
						msgIDs = append(msgIDs, m.ID)
					}
				case *tg.UpdateNewChannelMessage:
					if m, ok := x.Message.(*tg.Message); ok {
						msgIDs = append(msgIDs, m.ID)
					}
				case *tg.UpdateNewScheduledMessage:
					if m, ok := x.Message.(*tg.Message); ok {
						msgIDs = append(msgIDs, m.ID)
					}
				}
			}
		}
		firstID := 0
		if len(msgIDs) > 0 {
			firstID = msgIDs[0]
		}
		if !scheduleAt.IsZero() {
			fmt.Fprintf(os.Stderr, "Album scheduled (%d files) for %s (id=%d)\n", len(filePaths), scheduleAt.Format("2006-01-02 15:04"), firstID)
			return printJSON(map[string]any{"status": "scheduled", "count": len(filePaths), "at": scheduleAt.Format(time.RFC3339), "id": firstID, "ids": msgIDs})
		}
		fmt.Fprintf(os.Stderr, "Album sent (%d files) (id=%d)\n", len(filePaths), firstID)
		return printJSON(map[string]any{"status": "sent", "count": len(filePaths), "id": firstID, "ids": msgIDs})
	})
}

// ── delete-user-messages ─────────────────────────────────────────────────────

// cmdDeleteUserMessages deletes all messages from a user in a channel/supergroup.
func cmdDeleteUserMessages(c config, chatName, userName string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		chatPeer, err := resolvePeer(ctx, pm, api, chatName)
		if err != nil {
			return err
		}
		userPeer, err := resolvePeer(ctx, pm, api, userName)
		if err != nil {
			return err
		}

		ch, ok := chatPeer.(peers.Channel)
		if !ok {
			return fmt.Errorf("delete-user-messages only works for channels and supergroups")
		}
		u, ok := userPeer.(peers.User)
		if !ok {
			return fmt.Errorf("%s is not a user", userName)
		}
		raw := u.Raw()

		result, err := api.ChannelsDeleteParticipantHistory(ctx, &tg.ChannelsDeleteParticipantHistoryRequest{
			Channel:     ch.InputChannel(),
			Participant: &tg.InputPeerUser{UserID: raw.ID, AccessHash: raw.AccessHash},
		})
		if err != nil {
			return err
		}
		return printJSON(map[string]any{
			"status":    "deleted",
			"pts":       result.Pts,
			"pts_count": result.PtsCount,
		})
	})
}

// ── stats ────────────────────────────────────────────────────────────────────

// cmdStats returns available statistics for a channel or supergroup.
func cmdStats(c config, name string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		peer, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		ch, ok := peer.(peers.Channel)
		if !ok {
			return fmt.Errorf("stats only available for channels and supergroups")
		}

		// Get basic info from full channel
		full, err := api.ChannelsGetFullChannel(ctx, ch.InputChannel())
		if err != nil {
			return err
		}
		chFull, ok := full.FullChat.(*tg.ChannelFull)
		if !ok {
			return fmt.Errorf("unexpected full chat type")
		}

		out := map[string]any{
			"members": chFull.ParticipantsCount,
			"online":  chFull.OnlineCount,
		}

		raw := ch.Raw()
		if raw.Broadcast {
			stats, serr := api.StatsGetBroadcastStats(ctx, &tg.StatsGetBroadcastStatsRequest{
				Channel: ch.InputChannel(),
			})
			if serr == nil {
				out["followers"] = map[string]any{
					"current":  stats.Followers.Current,
					"previous": stats.Followers.Previous,
				}
				out["views_per_post"] = map[string]any{
					"current":  stats.ViewsPerPost.Current,
					"previous": stats.ViewsPerPost.Previous,
				}
				out["shares_per_post"] = map[string]any{
					"current":  stats.SharesPerPost.Current,
					"previous": stats.SharesPerPost.Previous,
				}
				out["reactions_per_post"] = map[string]any{
					"current":  stats.ReactionsPerPost.Current,
					"previous": stats.ReactionsPerPost.Previous,
				}
				out["enabled_notifications_percent"] = map[string]any{
					"part":  stats.EnabledNotifications.Part,
					"total": stats.EnabledNotifications.Total,
				}
			} else {
				out["stats_error"] = serr.Error()
			}
		} else {
			stats, serr := api.StatsGetMegagroupStats(ctx, &tg.StatsGetMegagroupStatsRequest{
				Channel: ch.InputChannel(),
			})
			if serr == nil {
				out["members_stats"] = map[string]any{
					"current":  stats.Members.Current,
					"previous": stats.Members.Previous,
				}
				out["messages_stats"] = map[string]any{
					"current":  stats.Messages.Current,
					"previous": stats.Messages.Previous,
				}
				out["viewers_stats"] = map[string]any{
					"current":  stats.Viewers.Current,
					"previous": stats.Viewers.Previous,
				}
				out["posters_stats"] = map[string]any{
					"current":  stats.Posters.Current,
					"previous": stats.Posters.Previous,
				}
			} else {
				out["stats_error"] = serr.Error()
			}
		}

		return printJSON(out)
	})
}

// ── transcribe ───────────────────────────────────────────────────────────────

// cmdTranscribe requests server-side voice-to-text transcription for a message.
func cmdTranscribe(c config, name string, msgID int) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		peer, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}

		result, err := api.MessagesTranscribeAudio(ctx, &tg.MessagesTranscribeAudioRequest{
			Peer:  peer.InputPeer(),
			MsgID: msgID,
		})
		if err != nil {
			return err
		}

		if !result.Pending {
			return printJSON(map[string]any{
				"text":             result.Text,
				"transcription_id": result.TranscriptionID,
			})
		}

		// Poll until transcription completes (up to 60s)
		for range 30 {
			time.Sleep(2 * time.Second)
			poll, perr := api.MessagesTranscribeAudio(ctx, &tg.MessagesTranscribeAudioRequest{
				Peer:  peer.InputPeer(),
				MsgID: msgID,
			})
			if perr != nil {
				return perr
			}
			if !poll.Pending {
				return printJSON(map[string]any{
					"text":             poll.Text,
					"transcription_id": result.TranscriptionID,
				})
			}
		}

		return fmt.Errorf("transcription timed out (id: %d)", result.TranscriptionID)
	})
}

// ── restrict ─────────────────────────────────────────────────────────────────

// cmdRestrict applies granular restrictions to a user in a channel/supergroup.
func cmdRestrict(c config, chatName, userName string, until time.Time,
	noSend, noMedia, noStickers, noWebPreview, noPolls, noChangeInfo, noInvite, noPin bool) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		chatPeer, err := resolvePeer(ctx, pm, api, chatName)
		if err != nil {
			return err
		}
		userPeer, err := resolvePeer(ctx, pm, api, userName)
		if err != nil {
			return err
		}

		ch, ok := chatPeer.(peers.Channel)
		if !ok {
			return fmt.Errorf("restrict only works for channels and supergroups")
		}
		u, ok := userPeer.(peers.User)
		if !ok {
			return fmt.Errorf("%s is not a user", userName)
		}
		raw := u.Raw()

		rights := tg.ChatBannedRights{
			SendMessages: noSend,
			SendMedia:    noMedia,
			SendStickers: noStickers,
			SendGifs:     noStickers,
			EmbedLinks:   noWebPreview,
			SendPolls:    noPolls,
			ChangeInfo:   noChangeInfo,
			InviteUsers:  noInvite,
			PinMessages:  noPin,
		}
		if !until.IsZero() {
			rights.UntilDate = int(until.Unix())
		}

		_, err = api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
			Channel:      ch.InputChannel(),
			Participant:  &tg.InputPeerUser{UserID: raw.ID, AccessHash: raw.AccessHash},
			BannedRights: rights,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Restricted %s in %s\n", userName, chatName)
		return printJSON(map[string]any{"status": "restricted", "user": userName, "chat": chatName})
	})
}

// ── list-scheduled ───────────────────────────────────────────────────────────

// cmdListScheduled lists messages in the scheduled queue for a chat/channel.
func cmdListScheduled(c config, name string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		resp, err := api.MessagesGetScheduledHistory(ctx, &tg.MessagesGetScheduledHistoryRequest{
			Peer: p.InputPeer(),
			Hash: 0,
		})
		if err != nil {
			return err
		}
		var msgs []tg.MessageClass
		switch d := resp.(type) {
		case *tg.MessagesMessages:
			msgs = d.Messages
		case *tg.MessagesMessagesSlice:
			msgs = d.Messages
		case *tg.MessagesChannelMessages:
			msgs = d.Messages
		case *tg.MessagesMessagesNotModified:
			return printJSON(map[string]any{"chat": name, "scheduled_count": 0, "scheduled": []any{}})
		}

		type row struct {
			ID      int    `json:"id"`
			Date    int    `json:"date"`
			Caption string `json:"caption,omitempty"`
			HasMedia bool  `json:"has_media,omitempty"`
		}
		out := make([]row, 0, len(msgs))
		for _, m := range msgs {
			if mm, ok := m.(*tg.Message); ok {
				out = append(out, row{
					ID:       mm.ID,
					Date:     mm.Date,
					Caption:  mm.Message,
					HasMedia: mm.Media != nil,
				})
			}
		}
		return printJSON(map[string]any{
			"chat":            name,
			"scheduled_count": len(out),
			"scheduled":       out,
		})
	})
}

// ── set-discussion ───────────────────────────────────────────────────────────

// cmdSetDiscussion links a broadcast channel with a megagroup as its discussion.
// Pass empty groupName to unlink the existing discussion group.
func cmdSetDiscussion(c config, channelName, groupName string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		chPeer, err := resolvePeer(ctx, pm, api, channelName)
		if err != nil {
			return err
		}
		ch, ok := chPeer.(peers.Channel)
		if !ok {
			return fmt.Errorf("%s is not a channel", channelName)
		}

		var groupInput tg.InputChannelClass = &tg.InputChannelEmpty{}
		if groupName != "" {
			gPeer, err := resolvePeer(ctx, pm, api, groupName)
			if err != nil {
				return fmt.Errorf("resolve group %s: %w", groupName, err)
			}
			g, ok := gPeer.(peers.Channel)
			if !ok {
				return fmt.Errorf("%s is not a supergroup", groupName)
			}
			groupInput = g.InputChannel()
		}

		_, err = api.ChannelsSetDiscussionGroup(ctx, &tg.ChannelsSetDiscussionGroupRequest{
			Broadcast: ch.InputChannel(),
			Group:     groupInput,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Discussion %s linked to %s\n", groupName, channelName)
		return printJSON(map[string]any{
			"status":  "linked",
			"channel": channelName,
			"group":   groupName,
		})
	})
}

// ── comment ──────────────────────────────────────────────────────────────────

// cmdComment posts a comment under a channel post. Resolves the linked
// discussion group thread root via MessagesGetDiscussionMessage, then sends
// a reply to that root in the linked group.
func cmdComment(c config, channelName string, msgID int, text string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		chPeer, err := resolvePeer(ctx, pm, api, channelName)
		if err != nil {
			return err
		}
		disc, err := api.MessagesGetDiscussionMessage(ctx, &tg.MessagesGetDiscussionMessageRequest{
			Peer:  chPeer.InputPeer(),
			MsgID: msgID,
		})
		if err != nil {
			return fmt.Errorf("get discussion msg: %w", err)
		}
		if len(disc.Messages) == 0 {
			return fmt.Errorf("no discussion thread for msg %d (is the channel linked to a group?)", msgID)
		}

		root := disc.Messages[0]
		rootMsg, ok := root.(*tg.Message)
		if !ok {
			return fmt.Errorf("unexpected root msg type: %T", root)
		}

		var groupPeer tg.InputPeerClass
		switch p := rootMsg.PeerID.(type) {
		case *tg.PeerChannel:
			for _, ch := range disc.Chats {
				if channel, ok := ch.(*tg.Channel); ok && channel.ID == p.ChannelID {
					groupPeer = &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
					break
				}
			}
		}
		if groupPeer == nil {
			return fmt.Errorf("could not resolve linked group peer")
		}

		_, err = api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
			Peer:     groupPeer,
			Message:  text,
			RandomID: cryptoRandInt63(),
			ReplyTo: &tg.InputReplyToMessage{
				ReplyToMsgID: rootMsg.ID,
				TopMsgID:     rootMsg.ID,
			},
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Comment sent to %s msg #%d (thread root #%d)\n", channelName, msgID, rootMsg.ID)
		return nil
	})
}

// ── create-channel ───────────────────────────────────────────────────────────

// cmdCreateChannel creates a new broadcast channel or supergroup.
func cmdCreateChannel(c config, title string, isMegagroup bool, username string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		result, err := api.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{
			Title:     title,
			Broadcast: !isMegagroup,
			Megagroup: isMegagroup,
		})
		if err != nil {
			return err
		}

		var channelID int64
		var accessHash int64
		if upd, ok := result.(*tg.Updates); ok {
			for _, ch := range upd.Chats {
				if channel, ok := ch.(*tg.Channel); ok {
					channelID = channel.ID
					accessHash = channel.AccessHash
					break
				}
			}
		}

		if username != "" && channelID != 0 {
			username = strings.TrimPrefix(username, "@")
			_, err = api.ChannelsUpdateUsername(ctx, &tg.ChannelsUpdateUsernameRequest{
				Channel:  &tg.InputChannel{ChannelID: channelID, AccessHash: accessHash},
				Username: username,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: channel created but username failed: %v\n", err)
			}
		}

		chanType := "channel"
		if isMegagroup {
			chanType = "supergroup"
		}
		fmt.Fprintf(os.Stderr, "%s %q created (id: %d)\n", chanType, title, channelID)
		return printJSON(map[string]any{
			"status":     "created",
			"title":      title,
			"type":       chanType,
			"channel_id": channelID,
			"username":   username,
		})
	})
}

// ── sessions ─────────────────────────────────────────────────────────────────

// cmdSessions lists all active authorized sessions for the current account.
func cmdSessions(c config) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		result, err := api.AccountGetAuthorizations(ctx)
		if err != nil {
			return err
		}

		type sessionInfo struct {
			Hash          int64  `json:"hash"`
			Current       bool   `json:"current,omitempty"`
			AppName       string `json:"app_name"`
			DeviceModel   string `json:"device_model"`
			Platform      string `json:"platform"`
			SystemVersion string `json:"system_version"`
			DateCreated   int    `json:"date_created"`
			DateActive    int    `json:"date_active"`
			IP            string `json:"ip"`
			Country       string `json:"country"`
		}

		var sessions []sessionInfo
		for _, a := range result.Authorizations {
			sessions = append(sessions, sessionInfo{
				Hash:          a.Hash,
				Current:       a.Current,
				AppName:       a.AppName,
				DeviceModel:   a.DeviceModel,
				Platform:      a.Platform,
				SystemVersion: a.SystemVersion,
				DateCreated:   a.DateCreated,
				DateActive:    a.DateActive,
				IP:            a.IP,
				Country:       a.Country,
			})
		}
		return printJSON(map[string]any{"sessions": sessions, "total": len(sessions)})
	})
}

// cmdSessionsRevoke terminates a specific session by its hash.
func cmdSessionsRevoke(c config, hash int64) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		_, err := api.AccountResetAuthorization(ctx, hash)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Session %d revoked\n", hash)
		return printJSON(map[string]any{"status": "revoked", "hash": hash})
	})
}

// ── block / unblock / blocked ────────────────────────────────────────────────

// cmdBlock blocks a user so they can't send messages to the current account.
func cmdBlock(c config, name string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		peer, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		_, err = api.ContactsBlock(ctx, &tg.ContactsBlockRequest{
			ID: peer.InputPeer(),
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Blocked %s\n", name)
		return printJSON(map[string]any{"status": "blocked", "user": name})
	})
}

// cmdUnblock removes a block on a user.
func cmdUnblock(c config, name string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		peer, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		_, err = api.ContactsUnblock(ctx, &tg.ContactsUnblockRequest{
			ID: peer.InputPeer(),
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Unblocked %s\n", name)
		return printJSON(map[string]any{"status": "unblocked", "user": name})
	})
}

// cmdBlocked lists all blocked users.
func cmdBlocked(c config) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		result, err := api.ContactsGetBlocked(ctx, &tg.ContactsGetBlockedRequest{
			Limit: 200,
		})
		if err != nil {
			return err
		}

		type blockedUser struct {
			ID        int64  `json:"id"`
			Username  string `json:"username,omitempty"`
			FirstName string `json:"first_name,omitempty"`
			LastName  string `json:"last_name,omitempty"`
		}

		var blocked []blockedUser
		var users []tg.UserClass
		switch r := result.(type) {
		case *tg.ContactsBlocked:
			users = r.Users
		case *tg.ContactsBlockedSlice:
			users = r.Users
		}
		for _, u := range users {
			if usr, ok := u.(*tg.User); ok {
				blocked = append(blocked, blockedUser{
					ID:        usr.ID,
					Username:  usr.Username,
					FirstName: usr.FirstName,
					LastName:  usr.LastName,
				})
			}
		}
		return printJSON(map[string]any{"blocked": blocked, "total": len(blocked)})
	})
}

// ── delete-history ───────────────────────────────────────────────────────────

// cmdDeleteHistory clears the conversation history with a user or in a group.
// revoke=true also deletes on the other side (only works for private chats).
func cmdDeleteHistory(c config, name string, revoke bool) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		peer, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		result, err := api.MessagesDeleteHistory(ctx, &tg.MessagesDeleteHistoryRequest{
			Peer:   peer.InputPeer(),
			MaxID:  0,
			Revoke: revoke,
		})
		if err != nil {
			return err
		}
		return printJSON(map[string]any{
			"status":    "deleted",
			"pts":       result.Pts,
			"pts_count": result.PtsCount,
		})
	})
}

// ── archive / unarchive ──────────────────────────────────────────────────────

// cmdArchive moves a dialog to the Archive folder (folder_id=1).
func cmdArchive(c config, name string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		peer, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		_, err = api.FoldersEditPeerFolders(ctx, []tg.InputFolderPeer{
			{Peer: peer.InputPeer(), FolderID: 1},
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Archived %s\n", name)
		return printJSON(map[string]any{"status": "archived", "dialog": name})
	})
}

// cmdUnarchive moves a dialog back to the main folder (folder_id=0).
func cmdUnarchive(c config, name string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		peer, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		_, err = api.FoldersEditPeerFolders(ctx, []tg.InputFolderPeer{
			{Peer: peer.InputPeer(), FolderID: 0},
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Unarchived %s\n", name)
		return printJSON(map[string]any{"status": "unarchived", "dialog": name})
	})
}

// ── message-link ─────────────────────────────────────────────────────────────

// cmdMessageLink generates a t.me link to a specific message.
func cmdMessageLink(c config, name string, msgID int) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		peer, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		ch, ok := peer.(peers.Channel)
		if !ok {
			return fmt.Errorf("message links are only available for channels and supergroups")
		}
		raw := ch.Raw()
		var link string
		if raw.Username != "" {
			link = fmt.Sprintf("https://t.me/%s/%d", raw.Username, msgID)
		} else {
			// Private channel: use c/<channelID>/<msgID>
			link = fmt.Sprintf("https://t.me/c/%d/%d", raw.ID, msgID)
		}
		return printJSON(map[string]any{"link": link, "message_id": msgID})
	})
}

// ── search-members ────────────────────────────────────────────────────────────

// cmdSearchMembers searches group/channel members by name or username.
// Uses ChannelParticipantsSearch — works without admin rights.
func cmdSearchMembers(c config, name, query string, limit int) error {
	if limit <= 0 {
		limit = 50
	}
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}
		ch, ok := p.(peers.Channel)
		if !ok {
			return fmt.Errorf("search-members only works for channels and supergroups")
		}

		type memberInfo struct {
			ID        int64  `json:"id"`
			Username  string `json:"username,omitempty"`
			FirstName string `json:"first_name,omitempty"`
			LastName  string `json:"last_name,omitempty"`
		}
		var out []memberInfo

		batchSize := limit
		if batchSize > 200 {
			batchSize = 200
		}
		result, err := api.ChannelsGetParticipants(ctx, &tg.ChannelsGetParticipantsRequest{
			Channel: ch.InputChannel(),
			Filter:  &tg.ChannelParticipantsSearch{Q: query},
			Offset:  0,
			Limit:   batchSize,
		})
		if err != nil {
			return err
		}
		v, ok := result.(*tg.ChannelsChannelParticipants)
		if !ok {
			return fmt.Errorf("unexpected response type")
		}
		for _, u := range v.Users {
			if usr, ok := u.(*tg.User); ok {
				out = append(out, memberInfo{
					ID:        usr.ID,
					Username:  usr.Username,
					FirstName: usr.FirstName,
					LastName:  usr.LastName,
				})
			}
		}

		return printJSON(map[string]any{
			"members": out,
			"total":   len(out),
			"query":   query,
			"dialog":  name,
		})
	})
}

// ── parse-members ─────────────────────────────────────────────────────────────

// cmdParseMembers fetches all members from a channel/supergroup and writes them
// as CSV (or JSON with --format json). CSV columns: id,username,first_name,last_name,phone.
func cmdParseMembers(c config, name string, limit int, outFile string, format string) error {
	if limit <= 0 {
		limit = 5000
	}
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}

		type memberRow struct {
			ID        int64  `json:"id"`
			Username  string `json:"username,omitempty"`
			FirstName string `json:"first_name,omitempty"`
			LastName  string `json:"last_name,omitempty"`
			Phone     string `json:"phone,omitempty"`
		}
		var rows []memberRow

		fetchPage := func(filter tg.ChannelParticipantsFilterClass, offset, batchSize int) (int, error) {
			ch, ok := p.(peers.Channel)
			if !ok {
				return 0, nil
			}
			result, err := api.ChannelsGetParticipants(ctx, &tg.ChannelsGetParticipantsRequest{
				Channel: ch.InputChannel(),
				Filter:  filter,
				Offset:  offset,
				Limit:   batchSize,
			})
			if err != nil {
				return 0, err
			}
			v, ok := result.(*tg.ChannelsChannelParticipants)
			if !ok {
				return 0, nil
			}
			for _, u := range v.Users {
				if usr, ok := u.(*tg.User); ok {
					rows = append(rows, memberRow{
						ID:        usr.ID,
						Username:  usr.Username,
						FirstName: usr.FirstName,
						LastName:  usr.LastName,
						Phone:     usr.Phone,
					})
				}
			}
			return len(v.Users), nil
		}

		// Iterate with ChannelParticipantsRecent (no admin rights needed for supergroups)
		switch p.(type) {
		case peers.Channel:
			offset := 0
			for len(rows) < limit {
				remaining := limit - len(rows)
				batchSize := remaining
				if batchSize > 200 {
					batchSize = 200
				}
				n, ferr := fetchPage(&tg.ChannelParticipantsRecent{}, offset, batchSize)
				if ferr != nil {
					return ferr
				}
				offset += n
				if n < batchSize {
					break
				}
				fmt.Fprintf(os.Stderr, "Fetched %d members...\n", len(rows))
			}
		case peers.Chat:
			full, ferr := api.MessagesGetFullChat(ctx, p.(peers.Chat).Raw().ID)
			if ferr != nil {
				return ferr
			}
			for _, u := range full.Users {
				if usr, ok := u.(*tg.User); ok {
					rows = append(rows, memberRow{
						ID:        usr.ID,
						Username:  usr.Username,
						FirstName: usr.FirstName,
						LastName:  usr.LastName,
						Phone:     usr.Phone,
					})
					if len(rows) >= limit {
						break
					}
				}
			}
		default:
			return fmt.Errorf("parse-members only works for groups and channels")
		}

		fmt.Fprintf(os.Stderr, "Total members collected: %d\n", len(rows))

		// Output destination
		out := os.Stdout
		if outFile != "" {
			f, ferr := os.Create(outFile)
			if ferr != nil {
				return fmt.Errorf("create output file: %w", ferr)
			}
			defer f.Close()
			out = f
		}

		if format == "json" {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{"members": rows, "total": len(rows)})
		}

		// Default: CSV
		fmt.Fprintln(out, "id,username,first_name,last_name,phone")
		csvEscape := func(s string) string {
			if strings.ContainsAny(s, ",\"\n\r") {
				return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
			}
			return s
		}
		for _, r := range rows {
			fmt.Fprintf(out, "%d,%s,%s,%s,%s\n",
				r.ID,
				csvEscape(r.Username),
				csvEscape(r.FirstName),
				csvEscape(r.LastName),
				csvEscape(r.Phone),
			)
		}
		return nil
	})
}

// ── active-members ────────────────────────────────────────────────────────────

// cmdActiveMembers scans recent messages to find users who sent messages
// in the last N days. Returns a deduplicated list sorted by message count.
func cmdActiveMembers(c config, name string, days int, outFile string) error {
	if days <= 0 {
		days = 30
	}
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}

		since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)

		type senderStats struct {
			ID        int64
			Username  string
			FirstName string
			LastName  string
			Count     int
		}
		senderMap := make(map[int64]*senderStats)

		offsetID := 0
		totalScanned := 0
		for {
			result, ferr := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
				Peer:     p.InputPeer(),
				OffsetID: offsetID,
				Limit:    200,
			})
			if ferr != nil {
				return ferr
			}
			msgs, _, users, ferr := extractHistoryMessages(result)
			if ferr != nil {
				return ferr
			}
			if len(msgs) == 0 {
				break
			}

			// Build user lookup from this batch
			userByID := make(map[int64]*tg.User)
			for _, u := range users {
				if usr, ok := u.(*tg.User); ok {
					userByID[usr.ID] = usr
				}
			}

			done := false
			for _, m := range msgs {
				msg, ok := m.(*tg.Message)
				if !ok {
					continue
				}
				msgTime := time.Unix(int64(msg.Date), 0)
				if msgTime.Before(since) {
					done = true
					break
				}
				totalScanned++
				if msg.FromID == nil {
					continue
				}
				uid := senderID(msg.FromID)
				if uid == 0 {
					continue
				}
				if _, exists := senderMap[uid]; !exists {
					st := &senderStats{ID: uid}
					if usr, ok := userByID[uid]; ok {
						st.Username = usr.Username
						st.FirstName = usr.FirstName
						st.LastName = usr.LastName
					}
					senderMap[uid] = st
				}
				senderMap[uid].Count++
				offsetID = msg.ID
			}

			fmt.Fprintf(os.Stderr, "Scanned %d messages...\n", totalScanned)
			if done || len(msgs) < 200 {
				break
			}
		}

		type activeMember struct {
			ID        int64  `json:"id"`
			Username  string `json:"username,omitempty"`
			FirstName string `json:"first_name,omitempty"`
			LastName  string `json:"last_name,omitempty"`
			Messages  int    `json:"messages"`
		}
		var members []activeMember
		for _, s := range senderMap {
			members = append(members, activeMember{
				ID:        s.ID,
				Username:  s.Username,
				FirstName: s.FirstName,
				LastName:  s.LastName,
				Messages:  s.Count,
			})
		}
		// Sort by message count descending
		for i := 0; i < len(members)-1; i++ {
			for j := i + 1; j < len(members); j++ {
				if members[j].Messages > members[i].Messages {
					members[i], members[j] = members[j], members[i]
				}
			}
		}

		fmt.Fprintf(os.Stderr, "Found %d active members in last %d days\n", len(members), days)

		result := map[string]any{
			"members":        members,
			"total":          len(members),
			"days":           days,
			"messages_scanned": totalScanned,
			"dialog":         name,
		}

		if outFile != "" {
			f, ferr := os.Create(outFile)
			if ferr != nil {
				return fmt.Errorf("create output file: %w", ferr)
			}
			defer f.Close()
			enc := json.NewEncoder(f)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		return printJSON(result)
	})
}

// ── readTargetsFromFile ──────────────────────────────────────────────────────

// readTargetsFromFile reads one target (username/ID) per line.
// Pass "-" as file to read from stdin.
func readTargetsFromFile(file string) ([]string, error) {
	var r io.Reader
	if file == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(file)
		if err != nil {
			return nil, fmt.Errorf("open %q: %w", file, err)
		}
		defer f.Close()
		r = f
	}
	scanner := bufio.NewScanner(r)
	var out []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out, scanner.Err()
}

// ── broadcast ────────────────────────────────────────────────────────────────

func cmdBroadcast(c config, file, msg string, delay time.Duration, limit int, parseMode string) error {
	targets, err := readTargetsFromFile(file)
	if err != nil {
		return err
	}
	if limit > 0 && len(targets) > limit {
		targets = targets[:limit]
	}
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		type bcastResult struct {
			Target string `json:"target"`
			Status string `json:"status"`
			MsgID  int    `json:"msg_id,omitempty"`
			Error  string `json:"error,omitempty"`
		}
		msgText := msg
		var entities []tg.MessageEntityClass
		switch strings.ToLower(parseMode) {
		case "html":
			msgText, entities = parseHTMLEntities(msg)
		case "markdown", "md":
			msgText, entities = parseMarkdownEntities(msg)
		}
		var results []bcastResult
		for i, target := range targets {
			if i > 0 && delay > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(delay):
				}
			}
			peer, rerr := resolvePeer(ctx, pm, api, target)
			if rerr != nil {
				results = append(results, bcastResult{Target: target, Status: "error", Error: rerr.Error()})
				fmt.Fprintf(os.Stderr, "[%d/%d] %s: error: %v\n", i+1, len(targets), target, rerr)
				continue
			}
			req := &tg.MessagesSendMessageRequest{
				Peer:     peer.InputPeer(),
				Message:  msgText,
				RandomID: cryptoRandInt63(),
			}
			if len(entities) > 0 {
				req.Entities = entities
			}
			upds, serr := api.MessagesSendMessage(ctx, req)
			if serr != nil {
				results = append(results, bcastResult{Target: target, Status: "error", Error: serr.Error()})
				fmt.Fprintf(os.Stderr, "[%d/%d] %s: error: %v\n", i+1, len(targets), target, serr)
				continue
			}
			msgID := 0
			if u, ok := upds.(*tg.Updates); ok {
				for _, upd := range u.Updates {
					if mu, ok2 := upd.(*tg.UpdateMessageID); ok2 {
						msgID = mu.ID
						break
					}
				}
			}
			results = append(results, bcastResult{Target: target, Status: "ok", MsgID: msgID})
			fmt.Fprintf(os.Stderr, "[%d/%d] %s: sent\n", i+1, len(targets), target)
		}
		sent := 0
		for _, r := range results {
			if r.Status == "ok" {
				sent++
			}
		}
		return printJSON(map[string]any{
			"sent":    sent,
			"failed":  len(results) - sent,
			"total":   len(results),
			"results": results,
		})
	})
}

// ── enrich ───────────────────────────────────────────────────────────────────

func cmdEnrich(c config, file, outFile string) error {
	targets, err := readTargetsFromFile(file)
	if err != nil {
		return err
	}
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		type enrichedMember struct {
			ID        int64  `json:"id"`
			Username  string `json:"username,omitempty"`
			FirstName string `json:"first_name,omitempty"`
			LastName  string `json:"last_name,omitempty"`
			Phone     string `json:"phone,omitempty"`
			Bio       string `json:"bio,omitempty"`
			Error     string `json:"error,omitempty"`
		}
		var results []enrichedMember
		for i, target := range targets {
			peer, rerr := resolvePeer(ctx, pm, api, target)
			if rerr != nil {
				results = append(results, enrichedMember{Error: rerr.Error()})
				fmt.Fprintf(os.Stderr, "[%d/%d] %s: %v\n", i+1, len(targets), target, rerr)
				continue
			}
			user, ok := peer.(peers.User)
			if !ok {
				results = append(results, enrichedMember{Error: "not a user"})
				fmt.Fprintf(os.Stderr, "[%d/%d] %s: not a user\n", i+1, len(targets), target)
				continue
			}
			raw := user.Raw()
			item := enrichedMember{
				ID:        raw.ID,
				Username:  raw.Username,
				FirstName: raw.FirstName,
				LastName:  raw.LastName,
				Phone:     raw.Phone,
			}
			if full, ferr := api.UsersGetFullUser(ctx, user.InputUser()); ferr == nil {
				item.Bio = full.FullUser.About
			}
			results = append(results, item)
			fmt.Fprintf(os.Stderr, "[%d/%d] %s: ok\n", i+1, len(targets), target)
		}
		out := map[string]any{
			"count":   len(results),
			"members": results,
		}
		if outFile != "" {
			f, ferr := os.Create(outFile)
			if ferr != nil {
				return fmt.Errorf("create output file: %w", ferr)
			}
			defer f.Close()
			enc := json.NewEncoder(f)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}
		return printJSON(out)
	})
}

// ── mass-invite ───────────────────────────────────────────────────────────────

func cmdMassInvite(c config, group, file string, delay time.Duration, limit int) error {
	targets, err := readTargetsFromFile(file)
	if err != nil {
		return err
	}
	if limit > 0 && len(targets) > limit {
		targets = targets[:limit]
	}
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		grpPeer, err := resolvePeer(ctx, pm, api, group)
		if err != nil {
			return fmt.Errorf("resolve group: %w", err)
		}
		type invResult struct {
			Target string `json:"target"`
			Status string `json:"status"`
			Error  string `json:"error,omitempty"`
		}
		var results []invResult
		for i, target := range targets {
			if i > 0 && delay > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(delay):
				}
			}
			userPeer, rerr := resolvePeer(ctx, pm, api, target)
			if rerr != nil {
				results = append(results, invResult{Target: target, Status: "error", Error: rerr.Error()})
				fmt.Fprintf(os.Stderr, "[%d/%d] %s: %v\n", i+1, len(targets), target, rerr)
				continue
			}
			user, ok := userPeer.(peers.User)
			if !ok {
				results = append(results, invResult{Target: target, Status: "error", Error: "not a user"})
				fmt.Fprintf(os.Stderr, "[%d/%d] %s: not a user\n", i+1, len(targets), target)
				continue
			}
			var invErr error
			switch ch := grpPeer.(type) {
			case peers.Channel:
				_, invErr = api.ChannelsInviteToChannel(ctx, &tg.ChannelsInviteToChannelRequest{
					Channel: ch.InputChannel(),
					Users:   []tg.InputUserClass{user.InputUser()},
				})
			case peers.Chat:
				_, invErr = api.MessagesAddChatUser(ctx, &tg.MessagesAddChatUserRequest{
					ChatID:   ch.Raw().ID,
					UserID:   user.InputUser(),
					FwdLimit: 100,
				})
			default:
				invErr = fmt.Errorf("target must be a channel or group")
			}
			if invErr != nil {
				results = append(results, invResult{Target: target, Status: "error", Error: invErr.Error()})
				fmt.Fprintf(os.Stderr, "[%d/%d] %s: %v\n", i+1, len(targets), target, invErr)
				continue
			}
			results = append(results, invResult{Target: target, Status: "ok"})
			fmt.Fprintf(os.Stderr, "[%d/%d] %s: invited\n", i+1, len(targets), target)
		}
		invited := 0
		for _, r := range results {
			if r.Status == "ok" {
				invited++
			}
		}
		return printJSON(map[string]any{
			"invited": invited,
			"failed":  len(results) - invited,
			"total":   len(results),
			"results": results,
		})
	})
}

// ── set-profile ───────────────────────────────────────────────────────────────

func cmdSetProfile(c config, firstName, lastName, bio, username string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		if firstName == "" && lastName == "" && bio == "" && username == "" {
			return fmt.Errorf("specify at least one of --first-name, --last-name, --bio, --username")
		}
		if firstName != "" || lastName != "" || bio != "" {
			req := &tg.AccountUpdateProfileRequest{}
			if firstName != "" {
				req.SetFirstName(firstName)
			}
			if lastName != "" {
				req.SetLastName(lastName)
			}
			if bio != "" {
				req.SetAbout(bio)
			}
			if _, err := api.AccountUpdateProfile(ctx, req); err != nil {
				return fmt.Errorf("update profile: %w", err)
			}
		}
		if username != "" {
			u := strings.TrimPrefix(username, "@")
			if _, err := api.AccountUpdateUsername(ctx, u); err != nil {
				return fmt.Errorf("update username: %w", err)
			}
		}
		self, err := client.Self(ctx)
		if err != nil {
			return err
		}
		return printJSON(map[string]any{
			"status":     "updated",
			"id":         self.ID,
			"username":   self.Username,
			"first_name": self.FirstName,
			"last_name":  self.LastName,
		})
	})
}

// ── set-profile-photo ─────────────────────────────────────────────────────────

func cmdSetProfilePhoto(c config, path string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open file: %w", err)
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		u := uploader.NewUploader(api)
		uploaded, err := u.Upload(ctx, uploader.NewUpload(filepath.Base(path), f, fi.Size()))
		if err != nil {
			return fmt.Errorf("upload: %w", err)
		}
		_, err = api.PhotosUploadProfilePhoto(ctx, &tg.PhotosUploadProfilePhotoRequest{
			File: uploaded,
		})
		if err != nil {
			return fmt.Errorf("set profile photo: %w", err)
		}
		fmt.Fprintln(os.Stderr, "Profile photo updated")
		return printJSON(map[string]any{"status": "updated"})
	})
}

// ── poll ──────────────────────────────────────────────────────────────────────

func cmdPoll(c config, target, question string, options []string, anonymous, multiple bool) error {
	if len(options) < 2 {
		return fmt.Errorf("a poll requires at least 2 options")
	}
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		peer, err := resolvePeer(ctx, pm, api, target)
		if err != nil {
			return err
		}
		var answers []tg.PollAnswer
		for i, opt := range options {
			answers = append(answers, tg.PollAnswer{
				Text:   tg.TextWithEntities{Text: opt},
				Option: []byte{byte(i + 1)},
			})
		}
		poll := tg.Poll{
			ID:             cryptoRandInt63(),
			Question:       tg.TextWithEntities{Text: question},
			Answers:        answers,
			PublicVoters:   !anonymous,
			MultipleChoice: multiple,
		}
		_, err = api.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
			Peer:     peer.InputPeer(),
			Media:    &tg.InputMediaPoll{Poll: poll},
			RandomID: cryptoRandInt63(),
		})
		if err != nil {
			return fmt.Errorf("send poll: %w", err)
		}
		fmt.Fprintln(os.Stderr, "Poll sent")
		return printJSON(map[string]any{
			"status":   "sent",
			"question": question,
			"options":  options,
		})
	})
}

// ── resolve ───────────────────────────────────────────────────────────────────

func cmdResolve(c config, query string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		peer, err := resolvePeer(ctx, pm, api, query)
		if err != nil {
			return err
		}
		result := map[string]any{}
		switch p := peer.(type) {
		case peers.User:
			raw := p.Raw()
			result["type"] = "user"
			result["id"] = raw.ID
			result["username"] = raw.Username
			result["first_name"] = raw.FirstName
			result["last_name"] = raw.LastName
			result["phone"] = raw.Phone
		case peers.Channel:
			raw := p.Raw()
			t := "channel"
			if raw.Megagroup {
				t = "supergroup"
			}
			result["type"] = t
			result["id"] = raw.ID
			result["username"] = raw.Username
			result["title"] = raw.Title
		case peers.Chat:
			raw := p.Raw()
			result["type"] = "group"
			result["id"] = raw.ID
			result["title"] = raw.Title
		}
		return printJSON(result)
	})
}
