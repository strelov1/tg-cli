// tg-cli — standalone Telegram CLI using MTProto directly (no subprocess needed).
//
// Uses github.com/gotd/td for MTProto. Sessions stored at ~/.tg-cli/sessions/<phone>/.
// Multiple accounts supported.
//
// Usage: tg-cli [--account <phone>] <command> [flags]
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const helpText = `tg-cli — Telegram CLI (standalone, no external binaries)

Usage: tg-cli [--account <phone>] [--timeout <seconds>] <command> [flags]

Setup:
  config set <key> <value>               Set a config value
  config get <key>                       Get a config value
  config list                            Show all config

Account management:
  accounts                               List all authorized accounts
  accounts use <phone>                   Set default account
  accounts remove <phone>                Remove session

Auth (agent-friendly, two steps):
  auth-request <phone>                   Step 1: send code to phone (non-blocking)
  auth-complete <phone> --code <code> [--password <2fa>]  Step 2: finish auth

Auth (interactive, single step):
  auth <phone> [--password <2fa>]        Authorize an account (reads code from stdin)
  status                                 Check session status

Telegram:
  me                                     Account info
  dialogs [--unread] [--limit <n>]                    List dialogs (default: all)
  read <name> [--offset <n>] [--since <duration>]     Read messages (--since 1h, 30m, 7d)
  export <name> [--limit <n>]                         Export full history (stdout JSON)
  send <name> <text...>                               Send message
  send-file <name> <path>                             Send a file or document
  reply <name> <message-id> <text...>                 Reply to a specific message
  edit <name> <message-id> <text...>                  Edit own message
  delete <name> <message-id> [<id>...]                Delete message(s)
  react <name> <message-id> <emoji>                   Add reaction to a message
  forward <from> <message-id> <to>                    Forward message to another dialog
  mark-read <name>                                    Mark dialog as read
  search <dialog> <query> [--limit <n>]               Search messages in dialog
  search-all <query> [--limit <n>]                    Search messages across all chats
  search-groups <query> [--limit <n>]                 Search public groups/channels
  join <target>                                       Join group/channel (username or t.me link)
  leave <name>                                        Leave group/channel
  info <name>                                         Get info about a user, group, or channel
  members <name> [--limit <n>]                        List members of a group or channel
  watch <name> [--interval <seconds>]                 Watch for new messages (default: 5s poll)

Config keys:
  app-id           Telegram App ID  (https://my.telegram.org/apps)
  api-hash         Telegram API Hash
  default-account  Default phone number to use
  session-dir      Base dir for sessions (default: ~/.tg-cli)

Env vars override config file:
  TG_APP_ID, TG_API_HASH, SESSION_DIR, ACCOUNT

Examples:
  tg-cli config set app-id 12345
  tg-cli config set api-hash abc123def456...

  tg-cli auth +12025551234 --password mySecret2FA

  tg-cli accounts
  tg-cli accounts use +12025551234

  tg-cli me
  tg-cli --account +12025551234 me
  tg-cli dialogs --unread
  tg-cli dialogs --limit 50
  tg-cli read durov
  tg-cli export team-chat --limit 500 > chat.json
  tg-cli send @alice "Hello!"
  tg-cli join https://t.me/+AbCdEfGhIjK
  tg-cli search aws_group "CDK"
  tg-cli --timeout 30 dialogs`

