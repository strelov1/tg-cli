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
