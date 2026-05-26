package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
)

// dumpedMsg is the on-disk representation of a single message.
type dumpedMsg struct {
	ID         int                      `json:"id"`
	Date       string                   `json:"date"`
	UnixDate   int                      `json:"unix_date"`
	Text       string                   `json:"text,omitempty"`
	Entities   []map[string]any         `json:"entities,omitempty"`
	GroupedID  int64                    `json:"grouped_id,omitempty"`
	ReplyTo    int                      `json:"reply_to,omitempty"`
	Views      int                      `json:"views,omitempty"`
	Forwards   int                      `json:"forwards,omitempty"`
	PostAuthor string                   `json:"post_author,omitempty"`
	Pinned     bool                     `json:"pinned,omitempty"`
	Reactions  []reactionInfo           `json:"reactions,omitempty"`
	Media      map[string]any           `json:"media,omitempty"`
	Forward    map[string]any           `json:"forward,omitempty"`
	Webpage    map[string]any           `json:"webpage,omitempty"`
}

func serializeEntities(ents []tg.MessageEntityClass) []map[string]any {
	if len(ents) == 0 {
		return nil
	}
	var out []map[string]any
	for _, e := range ents {
		m := map[string]any{}
		switch v := e.(type) {
		case *tg.MessageEntityBold:
			m["type"], m["offset"], m["length"] = "bold", v.Offset, v.Length
		case *tg.MessageEntityItalic:
			m["type"], m["offset"], m["length"] = "italic", v.Offset, v.Length
		case *tg.MessageEntityUnderline:
			m["type"], m["offset"], m["length"] = "underline", v.Offset, v.Length
		case *tg.MessageEntityStrike:
			m["type"], m["offset"], m["length"] = "strike", v.Offset, v.Length
		case *tg.MessageEntitySpoiler:
			m["type"], m["offset"], m["length"] = "spoiler", v.Offset, v.Length
		case *tg.MessageEntityCode:
			m["type"], m["offset"], m["length"] = "code", v.Offset, v.Length
		case *tg.MessageEntityPre:
			m["type"], m["offset"], m["length"], m["language"] = "pre", v.Offset, v.Length, v.Language
		case *tg.MessageEntityBlockquote:
			m["type"], m["offset"], m["length"], m["collapsed"] = "blockquote", v.Offset, v.Length, v.Collapsed
		case *tg.MessageEntityURL:
			m["type"], m["offset"], m["length"] = "url", v.Offset, v.Length
		case *tg.MessageEntityTextURL:
			m["type"], m["offset"], m["length"], m["url"] = "text_url", v.Offset, v.Length, v.URL
		case *tg.MessageEntityMention:
			m["type"], m["offset"], m["length"] = "mention", v.Offset, v.Length
		case *tg.MessageEntityMentionName:
			m["type"], m["offset"], m["length"], m["user_id"] = "mention_name", v.Offset, v.Length, v.UserID
		case *tg.MessageEntityHashtag:
			m["type"], m["offset"], m["length"] = "hashtag", v.Offset, v.Length
		case *tg.MessageEntityCashtag:
			m["type"], m["offset"], m["length"] = "cashtag", v.Offset, v.Length
		case *tg.MessageEntityBotCommand:
			m["type"], m["offset"], m["length"] = "bot_command", v.Offset, v.Length
		case *tg.MessageEntityPhone:
			m["type"], m["offset"], m["length"] = "phone", v.Offset, v.Length
		case *tg.MessageEntityEmail:
			m["type"], m["offset"], m["length"] = "email", v.Offset, v.Length
		case *tg.MessageEntityCustomEmoji:
			m["type"], m["offset"], m["length"], m["document_id"] = "custom_emoji", v.Offset, v.Length, v.DocumentID
		case *tg.MessageEntityBankCard:
			m["type"], m["offset"], m["length"] = "bank_card", v.Offset, v.Length
		default:
			m["type"] = fmt.Sprintf("unknown:%T", e)
		}
		out = append(out, m)
	}
	return out
}

