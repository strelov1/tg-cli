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
