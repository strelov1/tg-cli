package main

import (
	"testing"
	"time"
)

// ── flagStr ──

func TestFlagStr_present(t *testing.T) {
	val, rest := flagStr([]string{"--code", "12345", "--foo", "bar"}, "--code")
	if val != "12345" {
		t.Errorf("expected 12345, got %q", val)
	}
	if len(rest) != 2 || rest[0] != "--foo" || rest[1] != "bar" {
		t.Errorf("unexpected rest: %v", rest)
	}
}

func TestFlagStr_absent(t *testing.T) {
	args := []string{"--foo", "bar"}
	val, rest := flagStr(args, "--code")
	if val != "" {
		t.Errorf("expected empty, got %q", val)
	}
	if len(rest) != len(args) {
		t.Errorf("rest should be unchanged: %v", rest)
	}
}

func TestFlagStr_atEnd(t *testing.T) {
	// flag without value at end of args — ignored
	val, rest := flagStr([]string{"--code"}, "--code")
	if val != "" {
		t.Errorf("expected empty, got %q", val)
	}
	if len(rest) != 1 {
		t.Errorf("unexpected rest: %v", rest)
	}
}

// ── flagBool ──

func TestFlagBool_present(t *testing.T) {
	ok, rest := flagBool([]string{"--unread", "--limit", "5"}, "--unread")
	if !ok {
		t.Error("expected true")
	}
	if len(rest) != 2 {
		t.Errorf("unexpected rest: %v", rest)
	}
}

func TestFlagBool_absent(t *testing.T) {
	args := []string{"--limit", "5"}
	ok, rest := flagBool(args, "--unread")
	if ok {
		t.Error("expected false")
	}
	if len(rest) != 2 {
		t.Errorf("rest should be unchanged: %v", rest)
	}
}

// ── flagInt ──

func TestFlagInt_present(t *testing.T) {
	n, rest := flagInt([]string{"--limit", "42"}, "--limit", 10)
	if n != 42 {
		t.Errorf("expected 42, got %d", n)
	}
	if len(rest) != 0 {
		t.Errorf("unexpected rest: %v", rest)
	}
}

func TestFlagInt_absent(t *testing.T) {
	n, _ := flagInt([]string{"--foo", "bar"}, "--limit", 10)
	if n != 10 {
		t.Errorf("expected default 10, got %d", n)
	}
}

func TestFlagInt_invalid(t *testing.T) {
	// invalid value → default returned, no panic
	n, _ := flagInt([]string{"--limit", "notanumber"}, "--limit", 7)
	if n != 7 {
		t.Errorf("expected default 7, got %d", n)
	}
}

// ── positional ──

func TestPositional_mixed(t *testing.T) {
	args := []string{"durov", "--unread", "--limit", "5"}
	got := positional(args)
	if len(got) != 1 || got[0] != "durov" {
		t.Errorf("unexpected positionals: %v", got)
	}
}

func TestPositional_boolFlagBeforeValueFlag(t *testing.T) {
	// Regression: old positional() ate --limit because --unread was bool.
	// Fix: a "--flag" only skips the next token if it doesn't start with "-".
	// So "--unread --limit 5": --limit is NOT consumed as --unread's value.
	args := []string{"--unread", "--limit", "5"}
	got := positional(args)
	// "5" must NOT leak into positionals
	if len(got) != 0 {
		t.Errorf("unexpected positionals: %v (want [])", got)
	}
}

func TestPositional_noFlags(t *testing.T) {
	args := []string{"search", "messages", "hello"}
	got := positional(args)
	if len(got) != 3 {
		t.Errorf("unexpected positionals: %v", got)
	}
}

func TestPositional_empty(t *testing.T) {
	got := positional(nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

// ── extractGlobalFlags ──

func TestExtractGlobalFlags_account(t *testing.T) {
	acc, _, rem := extractGlobalFlags([]string{"--account", "+79001234567", "me"})
	if acc != "+79001234567" {
		t.Errorf("expected +79001234567, got %q", acc)
	}
	if len(rem) != 1 || rem[0] != "me" {
		t.Errorf("unexpected remaining: %v", rem)
	}
}

func TestExtractGlobalFlags_shortAccount(t *testing.T) {
	acc, _, _ := extractGlobalFlags([]string{"-a", "+79001234567", "me"})
	if acc != "+79001234567" {
		t.Errorf("expected +79001234567, got %q", acc)
	}
}

func TestExtractGlobalFlags_timeout(t *testing.T) {
	_, timeout, _ := extractGlobalFlags([]string{"--timeout", "30", "dialogs"})
	if timeout != 30*time.Second {
		t.Errorf("expected 30s, got %v", timeout)
	}
}

func TestExtractGlobalFlags_invalidTimeout(t *testing.T) {
	// invalid timeout → zero duration, no panic
	_, timeout, _ := extractGlobalFlags([]string{"--timeout", "bad", "dialogs"})
	if timeout != 0 {
		t.Errorf("expected 0, got %v", timeout)
	}
}

// ── parseSince ──

func TestParseSince_hours(t *testing.T) {
	cutoff, err := parseSince("2h")
	if err != nil {
		t.Fatal(err)
	}
	diff := time.Since(cutoff)
	if diff < 1*time.Hour || diff > 3*time.Hour {
		t.Errorf("expected ~2h ago, got diff=%v", diff)
	}
}

func TestParseSince_minutes(t *testing.T) {
	cutoff, err := parseSince("30m")
	if err != nil {
		t.Fatal(err)
	}
	diff := time.Since(cutoff)
	if diff < 25*time.Minute || diff > 35*time.Minute {
		t.Errorf("expected ~30m ago, got diff=%v", diff)
	}
}

func TestParseSince_days(t *testing.T) {
	cutoff, err := parseSince("7d")
	if err != nil {
		t.Fatal(err)
	}
	diff := time.Since(cutoff)
	if diff < 6*24*time.Hour || diff > 8*24*time.Hour {
		t.Errorf("expected ~7d ago, got diff=%v", diff)
	}
}

func TestParseSince_invalid(t *testing.T) {
	cases := []string{"bad", "0d", "-1h", ""}
	for _, s := range cases {
		_, err := parseSince(s)
		if err == nil {
			t.Errorf("parseSince(%q) expected error, got nil", s)
		}
	}
}

func TestExtractGlobalFlags_noGlobals(t *testing.T) {
	acc, timeout, rem := extractGlobalFlags([]string{"dialogs", "--unread"})
	if acc != "" || timeout != 0 {
		t.Errorf("expected no globals, got acc=%q timeout=%v", acc, timeout)
	}
	if len(rem) != 2 {
		t.Errorf("unexpected remaining: %v", rem)
	}
}
