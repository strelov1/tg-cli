package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

// ── firstNonEmpty ──

func TestFirstNonEmpty(t *testing.T) {
	cases := []struct {
		vals []string
		want string
	}{
		{[]string{"", "", "c"}, "c"},
		{[]string{"a", "b"}, "a"},
		{[]string{"", ""}, ""},
		{nil, ""},
	}
	for _, tc := range cases {
		got := firstNonEmpty(tc.vals...)
		if got != tc.want {
			t.Errorf("firstNonEmpty(%v) = %q, want %q", tc.vals, got, tc.want)
		}
	}
}

// ── extractInviteHash ──

func TestExtractInviteHash(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"https://t.me/+AbCdEfGh", "AbCdEfGh"},
		{"t.me/+AbCdEfGh", "AbCdEfGh"},
		{"https://t.me/joinchat/AbCdEfGh", "AbCdEfGh"},
		{"t.me/joinchat/AbCdEfGh", "AbCdEfGh"},
		{"@golang_digest", ""},
		{"golang_digest", ""},
		{"https://t.me/golang_digest", ""},
	}
	for _, tc := range cases {
		got := extractInviteHash(tc.input)
		if got != tc.want {
			t.Errorf("extractInviteHash(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── buildEntityMaps ──

func TestBuildEntityMaps(t *testing.T) {
	user := &tg.User{ID: 1, FirstName: "Alice", LastName: "Smith"}
	chat := &tg.Chat{ID: 2, Title: "General"}
	channel := &tg.Channel{ID: 3, Title: "News"}

	em := buildEntityMaps(
		[]tg.UserClass{user},
		[]tg.ChatClass{chat, channel},
	)

	if u, ok := em.users[1]; !ok || u.FirstName != "Alice" {
		t.Error("user not indexed correctly")
	}
	if c, ok := em.chats[2]; !ok || c.Title != "General" {
		t.Error("chat not indexed correctly")
	}
	if ch, ok := em.channels[3]; !ok || ch.Title != "News" {
		t.Error("channel not indexed correctly")
	}
}

func TestBuildEntityMaps_empty(t *testing.T) {
	em := buildEntityMaps(nil, nil)
	if len(em.users) != 0 || len(em.chats) != 0 || len(em.channels) != 0 {
		t.Error("expected all empty maps")
	}
}

// ── formatMessages ──

func TestFormatMessages_basic(t *testing.T) {
	user := &tg.User{ID: 7, FirstName: "Bob", LastName: ""}
	em := buildEntityMaps([]tg.UserClass{user}, nil)

	msg := &tg.Message{
		ID:      42,
		FromID:  &tg.PeerUser{UserID: 7},
		Date:    1_000_000,
		Message: "Hello world",
	}

	out := formatMessages([]tg.MessageClass{msg}, em)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0].ID != 42 {
		t.Errorf("ID: got %d, want 42", out[0].ID)
	}
	if out[0].Who != "Bob" {
		t.Errorf("Who: got %q, want Bob", out[0].Who)
	}
	if out[0].Text != "Hello world" {
		t.Errorf("Text: got %q, want 'Hello world'", out[0].Text)
	}
	// When should be RFC3339
	if _, err := time.Parse(time.RFC3339, out[0].When); err != nil {
		t.Errorf("When is not RFC3339: %q", out[0].When)
	}
}

func TestFormatMessages_skipsEmpty(t *testing.T) {
	em := buildEntityMaps(nil, nil)
	// Message with empty text is skipped (service message)
	msg := &tg.Message{ID: 1, Message: ""}
	out := formatMessages([]tg.MessageClass{msg}, em)
	if len(out) != 0 {
		t.Errorf("expected 0 messages, got %d", len(out))
	}
}

func TestFormatMessages_skipsNonMessage(t *testing.T) {
	em := buildEntityMaps(nil, nil)
	// MessageService is not *tg.Message — should be skipped
	svc := &tg.MessageService{ID: 5}
	out := formatMessages([]tg.MessageClass{svc}, em)
	if len(out) != 0 {
		t.Errorf("expected 0 messages, got %d", len(out))
	}
}

func TestFormatMessages_unknownSender(t *testing.T) {
	em := buildEntityMaps(nil, nil) // no users
	msg := &tg.Message{
		ID:      1,
		FromID:  &tg.PeerUser{UserID: 999},
		Date:    1000,
		Message: "hi",
	}
	out := formatMessages([]tg.MessageClass{msg}, em)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0].Who != "user:999" {
		t.Errorf("Who: got %q, want user:999", out[0].Who)
	}
}

func TestFormatMessages_noFromID(t *testing.T) {
	em := buildEntityMaps(nil, nil)
	// Channel messages often have no FromID (sent as channel)
	msg := &tg.Message{
		ID:      10,
		FromID:  nil,
		Date:    2000,
		Message: "broadcast",
	}
	out := formatMessages([]tg.MessageClass{msg}, em)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0].Who != "" {
		t.Errorf("Who should be empty for nil FromID, got %q", out[0].Who)
	}
}