func main() {
	if len(os.Args) < 2 {
		fmt.Println(helpText)
		os.Exit(0)
	}

	globalAccount, timeout, osArgs := extractGlobalFlags(os.Args[1:])
	if len(osArgs) == 0 {
		fmt.Println(helpText)
		os.Exit(0)
	}

	cmd := osArgs[0]
	args := osArgs[1:]

	if cmd == "--help" || cmd == "-h" || cmd == "help" {
		fmt.Println(helpText)
		os.Exit(0)
	}

	// Commands that don't need app-id/api-hash
	switch cmd {
	case "config":
		if err := cmdConfig(args); err != nil {
			fatalf("%v", err)
		}
		return
	case "accounts":
		if err := cmdAccounts(args); err != nil {
			fatalf("%v", err)
		}
		return
	}

	c, err := loadConfig(globalAccount, timeout)
	if err != nil {
		fatalf("%v", err)
	}

	switch cmd {
	case "auth-request":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli auth-request <phone>")
		}
		if err := cmdAuthRequest(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "auth-complete":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli auth-complete <phone> --code <code> [--password <2fa>]")
		}
		code, _ := flagStr(args, "--code")
		password, _ := flagStr(args, "--password")
		if code == "" {
			fatalf("--code is required")
		}
		if err := cmdAuthComplete(c, pos[0], code, password); err != nil {
			fatalf("%v", err)
		}

	case "auth":
		if err := cmdAuth(c, args); err != nil {
			fatalf("%v", err)
		}

	case "status":
		if err := cmdStatus(c); err != nil {
			fatalf("%v", err)
		}

	case "me":
		if err := cmdMe(c); err != nil {
			fatalf("%v", err)
		}

	case "dialogs":
		unread, rest := flagBool(args, "--unread")
		limit, _ := flagInt(rest, "--limit", 0)
		if err := cmdDialogs(c, unread, limit); err != nil {
			fatalf("%v", err)
		}

	case "read":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli read <name> [--offset <n>] [--since <duration>]")
		}
		offset, args2 := flagInt(args, "--offset", 0)
		sinceStr, _ := flagStr(args2, "--since")
		var since time.Time
		if sinceStr != "" {
			var err error
			since, err = parseSince(sinceStr)
			if err != nil {
				fatalf("%v", err)
			}
		}
		if err := cmdRead(c, pos[0], offset, since); err != nil {
			fatalf("%v", err)
		}

	case "send":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli send <name> <text...>")
		}
		if err := cmdSend(c, pos[0], strings.Join(pos[1:], " ")); err != nil {
			fatalf("%v", err)
		}

	case "mark-read":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli mark-read <name>")
		}
		if err := cmdMarkRead(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "search-groups":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli search-groups <query> [--limit <n>]")
		}
		limit, _ := flagInt(args, "--limit", 20)
		if err := cmdSearchGroups(c, strings.Join(pos, " "), limit); err != nil {
			fatalf("%v", err)
		}

	case "join":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli join <target>")
		}
		if err := cmdJoin(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "leave":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli leave <name>")
		}
		if err := cmdLeave(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "reply":
		pos := positional(args)
		if len(pos) < 3 {
			fatalf("usage: tg-cli reply <name> <message-id> <text...>")
		}
		msgID, err := strconv.Atoi(pos[1])
		if err != nil {
			fatalf("invalid message ID %q: must be a number", pos[1])
		}
		if err := cmdReply(c, pos[0], msgID, strings.Join(pos[2:], " ")); err != nil {
			fatalf("%v", err)
		}

	case "edit":
		pos := positional(args)
		if len(pos) < 3 {
			fatalf("usage: tg-cli edit <name> <message-id> <text...>")
		}
		msgID, err := strconv.Atoi(pos[1])
		if err != nil {
			fatalf("invalid message ID %q: must be a number", pos[1])
		}
		if err := cmdEdit(c, pos[0], msgID, strings.Join(pos[2:], " ")); err != nil {
			fatalf("%v", err)
		}

	case "delete":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli delete <name> <message-id> [<id>...]")
		}
		var msgIDs []int
		for _, s := range pos[1:] {
			id, err := strconv.Atoi(s)
			if err != nil {
				fatalf("invalid message ID %q: must be a number", s)
			}
			msgIDs = append(msgIDs, id)
		}
		if err := cmdDelete(c, pos[0], msgIDs); err != nil {
			fatalf("%v", err)
		}

	case "react":
		pos := positional(args)
		if len(pos) < 3 {
			fatalf("usage: tg-cli react <name> <message-id> <emoji>")
		}
		msgID, err := strconv.Atoi(pos[1])
		if err != nil {
			fatalf("invalid message ID %q: must be a number", pos[1])
		}
		if err := cmdReact(c, pos[0], msgID, pos[2]); err != nil {
			fatalf("%v", err)
		}

	case "forward":
		pos := positional(args)
		if len(pos) < 3 {
			fatalf("usage: tg-cli forward <from> <message-id> <to>")
		}
		msgID, err := strconv.Atoi(pos[1])
		if err != nil {
			fatalf("invalid message ID %q: must be a number", pos[1])
		}
		if err := cmdForward(c, pos[0], msgID, pos[2]); err != nil {
			fatalf("%v", err)
		}

	case "send-file":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli send-file <name> <path>")
		}
		if err := cmdSendFile(c, pos[0], pos[1]); err != nil {
			fatalf("%v", err)
		}

	case "info":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli info <name>")
		}
		if err := cmdInfo(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "members":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli members <name> [--limit <n>]")
		}
		limit, _ := flagInt(args, "--limit", 100)
		if err := cmdMembers(c, pos[0], limit); err != nil {
			fatalf("%v", err)
		}

	case "watch":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli watch <name> [--interval <seconds>]")
		}
		interval, _ := flagInt(args, "--interval", 5)
		if err := cmdWatch(c, pos[0], interval); err != nil {
			fatalf("%v", err)
		}

	case "search-all":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli search-all <query> [--limit <n>]")
		}
		limit, _ := flagInt(args, "--limit", 50)
		if err := cmdSearchAll(c, strings.Join(pos, " "), limit); err != nil {
			fatalf("%v", err)
		}

	case "search":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli search <dialog> <query> [--limit <n>]")
		}
		limit, _ := flagInt(args, "--limit", 50)
		if err := cmdSearch(c, pos[0], strings.Join(pos[1:], " "), limit); err != nil {
			fatalf("%v", err)
		}

	case "export":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli export <dialog> [--limit <n>]")
		}
		limit, _ := flagInt(args, "--limit", 0)
		if err := cmdExport(c, pos[0], limit); err != nil {
			fatalf("%v", err)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n%s\n", cmd, helpText)
		os.Exit(1)
	}
}

func fatalf(format string, a ...any) {
	if format == "%v" && len(a) == 1 && a[0] == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", a...)
	os.Exit(1)
}
