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
	"path/filepath"
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

Auth (interactive):
  auth <phone> [--password <2fa>]        Authorize an account (reads code from stdin)
  auth-qr                                Authorize by scanning a QR code (no phone needed)
  status                                 Check session status

Telegram:
  me                                     Account info
  dialogs [--unread] [--limit <n>] [--type user|channel|group] [--archived] [--since <d>]  List dialogs
  read <name> [--offset <n>] [--since <duration>] [--format text] [--media-only] [--from-me]  Read messages
  export <name> [--limit <n>]                         Export full history (stdout JSON)
  send <name> <text...> [--at "YYYY-MM-DD HH:MM"] [--parse-mode html|markdown]  Send message
  send-file <name> <path>                             Send a file or document
  send-album <name> <file1> [<file2>...] [--caption "..."] [--spoiler] [--at "YYYY-MM-DD HH:MM"]  Album
  reply <name> <message-id> <text...>                 Reply to a specific message
  edit <name> <message-id> <text...>                  Edit own message
  delete <name> <message-id> [<id>...]                Delete message(s)
  react <name> <message-id> <emoji>                   Add reaction to a message
  forward <from> <message-id> <to>                    Forward message to another dialog
  forward-copy <from> <message-id> <to>               Copy message without "Forwarded from" label
  mark-read <name>                                    Mark dialog as read
  reactions <name> <message-id>                       Get reaction counts on a message
  search <dialog> <query> [--limit <n>]               Search messages in dialog
  search-all <query> [--limit <n>]                    Search messages across all chats
  search-groups <query> [--limit <n>]                 Search public groups/channels
  join <target>                                       Join group/channel (username or t.me link)
  leave <name>                                        Leave group/channel
  invite <group> <user>                               Invite user or bot to a group/channel
  info <name>                                         Get info about a user, group, or channel
  members <name> [--limit <n>]                        List members of a group or channel
  admins <name>                                       List admins with their permissions
  topics <name>                                       List forum topics in a supergroup
  invite-link <name>                                  Generate an invite link (requires admin)
  watch <name> [--interval <s>] [--keyword <w>] [--event new,edit]  Watch for messages
  get-message <name> <id> [<id>...]                   Fetch specific messages by ID
  scan <name> --from <id> --to <id>                   Scan a message ID range (IDscan)
  pin <name> <message-id> [--silent]                  Pin a message
  unpin <name> <message-id>                           Unpin a message
  mute <name> [--duration <d>]                        Mute notifications (1h, 30m, 7d; default: permanent)
  unmute <name>                                       Unmute notifications
  download-media <name> <message-id> <dir>            Download media from a message to a directory
  user-photos <name> [--save-to <dir>]                List (and optionally download) user profile photos
  common-chats <user>                                 Find common groups/channels with a user
  create-group <title> [<user>...]                    Create a new group with optional initial members
  my-channels [--owned]                               List channels/supergroups where you are admin
  kick <group> <user>                                 Remove user from a group (can re-join)
  ban <group> <user> [--until "YYYY-MM-DD HH:MM"]    Ban user from a channel/supergroup
  unban <group> <user>                                Unban user in a channel/supergroup
  promote <group> <user> [--perms <p,...>] [--rank <title>]  Make user an admin
  demote <group> <user>                               Remove admin rights from a user
  set-title <name> <title>                            Set group/channel title
  set-description <name> <text>                       Set group/channel description
  set-photo <name> <path>                             Set group/channel photo from file
  contacts                                            List your Telegram contacts
  contacts add <phone> <first-name> [<last-name>]    Add a contact by phone number
  contacts delete <user>                              Remove a contact
  sessions                                            List active sessions
  sessions revoke <hash>                             Terminate a session by hash
  block <user>                                        Block a user
  unblock <user>                                      Unblock a user
  blocked                                             List blocked users
  delete-history <name> [--revoke]                   Delete conversation history
  archive <name>                                      Archive a dialog
  unarchive <name>                                    Move dialog back from archive
  message-link <name> <id>                           Generate t.me link to a message
  delete-user-messages <chat> <user>                 Delete all messages from a user (channel/supergroup)
  stats <name>                                        Channel/supergroup analytics
  transcribe <name> <id>                             Voice-to-text transcription
  restrict <chat> <user> [--no-send] [--no-media] [--no-web-preview] [--no-polls] [--until "..."]  Partial ban
  create-channel <title> [--supergroup] [--username @slug]  Create broadcast channel or supergroup
  set-discussion <channel> <group>                    Link supergroup as channel discussion (empty to unlink)
  comment <channel> <message-id> <text...>            Post a comment under a channel post (via linked group)
  search-members <group> <query> [--limit <n>]       Search members by name/username
  parse-members <group> [--limit <n>] [--out file.csv] [--format json]  Export all members to CSV
  active-members <group> [--days <n>] [--out file.json]  Members who wrote recently
  broadcast --file <path> --msg <text> [--delay 30s] [--limit <n>] [--parse-mode html|markdown]  Send to list
  enrich --file <path> [--out enriched.json]         Enrich member list with username/bio/phone
  mass-invite <group> --file <path> [--delay 60s] [--limit <n>]  Batch invite from file
  set-profile [--name "First Last"] [--first-name <n>] [--last-name <n>] [--bio <text>] [--username <u>]
  set-profile-photo <path>                           Set own profile photo
  poll <target> <question> <opt1> <opt2> [...] [--anonymous] [--multiple]  Create a poll
  schedule <name> <text...> --at "YYYY-MM-DD HH:MM"  Schedule a message (alias for send --at)
  resolve <username|id>                              Resolve username ↔ numeric ID
  list-scheduled <name>                              List messages in the scheduled queue
  paid-invite-link <name> --stars <amount> [--title "label"]  Stars-subscription invite link (30d)
  chatlist-preview <addlist-url-or-slug>             Inspect a chat-folder share link
  chatlist-join <addlist-url-or-slug> [--peers @ch1,@ch2,...] [--dry-run]  Join chats from folder link
  download-channel <chat> [--out <dir>] [--limit N] [--skip-media] [--resume] [--batch N]  Dump channel to JSON + media
  download-network <addlist-url-or-slug> [--out <dir>] [--limit N] [--skip-media] [--skip-existing] [--peers @ch1,...] [--public-only] [--resume] [--auto-join] [--pause N]  Bulk-dump a folder of channels

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
  tg-cli read team-chat --format text
  tg-cli export team-chat --limit 500 > chat.json
  tg-cli send @alice "Hello!"
  tg-cli send team-chat "Scheduled msg" --at "2026-03-15 10:00"
  tg-cli join https://t.me/+AbCdEfGhIjK
  tg-cli search aws_group "CDK"
  tg-cli my-channels
  tg-cli kick team-chat @alice
  tg-cli ban team-chat @alice --until "2026-04-01 00:00"
  tg-cli promote team-chat @alice --perms post,delete --rank "Editor"
  tg-cli contacts
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

	case "auth-qr":
		if err := cmdAuthQR(c); err != nil {
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
		archived, rest2 := flagBool(rest, "--archived")
		limit, rest3 := flagInt(rest2, "--limit", 0)
		typeFilter, rest4 := flagStr(rest3, "--type")
		sinceStr, _ := flagStr(rest4, "--since")
		var sinceTime time.Time
		if sinceStr != "" {
			var err error
			sinceTime, err = parseSince(sinceStr)
			if err != nil {
				fatalf("%v", err)
			}
		}
		if err := cmdDialogs(c, unread, limit, typeFilter, archived, sinceTime); err != nil {
			fatalf("%v", err)
		}

	case "read":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli read <name> [--offset <n>] [--since <duration>] [--format text] [--media-only]")
		}
		offset, args2 := flagInt(args, "--offset", 0)
		sinceStr, args3 := flagStr(args2, "--since")
		format, args4 := flagStr(args3, "--format")
		mediaOnly, args5 := flagBool(args4, "--media-only")
		fromMe, _ := flagBool(args5, "--from-me")
		var since time.Time
		if sinceStr != "" {
			var err error
			since, err = parseSince(sinceStr)
			if err != nil {
				fatalf("%v", err)
			}
		}
		if err := cmdRead(c, pos[0], offset, since, format, mediaOnly, fromMe); err != nil {
			fatalf("%v", err)
		}

	case "send":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli send <name> <text...> [--at \"YYYY-MM-DD HH:MM\"] [--parse-mode html|markdown]")
		}
		atStr, args2 := flagStr(args, "--at")
		parseMode, _ := flagStr(args2, "--parse-mode")
		var scheduleAt time.Time
		if atStr != "" {
			t, err := time.ParseInLocation("2006-01-02 15:04", atStr, time.Local)
			if err != nil {
				fatalf("--at: expected format \"YYYY-MM-DD HH:MM\", got %q", atStr)
			}
			scheduleAt = t
		}
		if err := cmdSend(c, pos[0], strings.Join(pos[1:], " "), scheduleAt, parseMode); err != nil {
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

	case "forward-copy":
		pos := positional(args)
		if len(pos) < 3 {
			fatalf("usage: tg-cli forward-copy <from> <message-id> <to>")
		}
		msgID, err := strconv.Atoi(pos[1])
		if err != nil {
			fatalf("invalid message ID %q: must be a number", pos[1])
		}
		if err := cmdForwardCopy(c, pos[0], msgID, pos[2]); err != nil {
			fatalf("%v", err)
		}

	case "reactions":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli reactions <name> <message-id>")
		}
		msgID, err := strconv.Atoi(pos[1])
		if err != nil {
			fatalf("invalid message ID %q: must be a number", pos[1])
		}
		if err := cmdReactions(c, pos[0], msgID); err != nil {
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
			fatalf("usage: tg-cli watch <name> [--interval <seconds>] [--keyword <word>] [--event new,edit]")
		}
		interval, args2 := flagInt(args, "--interval", 5)
		kwStr, args3 := flagStr(args2, "--keyword")
		evStr, _ := flagStr(args3, "--event")
		var keywords []string
		if kwStr != "" {
			for _, kw := range strings.Split(kwStr, ",") {
				if kw = strings.TrimSpace(kw); kw != "" {
					keywords = append(keywords, kw)
				}
			}
		}
		var events []string
		if evStr != "" {
			for _, ev := range strings.Split(evStr, ",") {
				if ev = strings.TrimSpace(ev); ev != "" {
					events = append(events, ev)
				}
			}
		}
		if err := cmdWatch(c, pos[0], interval, keywords, events); err != nil {
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

	case "admins":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli admins <name>")
		}
		if err := cmdAdmins(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "get-message":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli get-message <name> <id> [<id>...]")
		}
		var msgIDs []int
		for _, s := range pos[1:] {
			id, err := strconv.Atoi(s)
			if err != nil {
				fatalf("invalid message ID %q: must be a number", s)
			}
			msgIDs = append(msgIDs, id)
		}
		if err := cmdGetMessage(c, pos[0], msgIDs); err != nil {
			fatalf("%v", err)
		}

	case "scan":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli scan <name> --from <id> --to <id>")
		}
		fromID, _ := flagInt(args, "--from", 0)
		toID, _ := flagInt(args, "--to", 0)
		if fromID <= 0 || toID <= 0 || fromID > toID {
			fatalf("--from and --to must be positive integers with --from <= --to")
		}
		if err := cmdScan(c, pos[0], fromID, toID); err != nil {
			fatalf("%v", err)
		}

	case "common-chats":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli common-chats <user>")
		}
		if err := cmdCommonChats(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "user-photos":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli user-photos <user> [--save-to <dir>]")
		}
		saveDir, _ := flagStr(args, "--save-to")
		if err := cmdUserPhotos(c, pos[0], saveDir); err != nil {
			fatalf("%v", err)
		}

	case "download-media":
		pos := positional(args)
		if len(pos) < 3 {
			fatalf("usage: tg-cli download-media <name> <message-id> <dir>")
		}
		msgID, err := strconv.Atoi(pos[1])
		if err != nil {
			fatalf("invalid message ID %q: must be a number", pos[1])
		}
		if err := cmdDownloadMedia(c, pos[0], msgID, pos[2]); err != nil {
			fatalf("%v", err)
		}

	case "pin":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli pin <name> <message-id> [--silent]")
		}
		msgID, err := strconv.Atoi(pos[1])
		if err != nil {
			fatalf("invalid message ID %q: must be a number", pos[1])
		}
		silent, _ := flagBool(args, "--silent")
		if err := cmdPin(c, pos[0], msgID, silent); err != nil {
			fatalf("%v", err)
		}

	case "unpin":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli unpin <name> <message-id>")
		}
		msgID, err := strconv.Atoi(pos[1])
		if err != nil {
			fatalf("invalid message ID %q: must be a number", pos[1])
		}
		if err := cmdUnpin(c, pos[0], msgID); err != nil {
			fatalf("%v", err)
		}

	case "mute":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli mute <name> [--duration <d>]")
		}
		var dur time.Duration
		if dStr, _ := flagStr(args, "--duration"); dStr != "" {
			var err error
			dur, err = parseDuration(dStr)
			if err != nil {
				fatalf("%v", err)
			}
		}
		if err := cmdMute(c, pos[0], dur); err != nil {
			fatalf("%v", err)
		}

	case "unmute":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli unmute <name>")
		}
		if err := cmdUnmute(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "topics":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli topics <name>")
		}
		if err := cmdTopics(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "invite-link":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli invite-link <name>")
		}
		if err := cmdInviteLink(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "paid-invite-link":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli paid-invite-link <name> --stars <amount> [--title \"label\"]")
		}
		stars, _ := flagInt(args, "--stars", 0)
		if stars <= 0 {
			fatalf("--stars must be > 0")
		}
		title, _ := flagStr(args, "--title")
		if err := cmdPaidInviteLink(c, pos[0], int64(stars), title); err != nil {
			fatalf("%v", err)
		}

	case "invite":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli invite <group> <user>")
		}
		if err := cmdInvite(c, pos[0], pos[1]); err != nil {
			fatalf("%v", err)
		}

	case "create-group":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli create-group <title> [<user>...]")
		}
		title := pos[0]
		members := pos[1:]
		if err := cmdCreateGroup(c, title, members); err != nil {
			fatalf("%v", err)
		}

	case "my-channels", "my-admins":
		owned, _ := flagBool(args, "--owned")
		if err := cmdMyChannels(c, owned); err != nil {
			fatalf("%v", err)
		}

	case "kick":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli kick <group> <user>")
		}
		if err := cmdKick(c, pos[0], pos[1]); err != nil {
			fatalf("%v", err)
		}

	case "ban":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli ban <group> <user> [--until \"YYYY-MM-DD HH:MM\"]")
		}
		untilStr, _ := flagStr(args, "--until")
		var until time.Time
		if untilStr != "" {
			t, err := time.ParseInLocation("2006-01-02 15:04", untilStr, time.Local)
			if err != nil {
				fatalf("--until: expected format \"YYYY-MM-DD HH:MM\", got %q", untilStr)
			}
			until = t
		}
		if err := cmdBan(c, pos[0], pos[1], until); err != nil {
			fatalf("%v", err)
		}

	case "unban":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli unban <group> <user>")
		}
		if err := cmdUnban(c, pos[0], pos[1]); err != nil {
			fatalf("%v", err)
		}

	case "promote":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli promote <group> <user> [--perms <p,...>] [--rank <title>]")
		}
		permsStr, _ := flagStr(args, "--perms")
		rank, _ := flagStr(args, "--rank")
		var perms []string
		if permsStr != "" {
			for _, p := range strings.Split(permsStr, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					perms = append(perms, p)
				}
			}
		}
		if len(perms) == 0 {
			perms = []string{"all"}
		}
		if err := cmdPromote(c, pos[0], pos[1], perms, rank); err != nil {
			fatalf("%v", err)
		}

	case "demote":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli demote <group> <user>")
		}
		if err := cmdDemote(c, pos[0], pos[1]); err != nil {
			fatalf("%v", err)
		}

	case "set-title":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli set-title <name> <title>")
		}
		if err := cmdSetTitle(c, pos[0], strings.Join(pos[1:], " ")); err != nil {
			fatalf("%v", err)
		}

	case "set-description":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli set-description <name> <text>")
		}
		if err := cmdSetDescription(c, pos[0], strings.Join(pos[1:], " ")); err != nil {
			fatalf("%v", err)
		}

	case "set-photo":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli set-photo <name> <path>")
		}
		if err := cmdSetPhoto(c, pos[0], pos[1]); err != nil {
			fatalf("%v", err)
		}

	case "contacts":
		pos := positional(args)
		if len(pos) > 0 && pos[0] == "add" {
			if len(pos) < 3 {
				fatalf("usage: tg-cli contacts add <phone> <first-name> [<last-name>]")
			}
			lastName := ""
			if len(pos) >= 4 {
				lastName = pos[3]
			}
			if err := cmdContactsAdd(c, pos[1], pos[2], lastName); err != nil {
				fatalf("%v", err)
			}
		} else if len(pos) > 0 && (pos[0] == "delete" || pos[0] == "remove") {
			if len(pos) < 2 {
				fatalf("usage: tg-cli contacts delete <user>")
			}
			if err := cmdContactsDelete(c, pos[1]); err != nil {
				fatalf("%v", err)
			}
		} else {
			if err := cmdContacts(c); err != nil {
				fatalf("%v", err)
			}
		}

	case "send-album":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli send-album <name> <file1> [<file2>...] [--caption \"…\"] [--spoiler] [--at \"YYYY-MM-DD HH:MM\"]")
		}
		caption, _ := flagStr(args, "--caption")
		spoiler, _ := flagBool(args, "--spoiler")
		atStr, _ := flagStr(args, "--at")
		var scheduleAt time.Time
		if atStr != "" {
			t, err := time.ParseInLocation("2006-01-02 15:04", atStr, time.Local)
			if err != nil {
				fatalf("--at: expected format \"YYYY-MM-DD HH:MM\", got %q", atStr)
			}
			scheduleAt = t
		}
		if err := cmdSendAlbum(c, pos[0], pos[1:], caption, spoiler, scheduleAt); err != nil {
			fatalf("%v", err)
		}

	case "download-network":
		urlOrSlug, _ := flagStr(args, "--chatlist")
		if urlOrSlug == "" {
			pos := positional(args)
			if len(pos) > 0 {
				urlOrSlug = pos[0]
			}
		}
		if urlOrSlug == "" {
			fatalf("usage: tg-cli download-network <addlist-url-or-slug> [--out <dir>] [--limit N] [--skip-media] [--skip-existing] [--peers @ch1,...] [--public-only] [--resume] [--auto-join] [--pause N]")
		}
		outDir, _ := flagStr(args, "--out")
		if outDir == "" {
			outDir = filepath.Join(".", "dump", "network_"+extractChatlistSlug(urlOrSlug))
		}
		limit, _ := flagInt(args, "--limit", 0)
		skipMedia, _ := flagBool(args, "--skip-media")
		skipExisting, _ := flagBool(args, "--skip-existing")
		publicOnly, _ := flagBool(args, "--public-only")
		resume, _ := flagBool(args, "--resume")
		autoJoin, _ := flagBool(args, "--auto-join")
		pause, _ := flagInt(args, "--pause", 2)
		peersStr, _ := flagStr(args, "--peers")
		var peerList []string
		if peersStr != "" {
			for _, p := range strings.Split(peersStr, ",") {
				if s := strings.TrimSpace(p); s != "" {
					peerList = append(peerList, s)
				}
			}
		}
		if err := cmdDownloadNetwork(c, urlOrSlug, outDir, limit, skipMedia, skipExisting, peerList, publicOnly, resume, autoJoin, pause); err != nil {
			fatalf("%v", err)
		}

	case "download-channel":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli download-channel <chat> [--out <dir>] [--limit N] [--skip-media] [--resume] [--batch N]")
		}
		outDir, _ := flagStr(args, "--out")
		if outDir == "" {
			outDir = filepath.Join(".", "dump", pos[0])
		}
		limit, _ := flagInt(args, "--limit", 0)
		skipMedia, _ := flagBool(args, "--skip-media")
		resume, _ := flagBool(args, "--resume")
		batch, _ := flagInt(args, "--batch", 100)
		if err := cmdDownloadChannel(c, pos[0], outDir, limit, skipMedia, resume, batch); err != nil {
			fatalf("%v", err)
		}

	case "chatlist-preview":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli chatlist-preview <addlist-url-or-slug>")
		}
		if err := cmdChatlistPreview(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "chatlist-join":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli chatlist-join <addlist-url-or-slug> [--peers @ch1,@ch2,...] [--dry-run]")
		}
		peersStr, _ := flagStr(args, "--peers")
		var peerList []string
		if peersStr != "" {
			for _, p := range strings.Split(peersStr, ",") {
				if s := strings.TrimSpace(p); s != "" {
					peerList = append(peerList, s)
				}
			}
		}
		dryRun, _ := flagBool(args, "--dry-run")
		if err := cmdChatlistJoin(c, pos[0], peerList, dryRun); err != nil {
			fatalf("%v", err)
		}

	case "delete-user-messages":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli delete-user-messages <chat> <user>")
		}
		if err := cmdDeleteUserMessages(c, pos[0], pos[1]); err != nil {
			fatalf("%v", err)
		}

	case "stats":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli stats <name>")
		}
		if err := cmdStats(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "transcribe":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli transcribe <name> <message-id>")
		}
		msgID, err := strconv.Atoi(pos[1])
		if err != nil {
			fatalf("invalid message ID %q: must be a number", pos[1])
		}
		if err := cmdTranscribe(c, pos[0], msgID); err != nil {
			fatalf("%v", err)
		}

	case "restrict":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli restrict <chat> <user> [--no-send] [--no-media] [--no-stickers] [--no-web-preview] [--no-polls] [--no-change-info] [--no-invite] [--no-pin] [--until \"YYYY-MM-DD HH:MM\"]")
		}
		untilStr, _ := flagStr(args, "--until")
		var until time.Time
		if untilStr != "" {
			t, err := time.ParseInLocation("2006-01-02 15:04", untilStr, time.Local)
			if err != nil {
				fatalf("--until: expected format \"YYYY-MM-DD HH:MM\", got %q", untilStr)
			}
			until = t
		}
		noSend, _ := flagBool(args, "--no-send")
		noMedia, _ := flagBool(args, "--no-media")
		noStickers, _ := flagBool(args, "--no-stickers")
		noWebPreview, _ := flagBool(args, "--no-web-preview")
		noPolls, _ := flagBool(args, "--no-polls")
		noChangeInfo, _ := flagBool(args, "--no-change-info")
		noInvite, _ := flagBool(args, "--no-invite")
		noPin, _ := flagBool(args, "--no-pin")
		if err := cmdRestrict(c, pos[0], pos[1], until, noSend, noMedia, noStickers, noWebPreview, noPolls, noChangeInfo, noInvite, noPin); err != nil {
			fatalf("%v", err)
		}

	case "create-channel":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli create-channel <title> [--supergroup] [--username @slug]")
		}
		isSupergroup, _ := flagBool(args, "--supergroup")
		username, _ := flagStr(args, "--username")
		if err := cmdCreateChannel(c, strings.Join(pos, " "), isSupergroup, username); err != nil {
			fatalf("%v", err)
		}

	case "list-scheduled", "scheduled":
		pos := positional(args)
		if len(pos) < 1 {
			fatalf("usage: tg-cli list-scheduled <name>")
		}
		if err := cmdListScheduled(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "set-discussion":
		pos := positional(args)
		if len(pos) < 1 {
			fatalf("usage: tg-cli set-discussion <channel> <group>  (pass empty group to unlink)")
		}
		group := ""
		if len(pos) >= 2 {
			group = pos[1]
		}
		if err := cmdSetDiscussion(c, pos[0], group); err != nil {
			fatalf("%v", err)
		}

	case "comment":
		pos := positional(args)
		if len(pos) < 3 {
			fatalf("usage: tg-cli comment <channel> <message-id> <text...>")
		}
		msgID, err := strconv.Atoi(pos[1])
		if err != nil {
			fatalf("invalid message ID %q: must be a number", pos[1])
		}
		if err := cmdComment(c, pos[0], msgID, strings.Join(pos[2:], " ")); err != nil {
			fatalf("%v", err)
		}

	case "sessions":
		pos := positional(args)
		if len(pos) > 0 && pos[0] == "revoke" {
			if len(pos) < 2 {
				fatalf("usage: tg-cli sessions revoke <hash>")
			}
			hash, err := strconv.ParseInt(pos[1], 10, 64)
			if err != nil {
				fatalf("invalid session hash %q: must be a number", pos[1])
			}
			if err := cmdSessionsRevoke(c, hash); err != nil {
				fatalf("%v", err)
			}
		} else {
			if err := cmdSessions(c); err != nil {
				fatalf("%v", err)
			}
		}

	case "block":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli block <user>")
		}
		if err := cmdBlock(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "unblock":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli unblock <user>")
		}
		if err := cmdUnblock(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "blocked":
		if err := cmdBlocked(c); err != nil {
			fatalf("%v", err)
		}

	case "delete-history":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli delete-history <name> [--revoke]")
		}
		revoke, _ := flagBool(args, "--revoke")
		if err := cmdDeleteHistory(c, pos[0], revoke); err != nil {
			fatalf("%v", err)
		}

	case "archive":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli archive <name>")
		}
		if err := cmdArchive(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "unarchive":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli unarchive <name>")
		}
		if err := cmdUnarchive(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "message-link":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli message-link <name> <message-id>")
		}
		msgID, err := strconv.Atoi(pos[1])
		if err != nil {
			fatalf("invalid message ID %q: must be a number", pos[1])
		}
		if err := cmdMessageLink(c, pos[0], msgID); err != nil {
			fatalf("%v", err)
		}

	case "search-members":
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli search-members <group> <query> [--limit <n>]")
		}
		limit, _ := flagInt(args, "--limit", 50)
		if err := cmdSearchMembers(c, pos[0], strings.Join(pos[1:], " "), limit); err != nil {
			fatalf("%v", err)
		}

	case "parse-members":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli parse-members <group> [--limit <n>] [--out <file>] [--format json]")
		}
		limit, args2 := flagInt(args, "--limit", 5000)
		outFile, args3 := flagStr(args2, "--out")
		format, _ := flagStr(args3, "--format")
		if err := cmdParseMembers(c, pos[0], limit, outFile, format); err != nil {
			fatalf("%v", err)
		}

	case "active-members":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli active-members <group> [--days <n>] [--out <file>]")
		}
		days, args2 := flagInt(args, "--days", 30)
		outFile, _ := flagStr(args2, "--out")
		if err := cmdActiveMembers(c, pos[0], days, outFile); err != nil {
			fatalf("%v", err)
		}

	case "broadcast":
		fileArg, args2 := flagStr(args, "--file")
		stdin, args3 := flagBool(args2, "--stdin")
		msgArg, args4 := flagStr(args3, "--msg")
		delayStr, args5 := flagStr(args4, "--delay")
		limit, args6 := flagInt(args5, "--limit", 0)
		parseMode, _ := flagStr(args6, "--parse-mode")
		if stdin {
			fileArg = "-"
		}
		if fileArg == "" {
			fatalf("usage: tg-cli broadcast --file <path> --msg <text> [--delay 30s] [--limit <n>] [--parse-mode html|markdown]")
		}
		if msgArg == "" {
			pos := positional(args)
			if len(pos) > 0 {
				msgArg = strings.Join(pos, " ")
			}
		}
		if msgArg == "" {
			fatalf("--msg is required")
		}
		var delay time.Duration
		if delayStr != "" {
			var err error
			delay, err = parseDuration(delayStr)
			if err != nil {
				fatalf("%v", err)
			}
		}
		if err := cmdBroadcast(c, fileArg, msgArg, delay, limit, parseMode); err != nil {
			fatalf("%v", err)
		}

	case "enrich":
		fileArg, args2 := flagStr(args, "--file")
		stdin, _ := flagBool(args2, "--stdin")
		outFile, _ := flagStr(args, "--out")
		if stdin {
			fileArg = "-"
		}
		if fileArg == "" {
			pos := positional(args)
			if len(pos) > 0 {
				fileArg = pos[0]
			}
		}
		if fileArg == "" {
			fatalf("usage: tg-cli enrich --file <path> [--out enriched.json]")
		}
		if err := cmdEnrich(c, fileArg, outFile); err != nil {
			fatalf("%v", err)
		}

	case "mass-invite":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli mass-invite <group> --file <path> [--delay 60s] [--limit <n>]")
		}
		fileArg, args2 := flagStr(args, "--file")
		stdin, args3 := flagBool(args2, "--stdin")
		delayStr, args4 := flagStr(args3, "--delay")
		limit, _ := flagInt(args4, "--limit", 0)
		if stdin {
			fileArg = "-"
		}
		if fileArg == "" {
			fatalf("--file is required")
		}
		var delay time.Duration
		if delayStr != "" {
			var err error
			delay, err = parseDuration(delayStr)
			if err != nil {
				fatalf("%v", err)
			}
		}
		if err := cmdMassInvite(c, pos[0], fileArg, delay, limit); err != nil {
			fatalf("%v", err)
		}

	case "set-profile":
		firstName, _ := flagStr(args, "--first-name")
		lastName, _ := flagStr(args, "--last-name")
		bio, _ := flagStr(args, "--bio")
		username, _ := flagStr(args, "--username")
		// --name splits "First Last" into first + last
		if name, _ := flagStr(args, "--name"); name != "" {
			parts := strings.SplitN(name, " ", 2)
			if firstName == "" {
				firstName = parts[0]
			}
			if lastName == "" && len(parts) > 1 {
				lastName = parts[1]
			}
		}
		if err := cmdSetProfile(c, firstName, lastName, bio, username); err != nil {
			fatalf("%v", err)
		}

	case "set-profile-photo":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli set-profile-photo <path>")
		}
		if err := cmdSetProfilePhoto(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "poll":
		pos := positional(args)
		if len(pos) < 3 {
			fatalf("usage: tg-cli poll <target> <question> <option1> <option2> [<option3>...] [--anonymous] [--multiple]")
		}
		anonymous, _ := flagBool(args, "--anonymous")
		multiple, _ := flagBool(args, "--multiple")
		if err := cmdPoll(c, pos[0], pos[1], pos[2:], anonymous, multiple); err != nil {
			fatalf("%v", err)
		}

	case "resolve":
		pos := positional(args)
		if len(pos) == 0 {
			fatalf("usage: tg-cli resolve <username|id>")
		}
		if err := cmdResolve(c, pos[0]); err != nil {
			fatalf("%v", err)
		}

	case "schedule":
		// Alias for: send <target> <text> --at "..."
		pos := positional(args)
		if len(pos) < 2 {
			fatalf("usage: tg-cli schedule <name> <text...> --at \"YYYY-MM-DD HH:MM\"")
		}
		atStr, args2 := flagStr(args, "--at")
		parseMode, _ := flagStr(args2, "--parse-mode")
		if atStr == "" {
			fatalf("--at is required for schedule")
		}
		t, err := time.ParseInLocation("2006-01-02 15:04", atStr, time.Local)
		if err != nil {
			fatalf("--at: expected format \"YYYY-MM-DD HH:MM\", got %q", atStr)
		}
		if err := cmdSend(c, pos[0], strings.Join(pos[1:], " "), t, parseMode); err != nil {
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
