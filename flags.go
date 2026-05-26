package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// flagStr extracts a named string flag from args, returning the value and the remaining args.
func flagStr(args []string, flag string) (string, []string) {
	for i := 0; i < len(args); i++ {
		if args[i] == flag && i+1 < len(args) {
			val := args[i+1]
			rest := make([]string, 0, len(args)-2)
			rest = append(rest, args[:i]...)
			rest = append(rest, args[i+2:]...)
			return val, rest
		}
	}
	return "", args
}

// flagBool extracts a boolean flag from args, returning true if present and the remaining args.
func flagBool(args []string, flag string) (bool, []string) {
	for i, a := range args {
		if a == flag {
			rest := make([]string, 0, len(args)-1)
			rest = append(rest, args[:i]...)
			rest = append(rest, args[i+1:]...)
			return true, rest
		}
	}
	return false, args
}

// flagInt extracts an integer flag from args, using def as default on missing or invalid value.
func flagInt(args []string, flag string, def int) (int, []string) {
	v, rest := flagStr(args, flag)
	if v == "" {
		return def, rest
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: invalid value %q for %s, using default %d\n", v, flag, def)
		return def, rest
	}
	return n, rest
}

// positional returns non-flag arguments. A "--flag" skips the next token only
// if it doesn't start with "-" (i.e., it looks like a value, not another flag).
func positional(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--") {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// extractGlobalFlags strips --account/-a and --timeout from args,
// returning them separately along with the remaining args.
// parseSince converts a human duration string ("1h", "30m", "7d") to a time.Time cutoff.
// Supports Go duration formats plus "d" suffix for days.
func parseSince(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n <= 0 {
			return time.Time{}, fmt.Errorf("invalid duration %q: use e.g. 7d", s)
		}
		return time.Now().Add(-time.Duration(n) * 24 * time.Hour), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return time.Time{}, fmt.Errorf("invalid duration %q: use formats like 1h, 30m, 7d", s)
	}
	return time.Now().Add(-d), nil
}

// parseLocalTime parses a "YYYY-MM-DD HH:MM" timestamp in the local timezone.
// flagName is used in the error message (e.g. "--at", "--until") so callers
// don't all have to repeat the format string.
func parseLocalTime(flagName, s string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: expected format \"YYYY-MM-DD HH:MM\", got %q", flagName, s)
	}
	return t, nil
}

// splitCSV splits a comma-separated list, trimming whitespace and dropping empty entries.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// parseDuration converts a human duration string ("1h", "30m", "7d") to time.Duration.
// Supports Go duration formats plus "d" suffix for days.
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid duration %q: use e.g. 7d", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid duration %q: use formats like 1h, 30m, 7d", s)
	}
	return d, nil
}

func extractGlobalFlags(args []string) (account string, timeout time.Duration, remaining []string) {
	for i := 0; i < len(args); i++ {
		switch {
		case (args[i] == "--account" || args[i] == "-a") && i+1 < len(args):
			account = args[i+1]
			i++
		case args[i] == "--timeout" && i+1 < len(args):
			if secs, err := strconv.Atoi(args[i+1]); err == nil {
				timeout = time.Duration(secs) * time.Second
			} else {
				fmt.Fprintf(os.Stderr, "Warning: invalid --timeout value %q, ignored\n", args[i+1])
			}
			i++
		default:
			remaining = append(remaining, args[i])
		}
	}
	return
}
