# tg-cli — v1.7.0

**Agentic-native Telegram CLI** — control Telegram from the command line with no interactive input required. Designed for AI agents: every command is a single call, output is JSON on stdout.

Built on [gotd/td](https://github.com/gotd/td) (MTProto, Go). No external binaries needed.

---

## Why agentic-native?

Standard Telegram CLIs require an interactive TTY (entering codes, passwords). `tg-cli` solves this:

- **Two-step auth** — the agent calls `auth-request`, waits for the code, then calls `auth-complete`. No stdin blocking.
- **QR auth** — scan a QR code from another device via `auth-qr`.
- **Clean JSON output** to stdout — easy to parse or pipe into `jq`.
- **Error messages** go to stderr — stdout stays clean for parsing.
- **One call = one action** — no dialogs, prompts, or menus.

## Installation

```bash
go install github.com/strelov1/tg-cli@latest
```

Or build from source:

```bash
git clone https://github.com/strelov1/tg-cli
cd tg-cli
go build -o tg-cli .
```

## Setup

Get your `app_id` and `api_hash` at [my.telegram.org/apps](https://my.telegram.org/apps).

```bash
tg-cli config set app-id 12345
tg-cli config set api-hash abc123def456...
```

## Authorization (agent-friendly)

### Two-step (phone + code)

```bash
# Step 1: send code to phone
tg-cli auth-request +12025551234
# → {"status":"code_sent","phone":"+12025551234"}

# Step 2: complete auth (with or without 2FA)
tg-cli auth-complete +12025551234 --code 12345
tg-cli auth-complete +12025551234 --code 12345 --password MySecret2FA
# → {"status":"authorized","phone":"+12025551234","username":"me",...}
```

### QR code (scan from existing device)

```bash
tg-cli auth-qr
```

Displays a QR code in the terminal. On another device: Telegram → Settings → Devices → Link Desktop Device. Once scanned, the session is saved automatically.

## Commands

### Account info

```bash
tg-cli me
tg-cli status
tg-cli accounts
tg-cli accounts use +12025551234
```

### Messages

```bash
# List dialogs
tg-cli dialogs
tg-cli dialogs --unread
tg-cli dialogs --limit 50
tg-cli dialogs --type channel       # only channels
tg-cli dialogs --archived           # archived dialogs

# Read messages
tg-cli read durov
tg-cli read team-chat --offset 1000
tg-cli read team-chat --since 1h         # last 1 hour
tg-cli read team-chat --since 7d         # last 7 days
tg-cli read team-chat --format text      # human-readable [who]: text output
tg-cli read team-chat --media-only  # only messages with files/photos

# Get a specific message by ID
tg-cli get-message team-chat 12345

# Scan a range of message IDs (channel/supergroup)
tg-cli scan team-chat 100 200

# Send a message
tg-cli send @alice "Hello!"
tg-cli send +12025551234 "Test"

# Schedule a message
tg-cli send team-chat "Meeting in 5 min!" --at "2026-03-15 09:55"

# Send a file
tg-cli send-file @alice ./report.pdf
tg-cli send-file team-chat /tmp/screenshot.png

# Send multiple files as album
tg-cli send-album team-chat ./photo1.jpg ./photo2.jpg

# Reply to a specific message
tg-cli reply team-chat 12345 "Got it!"

# Edit own message
tg-cli edit team-chat 12345 "Updated text"

# Delete messages (one or many)
tg-cli delete team-chat 12345
tg-cli delete team-chat 100 101 102

# Add a reaction
tg-cli react team-chat 12345 👍

# Get reactions on a message
tg-cli reactions team-chat 12345

# Forward a message to another dialog
tg-cli forward team-chat 12345 @alice

# Copy-forward without "Forwarded from" attribution
tg-cli forward-copy team-chat 12345 @alice

# Mark as read
tg-cli mark-read team-chat

# Pin / unpin a message
tg-cli pin team-chat 12345
tg-cli unpin team-chat 12345

# Mute / unmute notifications
tg-cli mute team-chat 1h    # mute for 1 hour (supports: 30m, 1h, 7d, forever)
tg-cli unmute team-chat

# Download media from a message
tg-cli download-media team-chat 12345
tg-cli download-media team-chat 12345 --out /tmp/file.jpg
```

### Groups and channels

```bash
# Search groups
tg-cli search-groups "golang" --limit 10

# Join / leave
tg-cli join @golang_digest
tg-cli join https://t.me/+AbCdEfGhIjK
tg-cli leave golang_digest

# Search messages in a dialog
tg-cli search team-chat "deploy" --limit 20

# Search across all chats
tg-cli search-all "deployment failed" --limit 20

# Get info about a chat or user
tg-cli info @alice
tg-cli info team-chat

# List members
tg-cli members team-chat --limit 50

# List admins
tg-cli admins team-chat

# List forum topics
tg-cli topics team-chat

# Get invite link
tg-cli invite-link team-chat

# Invite a user or bot into a group
tg-cli invite team-chat @alice
tg-cli invite team-chat +12025551234

# Common chats with a user
tg-cli common-chats @alice

# User's profile photos
tg-cli user-photos @alice
tg-cli user-photos @alice --limit 5

# Watch for new messages (polls every 5s, prints JSON per message)
tg-cli watch team-chat
tg-cli watch @alice --interval 10
tg-cli watch team-chat --keyword "deploy"    # filter by keyword
tg-cli watch team-chat --event new,edit      # include edits
```

### Group management

```bash
# List channels/groups where you are admin or owner
tg-cli my-channels
tg-cli my-channels --owned    # only where you are creator

# Kick a user (removes from group, can re-join)
tg-cli kick team-chat @alice

# Ban a user (channel/supergroup only)
tg-cli ban team-chat @alice              # permanent ban
tg-cli ban team-chat @alice --until "2026-04-01 00:00"

# Unban a user
tg-cli unban team-chat @alice

# Promote a user to admin
tg-cli promote team-chat @alice                         # all permissions
tg-cli promote team-chat @alice --perms post,delete     # specific permissions
tg-cli promote team-chat @alice --perms all --rank "Editor"

# Demote a user (remove admin rights)
tg-cli demote team-chat @alice

# Edit group/channel properties
tg-cli set-title team-chat "New Name"
tg-cli set-description team-chat "A great team"
tg-cli set-photo team-chat ./logo.jpg

# Create a new group
tg-cli create-group "My Team" @alice @bob
```

Available `--perms`: `post`, `edit`, `delete`, `ban`, `invite`, `pin`, `add_admins`, `manage`, `anonymous`, `change_info`, `topics`, `all`.

### Contacts

```bash
# List contacts
tg-cli contacts

# Add a contact by phone number
tg-cli contacts add +12025551234 John Doe
```

### Export history

```bash
# Full history → JSON
tg-cli export team-chat > history.json

# Last N messages
tg-cli export team-chat --limit 500 > recent.json
```

Export writes progress to stderr and JSON to stdout:

```json
{
  "dialog": "team-chat",
  "total_messages": 1234,
  "incomplete": false,
  "messages": [
    {
      "id": 42,
      "who": "Alice",
      "when": "2024-01-01T10:00:00Z",
      "text": "Hello!",
      "views": 1200,
      "forwards": 5,
      "reply_to": 41,
      "post_author": "Alice",
      "reactions": [{ "emoji": "👍", "count": 10 }]
    }
  ]
}
```

### Group analytics & moderation

```bash
# Channel/supergroup statistics
tg-cli stats team-chat

# Restrict a user (partial ban)
tg-cli restrict team-chat @alice --no-send --no-media

# Delete all messages from a user in a channel
tg-cli delete-user-messages team-chat @spammer

# Voice message transcription
tg-cli transcribe team-chat 12345

# Create a channel or supergroup
tg-cli create-channel "My Channel"             # broadcast channel
tg-cli create-channel "My Group" --supergroup  # supergroup
tg-cli create-channel "My Channel" --username @mychannel
```

### Account management

```bash
# List active sessions
tg-cli sessions

# Terminate a session
tg-cli sessions revoke 1234567890

# Block / unblock
tg-cli block @spammer
tg-cli unblock @spammer
tg-cli blocked

# Delete conversation history
tg-cli delete-history @alice
tg-cli delete-history @alice --revoke   # delete for both sides

# Archive / unarchive
tg-cli archive team-chat
tg-cli unarchive team-chat
tg-cli dialogs --archived
```

### Member analytics

```bash
# Search members by name or username (no admin rights needed)
tg-cli search-members team-chat "John"

# Export all members to CSV
tg-cli parse-members team-chat --limit 5000 --out members.csv
tg-cli parse-members team-chat --format json

# Who was active in the last N days
tg-cli active-members team-chat --days 30
tg-cli active-members team-chat --days 7 --out active.json
```

### Message tools

```bash
# Generate a t.me link to a specific message
tg-cli message-link team-chat 12345

# Send with rich text formatting
tg-cli send team-chat "<b>Bold</b> and <i>italic</i>" --parse-mode html
tg-cli send team-chat "**bold** and _italic_" --parse-mode markdown
```

### Channels — comments, scheduled, paid invites (v1.7)

```bash
# Link a discussion group to a channel
tg-cli set-discussion my-channel my-discussion-group

# Post a comment under a channel post (resolves to linked group thread)
tg-cli comment my-channel 12345 "Great post!"

# List messages in the scheduled queue
tg-cli list-scheduled team-chat

# Create a Telegram Stars subscription invite link (paid, 30-day cycle)
tg-cli paid-invite-link my-channel --stars 50 --title "VIP access"

# Send an album with caption, spoiler-blur and schedule
tg-cli send-album team-chat photo1.jpg photo2.jpg --caption "Trip" --spoiler --at "2026-06-01 10:00"
```

### Chat folders & bulk dump (v1.7)

```bash
# Preview a chat-folder share link without joining
tg-cli chatlist-preview https://t.me/addlist/abc123

# Join all chats from a folder link (or a subset via --peers)
tg-cli chatlist-join https://t.me/addlist/abc123 --peers @ch1,@ch2

# Dump a single channel to JSON + download media
tg-cli download-channel @news --out ./dump --limit 1000 --resume

# Bulk-dump every channel from a folder link
tg-cli download-network https://t.me/addlist/abc123 --out ./dump --auto-join --resume
```

## Global flags

```bash
tg-cli --account +12025551234 dialogs   # use a specific account
tg-cli --timeout 30 dialogs             # timeout in seconds
```

## Configuration

Config file: `~/.tg-cli/config.json`
Sessions: `~/.tg-cli/sessions/<phone>/session.json`

Environment variables (take priority over config file):

| Variable      | Description            |
| ------------- | ---------------------- |
| `TG_APP_ID`   | App ID                 |
| `TG_API_HASH` | API Hash               |
| `ACCOUNT`     | Default phone number   |
| `SESSION_DIR` | Directory for sessions |

## Agent usage examples

```bash
# Check unread messages
UNREAD=$(tg-cli dialogs --unread | jq '.[0].name' -r)
tg-cli read "$UNREAD" | jq '.messages[-1].text'

# Read only recent messages (last hour)
tg-cli read team-chat --since 1h | jq '.messages[].text'

# Reply to a message
MSG_ID=$(tg-cli read @alice | jq '.messages[-1].id')
tg-cli reply @alice "$MSG_ID" "Got it, on it now"

# Get a specific message by ID
tg-cli get-message team-chat 12345 | jq '.text'

# Send "processing..." then edit to result
MSG=$(tg-cli send team-chat "⏳ Processing...")
tg-cli edit team-chat "$MSG_ID" "✅ Done!"

# React to acknowledge without replying
tg-cli react team-chat 12345 👍

# See what reactions a message got
tg-cli reactions team-chat 12345 | jq '.reactions'

# Forward an important message to another chat (with attribution)
tg-cli forward team-chat 12345 @boss

# Copy-forward without "Forwarded from" label
tg-cli forward-copy team-chat 12345 @boss

# Send a file
tg-cli send-file @alice ./report.pdf

# Download a media file from a message
tg-cli download-media team-chat 12345 --out /tmp/file.jpg

# Find out who's in the group
tg-cli members team-chat | jq '.members[].username'

# Get group admins
tg-cli admins team-chat | jq '.admins[].username'

# Invite someone to a group
tg-cli invite team-chat @newmember

# Get an invite link to share
tg-cli invite-link team-chat | jq '.link'

# List forum topics
tg-cli topics team-chat | jq '.topics[].title'

# Mute a noisy chat for a day
tg-cli mute team-chat 1d

# Pin an important message
tg-cli pin team-chat 12345

# Watch a dialog for new messages (agent event loop)
tg-cli watch team-chat | while read line; do
  TEXT=$(echo "$line" | jq -r '.text')
  echo "New: $TEXT"
done

# Search across all chats for a keyword
tg-cli search-all "urgent" --limit 10 | jq '.results[].text'

# Export and analyze
tg-cli export project-chat --limit 100 | jq '.messages[].text' | grep -i "deploy"

# Read messages in human-readable format
tg-cli read team-chat --since 1h --format text

# Schedule a reminder message
tg-cli send team-chat "Deploy starts now!" --at "2026-03-15 14:00"

# List all channels you manage
tg-cli my-channels | jq '.channels[].title'

# Kick a spammer from a group
tg-cli kick team-chat @spammer

# Ban a user for 7 days
tg-cli ban team-chat @baduser --until "$(date -d '+7 days' '+%Y-%m-%d %H:%M')"

# Promote a moderator
tg-cli promote team-chat @mod --perms delete,ban,pin --rank "Moderator"

# View and manage contacts
tg-cli contacts | jq '.contacts[].username'
tg-cli contacts add +12025551234 John
```

## License

MIT
