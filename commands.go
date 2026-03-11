package main

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
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