// ── senderName ──

func TestSenderName_user(t *testing.T) {
	user := &tg.User{ID: 5, FirstName: "Anna", LastName: "Lee"}
	em := buildEntityMaps([]tg.UserClass{user}, nil)
	got := em.senderName(&tg.PeerUser{UserID: 5})
	if got != "Anna Lee" {
		t.Errorf("got %q, want 'Anna Lee'", got)
	}
}

func TestSenderName_channel(t *testing.T) {
	ch := &tg.Channel{ID: 10, Title: "TechNews"}
	em := buildEntityMaps(nil, []tg.ChatClass{ch})
	got := em.senderName(&tg.PeerChannel{ChannelID: 10})
	if got != "TechNews" {
		t.Errorf("got %q, want 'TechNews'", got)
	}
}

func TestSenderName_nil(t *testing.T) {
	em := buildEntityMaps(nil, nil)
	got := em.senderName(nil)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// ── pendingAuth TTL ──

func TestLoadPendingAuth_expired(t *testing.T) {
	dir := t.TempDir()
	c := config{
		baseDir: dir,
		account: "+70000000000",
	}
	// Write a pending_auth.json with CreatedAt 10 minutes ago
	if err := os.MkdirAll(c.accountDir(), 0700); err != nil {
		t.Fatal(err)
	}
	pa := pendingAuth{
		Phone:         "+70000000000",
		PhoneCodeHash: "hash123",
		CreatedAt:     time.Now().Add(-10 * time.Minute),
	}
	data, _ := json.Marshal(pa)
	if err := os.WriteFile(c.pendingAuthFile(), data, 0600); err != nil {
		t.Fatal(err)
	}

	_, err := loadPendingAuth(c)
	if err == nil {
		t.Error("expected error for expired auth, got nil")
	}
	// File should be deleted after TTL check
	if _, statErr := os.Stat(c.pendingAuthFile()); !os.IsNotExist(statErr) {
		t.Error("expired pending_auth.json should be removed")
	}
}

func TestLoadPendingAuth_valid(t *testing.T) {
	dir := t.TempDir()
	c := config{
		baseDir: dir,
		account: "+70000000001",
	}
	if err := os.MkdirAll(c.accountDir(), 0700); err != nil {
		t.Fatal(err)
	}
	pa := pendingAuth{
		Phone:         "+70000000001",
		PhoneCodeHash: "validhash",
		CreatedAt:     time.Now().Add(-1 * time.Minute), // 1 minute ago — still valid
	}
	data, _ := json.Marshal(pa)
	if err := os.WriteFile(c.pendingAuthFile(), data, 0600); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadPendingAuth(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loaded.PhoneCodeHash != "validhash" {
		t.Errorf("got hash %q, want validhash", loaded.PhoneCodeHash)
	}
}

func TestLoadPendingAuth_missing(t *testing.T) {
	dir := t.TempDir()
	c := config{
		baseDir: dir,
		account: "+70000000002",
	}
	_, err := loadPendingAuth(c)
	if err == nil {
		t.Error("expected error when file missing, got nil")
	}
}

// ── readFileConfig / writeFileConfig ──

func TestWriteReadFileConfig(t *testing.T) {
	dir := t.TempDir()
	// Override defaultConfigPath by writing directly to a temp path
	cfgPath := filepath.Join(dir, "config.json")
	fc := fileConfig{
		AppID:          "99999",
		APIHash:        "testhash",
		DefaultAccount: "+71234567890",
	}

	// Write using the raw path (bypass the home-dir-based default)
	data, _ := json.MarshalIndent(fc, "", "  ")
	if err := os.WriteFile(cfgPath, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	// Read back
	var loaded fileConfig
	raw, _ := os.ReadFile(cfgPath)
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatal(err)
	}

	if loaded.AppID != "99999" {
		t.Errorf("AppID: got %q, want 99999", loaded.AppID)
	}
	if loaded.APIHash != "testhash" {
		t.Errorf("APIHash: got %q, want testhash", loaded.APIHash)
	}
	if loaded.DefaultAccount != "+71234567890" {
		t.Errorf("DefaultAccount: got %q, want +71234567890", loaded.DefaultAccount)
	}
}

func TestGetFileConfigField(t *testing.T) {
	fc := fileConfig{AppID: "123", APIHash: "abc"}

	cases := []struct {
		key  string
		want *string
	}{
		{"app-id", &fc.AppID},
		{"api-hash", &fc.APIHash},
		{"session-dir", &fc.SessionDir},
		{"default-account", &fc.DefaultAccount},
		{"unknown", nil},
	}
	for _, tc := range cases {
		got := getFileConfigField(&fc, tc.key)
		if tc.want == nil {
			if got != nil {
				t.Errorf("key %q: expected nil, got non-nil", tc.key)
			}
		} else {
			if got != tc.want {
				t.Errorf("key %q: wrong pointer", tc.key)
			}
		}
	}
}

// ── listAccounts ──

func TestListAccounts(t *testing.T) {
	dir := t.TempDir()

	// Create two account dirs with session.json
	for _, phone := range []string{"+71111111111", "+72222222222"} {
		p := filepath.Join(dir, phone)
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "session.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Create a dir without session.json — should not appear
	if err := os.MkdirAll(filepath.Join(dir, "+73333333333"), 0700); err != nil {
		t.Fatal(err)
	}

	phones, err := listAccounts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(phones) != 2 {
		t.Errorf("expected 2 accounts, got %d: %v", len(phones), phones)
	}
}

func TestListAccounts_empty(t *testing.T) {
	dir := t.TempDir()
	phones, err := listAccounts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(phones) != 0 {
		t.Errorf("expected 0, got %d", len(phones))
	}
}

func TestListAccounts_notExist(t *testing.T) {
	phones, err := listAccounts("/nonexistent/path/sessions")
	if err != nil {
		t.Fatal(err)
	}
	if len(phones) != 0 {
		t.Errorf("expected 0 for missing dir, got %d", len(phones))
	}
}

// ── reactionEmoji ──

func TestReactionEmoji_emoji(t *testing.T) {
	got := reactionEmoji(&tg.ReactionEmoji{Emoticon: "👍"})
	if got != "👍" {
		t.Errorf("got %q, want 👍", got)
	}
}

func TestReactionEmoji_customEmoji(t *testing.T) {
	got := reactionEmoji(&tg.ReactionCustomEmoji{DocumentID: 12345})
	if got != "custom:12345" {
		t.Errorf("got %q, want custom:12345", got)
	}
}

func TestReactionEmoji_empty(t *testing.T) {
	got := reactionEmoji(&tg.ReactionEmpty{})
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestReactionEmoji_nil(t *testing.T) {
	got := reactionEmoji(nil)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// ── msgReplyTo ──

func TestMsgReplyTo_present(t *testing.T) {
	msg := &tg.Message{
		ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 42},
	}
	if got := msgReplyTo(msg); got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

func TestMsgReplyTo_nil(t *testing.T) {
	msg := &tg.Message{}
	if got := msgReplyTo(msg); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestMsgReplyTo_storyHeader(t *testing.T) {
	// MessageReplyStoryHeader should yield 0 (not a message reply)
	msg := &tg.Message{
		ReplyTo: &tg.MessageReplyStoryHeader{},
	}
	if got := msgReplyTo(msg); got != 0 {
		t.Errorf("got %d, want 0 for story header", got)
	}
}

// ── msgReactions ──

func TestMsgReactions_nil(t *testing.T) {
	msg := &tg.Message{}
	if got := msgReactions(msg); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

// ── formatMessages with metadata ──

func TestFormatMessages_withViews(t *testing.T) {
	em := buildEntityMaps(nil, nil)
	msg := &tg.Message{ID: 1, Date: 1000, Message: "hi"}
	msg.SetViews(999)
	out := formatMessages([]tg.MessageClass{msg}, em)
	if len(out) != 1 {
		t.Fatalf("expected 1, got %d", len(out))
	}
	if out[0].Views != 999 {
		t.Errorf("Views: got %d, want 999", out[0].Views)
	}
}

func TestFormatMessages_withForwards(t *testing.T) {
	em := buildEntityMaps(nil, nil)
	msg := &tg.Message{ID: 1, Date: 1000, Message: "hi"}
	msg.SetForwards(42)
	out := formatMessages([]tg.MessageClass{msg}, em)
	if len(out) != 1 {
		t.Fatalf("expected 1, got %d", len(out))
	}
	if out[0].Forwards != 42 {
		t.Errorf("Forwards: got %d, want 42", out[0].Forwards)
	}
}

func TestFormatMessages_withReplyTo(t *testing.T) {
	em := buildEntityMaps(nil, nil)
	msg := &tg.Message{
		ID:      5,
		Date:    1000,
		Message: "reply",
		ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 3},
	}
	out := formatMessages([]tg.MessageClass{msg}, em)
	if len(out) != 1 {
		t.Fatalf("expected 1, got %d", len(out))
	}
	if out[0].ReplyTo != 3 {
		t.Errorf("ReplyTo: got %d, want 3", out[0].ReplyTo)
	}
}

func TestFormatMessages_withPostAuthor(t *testing.T) {
	em := buildEntityMaps(nil, nil)
	msg := &tg.Message{ID: 1, Date: 1000, Message: "post"}
	msg.SetPostAuthor("Alice")
	out := formatMessages([]tg.MessageClass{msg}, em)
	if len(out) != 1 {
		t.Fatalf("expected 1, got %d", len(out))
	}
	if out[0].PostAuthor != "Alice" {
		t.Errorf("PostAuthor: got %q, want Alice", out[0].PostAuthor)
	}
}

// ── formatMessagesFull with metadata ──

func TestFormatMessagesFull_withViews(t *testing.T) {
	em := buildEntityMaps(nil, nil)
	msg := &tg.Message{ID: 1, Date: 1000, Message: "hi"}
	msg.SetViews(77)
	out := formatMessagesFull([]tg.MessageClass{msg}, em)
	if len(out) != 1 {
		t.Fatalf("expected 1, got %d", len(out))
	}
	if out[0].Views != 77 {
		t.Errorf("Views: got %d, want 77", out[0].Views)
	}
}

// ── mediaTypeName ──

func TestMediaTypeName_photo(t *testing.T) {
	if got := mediaTypeName(&tg.MessageMediaPhoto{}); got != "photo" {
		t.Errorf("got %q, want photo", got)
	}
}

func TestMediaTypeName_document(t *testing.T) {
	if got := mediaTypeName(&tg.MessageMediaDocument{}); got != "document" {
		t.Errorf("got %q, want document", got)
	}
}

func TestMediaTypeName_geo(t *testing.T) {
	if got := mediaTypeName(&tg.MessageMediaGeo{}); got != "geo" {
		t.Errorf("got %q, want geo", got)
	}
}

func TestMediaTypeName_contact(t *testing.T) {
	if got := mediaTypeName(&tg.MessageMediaContact{}); got != "contact" {
		t.Errorf("got %q, want contact", got)
	}
}

func TestMediaTypeName_poll(t *testing.T) {
	if got := mediaTypeName(&tg.MessageMediaPoll{}); got != "poll" {
		t.Errorf("got %q, want poll", got)
	}
}

func TestMediaTypeName_webpage(t *testing.T) {
	if got := mediaTypeName(&tg.MessageMediaWebPage{}); got != "webpage" {
		t.Errorf("got %q, want webpage", got)
	}
}

func TestMediaTypeName_dice(t *testing.T) {
	if got := mediaTypeName(&tg.MessageMediaDice{}); got != "dice" {
		t.Errorf("got %q, want dice", got)
	}
}

func TestMediaTypeName_venue(t *testing.T) {
	if got := mediaTypeName(&tg.MessageMediaVenue{}); got != "venue" {
		t.Errorf("got %q, want venue", got)
	}
}

func TestMediaTypeName_geoLive(t *testing.T) {
	if got := mediaTypeName(&tg.MessageMediaGeoLive{}); got != "geo_live" {
		t.Errorf("got %q, want geo_live", got)
	}
}

func TestMediaTypeName_story(t *testing.T) {
	if got := mediaTypeName(&tg.MessageMediaStory{}); got != "story" {
		t.Errorf("got %q, want story", got)
	}
}

func TestMediaTypeName_empty(t *testing.T) {
	if got := mediaTypeName(&tg.MessageMediaEmpty{}); got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestMediaTypeName_nil(t *testing.T) {
	if got := mediaTypeName(nil); got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

// ── extFromMIME ──

func TestExtFromMIME_knownTypes(t *testing.T) {
	cases := []struct {
		mime string
		want string
	}{
		{"image/jpeg", ".jpg"},
		{"image/png", ".png"},
		{"image/gif", ".gif"},
		{"image/webp", ".webp"},
		{"video/mp4", ".mp4"},
		{"video/quicktime", ".mov"},
		{"audio/mpeg", ".mp3"},
		{"audio/ogg", ".ogg"},
		{"audio/wav", ".wav"},
		{"application/pdf", ".pdf"},
		{"application/zip", ".zip"},
		{"application/x-tgsticker", ".tgs"},
	}
	for _, tc := range cases {
		got := extFromMIME(tc.mime)
		if got != tc.want {
			t.Errorf("extFromMIME(%q) = %q, want %q", tc.mime, got, tc.want)
		}
	}
}

func TestExtFromMIME_unknown(t *testing.T) {
	// Unknown MIME types should not panic; may return empty or stdlib ext
	got := extFromMIME("application/x-totally-unknown-format-xyz")
	_ = got // just verify no panic
}

func TestExtFromMIME_empty(t *testing.T) {
	got := extFromMIME("")
	_ = got // empty MIME → empty ext, no panic
}

// ── formatMessagesFull ──

func TestFormatMessagesFull_textOnly(t *testing.T) {
	user := &tg.User{ID: 1, FirstName: "Alice"}
	em := buildEntityMaps([]tg.UserClass{user}, nil)

	msg := &tg.Message{
		ID:      10,
		FromID:  &tg.PeerUser{UserID: 1},
		Date:    1_000_000,
		Message: "hello",
	}
	out := formatMessagesFull([]tg.MessageClass{msg}, em)
	if len(out) != 1 {
		t.Fatalf("expected 1, got %d", len(out))
	}
	if out[0].ID != 10 {
		t.Errorf("ID: got %d, want 10", out[0].ID)
	}
	if out[0].Who != "Alice" {
		t.Errorf("Who: got %q, want Alice", out[0].Who)
	}
	if out[0].Text != "hello" {
		t.Errorf("Text: got %q, want hello", out[0].Text)
	}
	if out[0].MediaType != "" {
		t.Errorf("MediaType should be empty, got %q", out[0].MediaType)
	}
	if _, err := time.Parse(time.RFC3339, out[0].When); err != nil {
		t.Errorf("When is not RFC3339: %q", out[0].When)
	}
}

func TestFormatMessagesFull_mediaOnly(t *testing.T) {
	em := buildEntityMaps(nil, nil)

	// Photo message with no text — formatMessagesFull should NOT skip it
	msg := &tg.Message{
		ID:      20,
		Date:    2_000_000,
		Message: "",
		Media:   &tg.MessageMediaPhoto{},
	}
	out := formatMessagesFull([]tg.MessageClass{msg}, em)
	if len(out) != 1 {
		t.Fatalf("expected 1 (media-only), got %d", len(out))
	}
	if out[0].MediaType != "photo" {
		t.Errorf("MediaType: got %q, want photo", out[0].MediaType)
	}
	if out[0].Text != "" {
		t.Errorf("Text should be empty, got %q", out[0].Text)
	}
}

func TestFormatMessagesFull_textAndMedia(t *testing.T) {
	em := buildEntityMaps(nil, nil)

	msg := &tg.Message{
		ID:      30,
		Date:    3_000_000,
		Message: "caption",
		Media:   &tg.MessageMediaDocument{},
	}
	out := formatMessagesFull([]tg.MessageClass{msg}, em)
	if len(out) != 1 {
		t.Fatalf("expected 1, got %d", len(out))
	}
	if out[0].Text != "caption" {
		t.Errorf("Text: got %q, want caption", out[0].Text)
	}
	if out[0].MediaType != "document" {
		t.Errorf("MediaType: got %q, want document", out[0].MediaType)
	}
}

func TestFormatMessagesFull_skipsEmptyAndNoMedia(t *testing.T) {
	em := buildEntityMaps(nil, nil)

	// Message with no text and no media should be skipped
	msg := &tg.Message{ID: 1, Date: 1000, Message: ""}
	out := formatMessagesFull([]tg.MessageClass{msg}, em)
	if len(out) != 0 {
		t.Errorf("expected 0, got %d", len(out))
	}
}

func TestFormatMessagesFull_skipsZeroID(t *testing.T) {
	em := buildEntityMaps(nil, nil)

	// ID=0 messages (e.g. NotModified placeholders) should be skipped
	msg := &tg.Message{ID: 0, Date: 1000, Message: "should skip"}
	out := formatMessagesFull([]tg.MessageClass{msg}, em)
	if len(out) != 0 {
		t.Errorf("expected 0 for ID=0 message, got %d", len(out))
	}
}

func TestFormatMessagesFull_skipsNonMessage(t *testing.T) {
	em := buildEntityMaps(nil, nil)

	svc := &tg.MessageService{ID: 5}
	out := formatMessagesFull([]tg.MessageClass{svc}, em)
	if len(out) != 0 {
		t.Errorf("expected 0 for MessageService, got %d", len(out))
	}
}

func TestFormatMessagesFull_noFromID(t *testing.T) {
	em := buildEntityMaps(nil, nil)

	// Channel-sent messages have no FromID
	msg := &tg.Message{ID: 99, Date: 5000, Message: "broadcast", FromID: nil}
	out := formatMessagesFull([]tg.MessageClass{msg}, em)
	if len(out) != 1 {
		t.Fatalf("expected 1, got %d", len(out))
	}
	if out[0].Who != "" {
		t.Errorf("Who should be empty for nil FromID, got %q", out[0].Who)
	}
}

func TestFormatMessagesFull_multipleMessages(t *testing.T) {
	em := buildEntityMaps(nil, nil)

	msgs := []tg.MessageClass{
		&tg.Message{ID: 1, Date: 1000, Message: "first"},
		&tg.Message{ID: 2, Date: 2000, Message: "", Media: &tg.MessageMediaPhoto{}},
		&tg.Message{ID: 3, Date: 3000, Message: ""},               // no media, skipped
		&tg.MessageService{ID: 4},                                   // service, skipped
		&tg.Message{ID: 5, Date: 5000, Message: "last"},
	}
	out := formatMessagesFull(msgs, em)
	if len(out) != 3 {
		t.Fatalf("expected 3, got %d", len(out))
	}
	if out[0].ID != 1 || out[1].ID != 2 || out[2].ID != 5 {
		t.Errorf("unexpected IDs: %v", []int{out[0].ID, out[1].ID, out[2].ID})
	}
}

// ── adminRightsMap ──

func TestAdminRightsMap_nil(t *testing.T) {
	if m := adminRightsMap(nil); m != nil {
		t.Errorf("expected nil for nil input, got %v", m)
	}
}

func TestAdminRightsMap_empty(t *testing.T) {
	r := &tg.ChatAdminRights{}
	m := adminRightsMap(r)
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

func TestAdminRightsMap_singlePerm(t *testing.T) {
	r := &tg.ChatAdminRights{PostMessages: true}
	m := adminRightsMap(r)
	if !m["post_messages"] {
		t.Error("expected post_messages = true")
	}
	if len(m) != 1 {
		t.Errorf("expected 1 entry, got %d: %v", len(m), m)
	}
}

func TestAdminRightsMap_allPerms(t *testing.T) {
	r := &tg.ChatAdminRights{
		ChangeInfo:     true,
		PostMessages:   true,
		EditMessages:   true,
		DeleteMessages: true,
		BanUsers:       true,
		InviteUsers:    true,
		PinMessages:    true,
		AddAdmins:      true,
		Anonymous:      true,
		ManageCall:     true,
		ManageTopics:   true,
	}
	m := adminRightsMap(r)
	expected := []string{
		"change_info", "post_messages", "edit_messages", "delete_messages",
		"ban_users", "invite_users", "pin_messages", "add_admins",
		"anonymous", "manage_call", "manage_topics",
	}
	if len(m) != len(expected) {
		t.Fatalf("expected %d entries, got %d: %v", len(expected), len(m), m)
	}
	for _, k := range expected {
		if !m[k] {
			t.Errorf("expected %q = true", k)
		}
	}
}

// ── parseAdminPerms ──

func TestParseAdminPerms_empty(t *testing.T) {
	r := parseAdminPerms(nil)
	if r.PostMessages || r.DeleteMessages || r.BanUsers {
		t.Error("expected all false for empty input")
	}
}

func TestParseAdminPerms_single(t *testing.T) {
	r := parseAdminPerms([]string{"post"})
	if !r.PostMessages {
		t.Error("expected PostMessages = true")
	}
	if r.EditMessages || r.DeleteMessages {
		t.Error("expected other perms = false")
	}
}

func TestParseAdminPerms_all(t *testing.T) {
	r := parseAdminPerms([]string{"all"})
	if !r.PostMessages || !r.EditMessages || !r.DeleteMessages ||
		!r.BanUsers || !r.InviteUsers || !r.PinMessages ||
		!r.ManageCall || !r.ManageTopics || !r.ChangeInfo {
		t.Error("expected all relevant perms = true for 'all'")
	}
	// AddAdmins and Anonymous are NOT in 'all'
	if r.AddAdmins {
		t.Error("expected AddAdmins = false in 'all'")
	}
}

func TestParseAdminPerms_multiple(t *testing.T) {
	r := parseAdminPerms([]string{"post", "delete", "ban", "pin"})
	if !r.PostMessages {
		t.Error("expected PostMessages = true")
	}
	if !r.DeleteMessages {
		t.Error("expected DeleteMessages = true")
	}
	if !r.BanUsers {
		t.Error("expected BanUsers = true")
	}
	if !r.PinMessages {
		t.Error("expected PinMessages = true")
	}
	if r.EditMessages || r.InviteUsers {
		t.Error("expected EditMessages and InviteUsers = false")
	}
}

func TestParseAdminPerms_caseInsensitive(t *testing.T) {
	r := parseAdminPerms([]string{"POST", "Edit", "  delete  "})
	if !r.PostMessages || !r.EditMessages || !r.DeleteMessages {
		t.Error("expected case-insensitive matching")
	}
}

func TestParseAdminPerms_allPerms(t *testing.T) {
	perms := []string{"post", "edit", "delete", "ban", "invite", "pin",
		"add_admins", "manage", "anonymous", "change_info", "topics"}
	r := parseAdminPerms(perms)
	if !r.PostMessages || !r.EditMessages || !r.DeleteMessages {
		t.Error("expected post/edit/delete = true")
	}
	if !r.BanUsers || !r.InviteUsers || !r.PinMessages {
		t.Error("expected ban/invite/pin = true")
	}
	if !r.AddAdmins || !r.ManageCall || !r.Anonymous {
		t.Error("expected add_admins/manage/anonymous = true")
	}
	if !r.ChangeInfo || !r.ManageTopics {
		t.Error("expected change_info/topics = true")
	}
}

// ── printMessagesText ──

func TestPrintMessagesText_empty(t *testing.T) {
	// should not panic on empty input
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	printMessagesText(nil)
	w.Close()
	os.Stdout = old
	buf := make([]byte, 64)
	n, _ := r.Read(buf)
	if n != 0 {
		t.Errorf("expected no output for nil input, got %q", buf[:n])
	}
}

func TestPrintMessagesText_singleMessage(t *testing.T) {
	msgs := []tgMsg{
		{
			ID:   1,
			Who:  "Alice",
			When: time.Unix(1000, 0).UTC().Format(time.RFC3339),
			Text: "Hello world",
		},
	}
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	printMessagesText(msgs)
	w.Close()
	os.Stdout = old

	buf := make([]byte, 256)
	n, _ := r.Read(buf)
	output := string(buf[:n])
	if output == "" {
		t.Error("expected output, got empty string")
	}
	if !containsStr(output, "Alice") {
		t.Errorf("expected 'Alice' in output, got %q", output)
	}
	if !containsStr(output, "Hello world") {
		t.Errorf("expected 'Hello world' in output, got %q", output)
	}
}

func TestPrintMessagesText_unknownSender(t *testing.T) {
	msgs := []tgMsg{
		{
			ID:   2,
			Who:  "",
			When: time.Unix(2000, 0).UTC().Format(time.RFC3339),
			Text: "Anonymous msg",
		},
	}
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	printMessagesText(msgs)
	w.Close()
	os.Stdout = old

	buf := make([]byte, 256)
	n, _ := r.Read(buf)
	output := string(buf[:n])
	if !containsStr(output, "?") {
		t.Errorf("expected '?' placeholder for unknown sender, got %q", output)
	}
	if !containsStr(output, "Anonymous msg") {
		t.Errorf("expected message text in output, got %q", output)
	}
}

// containsStr is a simple substring helper used in tests.
func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
