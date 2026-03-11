package main

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

func cmdDialogs(c config, onlyUnread bool, limit int) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		// Accumulated entity maps across pages
		usersMap := make(map[int64]*tg.User)
		chatsMap := make(map[int64]*tg.Chat)
		channelsMap := make(map[int64]*tg.Channel)

		var allDialogs []tg.DialogClass

		offsetDate := 0
		offsetID := 0
		var offsetPeer tg.InputPeerClass = &tg.InputPeerEmpty{}

		for {
			batchSize := 100
			if limit > 0 {
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
				}
			case *tg.PeerChannel:
				info.ID = p.ChannelID
				if ch, ok := channelsMap[p.ChannelID]; ok {
					info.Name = ch.Title
					info.Username = ch.Username
					if ch.Broadcast {
						info.Type = "channel"
					} else {
						info.Type = "supergroup"
					}
				}
			}
			if info.Name != "" {
				out = append(out, info)
			}
		}
		return printJSON(out)
	})
}

func cmdRead(c config, name string, offsetID int, since time.Time) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, name)
		if err != nil {
			return err
		}

		if !since.IsZero() {
			return readSince(ctx, api, p, since)
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
		formatted := formatMessages(msgs, em)

		// Offset for pagination = ID of last message
		offset := 0
		if len(msgs) > 0 {
			if last, ok := msgs[len(msgs)-1].(*tg.Message); ok {
				offset = last.ID
			}
		}

		return printJSON(map[string]any{
			"messages": formatted,
			"offset":   offset,
		})
	})
}

// readSince fetches all messages newer than the cutoff time (paginating as needed).
func readSince(ctx context.Context, api *tg.Client, p peers.Peer, since time.Time) error {
	cutoff := int(since.Unix())
	var all []tgMsg
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
			if offsetID == 0 || msg.ID < offsetID {
				offsetID = msg.ID
			}
		}
		if done || len(msgs) < 100 {
			break
		}
	}

	// Reverse to chronological order (getHistory returns newest first)
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}

	return printJSON(map[string]any{
		"messages": all,
		"total":    len(all),
		"since":    since.UTC().Format(time.RFC3339),
	})
}

func cmdReply(c config, name string, msgID int, text string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, name)
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

func cmdSend(c config, name, text string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, name)
		if err != nil {
			return err
		}

		_, err = api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
			Peer:     p.InputPeer(),
			Message:  text,
			RandomID: cryptoRandInt63(),
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Message sent")
		return nil
	})
}

func cmdMarkRead(c config, name string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, name)
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

		p, err := resolvePeer(ctx, pm, target)
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
		p, err := resolvePeer(ctx, pm, name)
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
		p, err := resolvePeer(ctx, pm, dialog)
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
		p, err := resolvePeer(ctx, pm, name)
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
		p, err := resolvePeer(ctx, pm, name)
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
		p, err := resolvePeer(ctx, pm, name)
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
		from, err := resolvePeer(ctx, pm, fromName)
		if err != nil {
			return fmt.Errorf("source: %w", err)
		}
		to, err := resolvePeer(ctx, pm, toName)
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
		p, err := resolvePeer(ctx, pm, name)
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
		p, err := resolvePeer(ctx, pm, name)
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
			return printJSON(map[string]any{
				"type":        t,
				"id":          ch.ID,
				"title":       ch.Title,
				"username":    ch.Username,
				"members":     members,
				"description": about,
			})
		case peers.Chat:
			ch := peer.Raw()
			about := ""
			if full, ferr := api.MessagesGetFullChat(ctx, ch.ID); ferr == nil {
				if fc, ok := full.FullChat.(*tg.ChatFull); ok {
					about = fc.About
				}
			}
			return printJSON(map[string]any{
				"type":        "group",
				"id":          ch.ID,
				"title":       ch.Title,
				"members":     ch.ParticipantsCount,
				"description": about,
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
		p, err := resolvePeer(ctx, pm, name)
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

func cmdWatch(c config, name string, intervalSecs int) error {
	if intervalSecs <= 0 {
		intervalSecs = 5
	}
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, name)
		if err != nil {
			return err
		}

		// Seed lastID from the most recent message so we only emit new ones
		lastID := 0
		result, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:  p.InputPeer(),
			Limit: 1,
		})
		if err != nil {
			return err
		}
		if msgs, _, _, _ := extractHistoryMessages(result); len(msgs) > 0 {
			if msg, ok := msgs[0].(*tg.Message); ok {
				lastID = msg.ID
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
						_ = printJSON(msg)
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
		from, err := resolvePeer(ctx, pm, fromName)
		if err != nil {
			return fmt.Errorf("source: %w", err)
		}
		to, err := resolvePeer(ctx, pm, toName)
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
		p, err := resolvePeer(ctx, pm, name)
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
		p, err := resolvePeer(ctx, pm, name)
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
		p, err := resolvePeer(ctx, pm, name)
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
		p, err := resolvePeer(ctx, pm, name)
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
		p, err := resolvePeer(ctx, pm, name)
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
		p, err := resolvePeer(ctx, pm, name)
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
		p, err := resolvePeer(ctx, pm, name)
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
		p, err := resolvePeer(ctx, pm, name)
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
		p, err := resolvePeer(ctx, pm, name)
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
		p, err := resolvePeer(ctx, pm, name)
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
		p, err := resolvePeer(ctx, pm, name)
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
		p, err := resolvePeer(ctx, pm, name)
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
		p, err := resolvePeer(ctx, pm, name)
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

// cmdInvite invites a user or bot to a group/channel.
// groupName may be a username, dialog name, or a raw numeric chat ID for regular groups.
func cmdInvite(c config, groupName, userName string) error {
	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		// Resolve the target user first.
		userPeer, err := resolvePeer(ctx, pm, userName)
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

		group, err := resolvePeer(ctx, pm, groupName)
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
		p, err := resolvePeer(ctx, pm, name)
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
			p, err := resolvePeer(ctx, pm, name)
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