// pickPhotoSize returns the largest non-progressive photo size + filename type.
func pickPhotoSize(sizes []tg.PhotoSizeClass) (sizeType string, sizeBytes int) {
	bestType, bestSize := "", 0
	for _, sz := range sizes {
		switch s := sz.(type) {
		case *tg.PhotoSize:
			if s.Size > bestSize {
				bestSize = s.Size
				bestType = s.Type
			}
		case *tg.PhotoSizeProgressive:
			total := 0
			for _, sb := range s.Sizes {
				if sb > total {
					total = sb
				}
			}
			if total > bestSize {
				bestSize = total
				bestType = s.Type
			}
		}
	}
	if bestType == "" {
		bestType = "y"
	}
	return bestType, bestSize
}

// describeAndDownloadMedia returns metadata for a message media and, optionally,
// downloads the media file to mediaDir, returning the relative path stored in metadata.
func describeAndDownloadMedia(
	ctx context.Context,
	api *tg.Client,
	d *downloader.Downloader,
	msgID int,
	media tg.MessageMediaClass,
	mediaDir string,
	skipDownload bool,
) (map[string]any, error) {
	out := map[string]any{}

	switch m := media.(type) {
	case *tg.MessageMediaPhoto:
		out["type"] = "photo"
		out["spoiler"] = m.Spoiler
		photo, ok := m.Photo.(*tg.Photo)
		if !ok {
			out["unavailable"] = true
			return out, nil
		}
		sizeType, sizeBytes := pickPhotoSize(photo.Sizes)
		out["size"] = sizeBytes
		out["photo_id"] = photo.ID
		if !skipDownload {
			outName := fmt.Sprintf("%d.jpg", msgID)
			outPath := filepath.Join(mediaDir, outName)
			loc := &tg.InputPhotoFileLocation{
				ID:            photo.ID,
				AccessHash:    photo.AccessHash,
				FileReference: photo.FileReference,
				ThumbSize:     sizeType,
			}
			if _, err := d.Download(api, loc).ToPath(ctx, outPath); err != nil {
				return out, fmt.Errorf("download photo msg %d: %w", msgID, err)
			}
			out["file"] = filepath.Join("media", outName)
		}
		return out, nil

	case *tg.MessageMediaDocument:
		out["spoiler"] = m.Spoiler
		doc, ok := m.Document.(*tg.Document)
		if !ok {
			out["unavailable"] = true
			return out, nil
		}
		out["mime"] = doc.MimeType
		out["size"] = doc.Size
		out["doc_id"] = doc.ID

		filename := ""
		isVideo := false
		isVoice := false
		for _, attr := range doc.Attributes {
			switch a := attr.(type) {
			case *tg.DocumentAttributeFilename:
				filename = a.FileName
			case *tg.DocumentAttributeVideo:
				isVideo = true
				out["w"] = a.W
				out["h"] = a.H
				out["duration"] = a.Duration
				if a.RoundMessage {
					out["round_message"] = true
				}
				if a.SupportsStreaming {
					out["supports_streaming"] = true
				}
			case *tg.DocumentAttributeAudio:
				if a.Voice {
					isVoice = true
					out["voice"] = true
				}
				out["duration"] = a.Duration
				if a.Title != "" {
					out["title"] = a.Title
				}
				if a.Performer != "" {
					out["performer"] = a.Performer
				}
			case *tg.DocumentAttributeImageSize:
				out["w"] = a.W
				out["h"] = a.H
			case *tg.DocumentAttributeAnimated:
				out["animated"] = true
			case *tg.DocumentAttributeSticker:
				out["sticker"] = true
				out["alt"] = a.Alt
			}
		}

		switch {
		case isVoice:
			out["type"] = "voice"
		case isVideo && doc.MimeType == "video/mp4" && hasAttr[*tg.DocumentAttributeAnimated](doc.Attributes):
			out["type"] = "gif"
		case isVideo:
			out["type"] = "video"
		case strings.HasPrefix(doc.MimeType, "image/"):
			out["type"] = "image"
		case strings.HasPrefix(doc.MimeType, "audio/"):
			out["type"] = "audio"
		case hasAttr[*tg.DocumentAttributeSticker](doc.Attributes):
			out["type"] = "sticker"
		default:
			out["type"] = "document"
		}

		if filename == "" {
			filename = fmt.Sprintf("%d%s", msgID, extFromMIME(doc.MimeType))
		}
		out["filename"] = filename

		if !skipDownload {
			outName := fmt.Sprintf("%d_%s", msgID, sanitizeFilename(filename))
			outPath := filepath.Join(mediaDir, outName)
			loc := &tg.InputDocumentFileLocation{
				ID:            doc.ID,
				AccessHash:    doc.AccessHash,
				FileReference: doc.FileReference,
			}
			if _, err := d.Download(api, loc).ToPath(ctx, outPath); err != nil {
				return out, fmt.Errorf("download doc msg %d: %w", msgID, err)
			}
			out["file"] = filepath.Join("media", outName)
		}
		return out, nil

	case *tg.MessageMediaWebPage:
		out["type"] = "webpage"
		if wp, ok := m.Webpage.(*tg.WebPage); ok {
			out["url"] = wp.URL
			out["title"] = wp.Title
			out["description"] = wp.Description
		}
		return out, nil

	case *tg.MessageMediaPoll:
		out["type"] = "poll"
		out["question"] = m.Poll.Question.Text
		return out, nil

	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		out["type"] = "geo"
		return out, nil

	case *tg.MessageMediaContact:
		out["type"] = "contact"
		out["phone"] = m.PhoneNumber
		out["first_name"] = m.FirstName
		out["last_name"] = m.LastName
		return out, nil

	case *tg.MessageMediaDice:
		out["type"] = "dice"
		out["emoticon"] = m.Emoticon
		out["value"] = m.Value
		return out, nil

	case *tg.MessageMediaStory:
		out["type"] = "story"
		out["story_id"] = m.ID
		return out, nil

	case *tg.MessageMediaEmpty, nil:
		return nil, nil
	default:
		out["type"] = fmt.Sprintf("unknown:%T", m)
		return out, nil
	}
}

func hasAttr[T tg.DocumentAttributeClass](attrs []tg.DocumentAttributeClass) bool {
	for _, a := range attrs {
		if _, ok := a.(T); ok {
			return true
		}
	}
	return false
}

func sanitizeFilename(s string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_", " ", "_")
	s = r.Replace(s)
	if len(s) > 80 {
		ext := filepath.Ext(s)
		if len(ext) > 8 {
			ext = ""
		}
		s = s[:80-len(ext)] + ext
	}
	return s
}

func cmdDownloadChannel(c config, name, outDir string, limit int, skipMedia bool, resume bool, batchSize int) error {
	if outDir == "" {
		return fmt.Errorf("--out is required")
	}
	if batchSize <= 0 || batchSize > 100 {
		batchSize = 100
	}

	return withTelegram(c, func(ctx context.Context, client *telegram.Client, api *tg.Client, pm *peers.Manager) error {
		p, err := resolvePeer(ctx, pm, api, name)
		if err != nil {
			return err
		}

		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", outDir, err)
		}
		mediaDir := filepath.Join(outDir, "media")
		if !skipMedia {
			if err := os.MkdirAll(mediaDir, 0o755); err != nil {
				return fmt.Errorf("mkdir media: %w", err)
			}
		}

		// meta.json
		meta := map[string]any{}
		switch peer := p.(type) {
		case peers.Channel:
			ch := peer.Raw()
			meta["type"] = "channel"
			if ch.Megagroup {
				meta["type"] = "supergroup"
			}
			meta["id"] = ch.ID
			meta["title"] = ch.Title
			meta["username"] = ch.Username
			meta["members"] = ch.ParticipantsCount
			full, ferr := api.ChannelsGetFullChannel(ctx, peer.InputChannel())
			if ferr == nil {
				if fc, ok := full.FullChat.(*tg.ChannelFull); ok {
					meta["description"] = fc.About
					meta["members"] = fc.ParticipantsCount
					meta["pinned_msg_id"] = fc.PinnedMsgID
					meta["read_inbox_max_id"] = fc.ReadInboxMaxID
				}
			}
		case peers.User:
			u := peer.Raw()
			meta["type"] = "user"
			meta["id"] = u.ID
			meta["username"] = u.Username
			meta["first_name"] = u.FirstName
			meta["last_name"] = u.LastName
		case peers.Chat:
			ch := peer.Raw()
			meta["type"] = "group"
			meta["id"] = ch.ID
			meta["title"] = ch.Title
		}
		meta["dumped_at"] = time.Now().UTC().Format(time.RFC3339)
		meta["dumped_by"] = c.account
		if err := writeJSON(filepath.Join(outDir, "meta.json"), meta); err != nil {
			return err
		}

		// resume: load existing messages.json and skip already-saved ids
		messagesPath := filepath.Join(outDir, "messages.json")
		seen := make(map[int]bool)
		var existing []dumpedMsg
		if resume {
			if data, err := os.ReadFile(messagesPath); err == nil {
				_ = json.Unmarshal(data, &existing)
				for _, m := range existing {
					seen[m.ID] = true
				}
				fmt.Fprintf(os.Stderr, "Resume: %d messages already dumped\n", len(existing))
			}
		}

		d := downloader.NewDownloader().WithPartSize(512 * 1024)
		dumped := existing
		offsetID := 0
		fetched := 0
		downloadedMedia := 0

		for {
			req := &tg.MessagesGetHistoryRequest{
				Peer:     p.InputPeer(),
				OffsetID: offsetID,
				Limit:    batchSize,
			}
			result, err := api.MessagesGetHistory(ctx, req)
			if err != nil {
				return fmt.Errorf("get history (offset %d): %w", offsetID, err)
			}
			msgs, _, _, err := extractHistoryMessages(result)
			if err != nil {
				return err
			}
			if len(msgs) == 0 {
				break
			}

			// process oldest first within batch — actually GetHistory returns newest first.
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
					ID:         msg.ID,
					UnixDate:   msg.Date,
					Date:       time.Unix(int64(msg.Date), 0).UTC().Format(time.RFC3339),
					Text:       msg.Message,
					Entities:   serializeEntities(msg.Entities),
					ReplyTo:    msgReplyTo(msg),
					Reactions:  msgReactions(msg),
					Pinned:     msg.Pinned,
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
					fm := map[string]any{
						"date": time.Unix(int64(fwd.Date), 0).UTC().Format(time.RFC3339),
					}
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
						fmt.Fprintf(os.Stderr, "media error msg %d: %v\n", msg.ID, mErr)
						if mediaInfo == nil {
							mediaInfo = map[string]any{}
						}
						mediaInfo["error"] = mErr.Error()
					}
					if mediaInfo != nil {
						dm.Media = mediaInfo
						if _, ok := mediaInfo["file"]; ok {
							downloadedMedia++
						}
					}
				}

				dumped = append(dumped, dm)
				fetched++

				if limit > 0 && fetched >= limit {
					break
				}
			}

			// next page: oldest message id in batch becomes new offset
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

			fmt.Fprintf(os.Stderr, "fetched=%d media=%d offset=%d\n", fetched, downloadedMedia, offsetID)

			// flush messages.json periodically
			if err := writeJSONAtomic(messagesPath, dumped); err != nil {
				return err
			}

			if limit > 0 && fetched >= limit {
				break
			}

			// be polite
			time.Sleep(300 * time.Millisecond)
		}

		// final flush
		if err := writeJSONAtomic(messagesPath, dumped); err != nil {
			return err
		}

		return printJSON(map[string]any{
			"status":           "done",
			"channel":          name,
			"dumped_messages":  len(dumped),
			"new_this_run":     fetched,
			"downloaded_media": downloadedMedia,
			"out_dir":          outDir,
		})
	})
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func writeJSONAtomic(path string, v any) error {
	tmp := path + ".tmp"
	if err := writeJSON(tmp, v); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
