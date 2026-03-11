# tg-cli

**Agentic-native Telegram CLI** — control Telegram from the command line with no interactive input required. Designed for AI agents: every command is a single call, output is JSON on stdout.

Built on [gotd/td](https://github.com/gotd/td) (MTProto, Go). No external binaries needed.

---

## Why agentic-native?

Standard Telegram CLIs require an interactive TTY (entering codes, passwords). `tg-cli` solves this:

- **Two-step auth** — the agent calls `auth-request`, waits for the code, then calls `auth-complete`. No stdin blocking.
- **Clean JSON output** to stdout — easy to parse or pipe into `jq`.
- **Error messages** go to stderr — stdout stays clean for parsing.
- **One call = one action** — no dialogs, prompts, or menus.

## Installation

```bash
go install github.com/privateclaw-com/tg-cli@latest
```

Or build from source:

```bash
git clone https://github.com/privateclaw-com/tg-cli
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

```bash
# Step 1: send code to phone
tg-cli auth-request +12025551234
# → {"status":"code_sent","phone":"+12025551234"}

# Step 2: complete auth (with or without 2FA)
tg-cli auth-complete +12025551234 --code 12345
tg-cli auth-complete +12025551234 --code 12345 --password MySecret2FA
# → {"status":"authorized","phone":"+12025551234","username":"me",...}
```

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

# Read messages
tg-cli read durov
tg-cli read team-chat --offset 1000
tg-cli read team-chat --since 1h     # last 1 hour
tg-cli read team-chat --since 7d     # last 7 days

# Send a message
tg-cli send @alice "Hello!"
tg-cli send +12025551234 "Test"

# Send a file
tg-cli send-file @alice ./report.pdf
tg-cli send-file team-chat /tmp/screenshot.png

# Reply to a specific message
tg-cli reply team-chat 12345 "Got it!"

# Edit own message
tg-cli edit team-chat 12345 "Updated text"

# Delete messages (one or many)
tg-cli delete team-chat 12345
tg-cli delete team-chat 100 101 102

# Add a reaction
tg-cli react team-chat 12345 👍

# Forward a message to another dialog
tg-cli forward team-chat 12345 @alice

# Mark as read
tg-cli mark-read team-chat
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

# Watch for new messages (polls every 5s, prints JSON per message)
tg-cli watch team-chat
tg-cli watch @alice --interval 10
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
  "messages": [...]
}
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

| Variable | Description |
|----------|-------------|
| `TG_APP_ID` | App ID |
| `TG_API_HASH` | API Hash |
| `ACCOUNT` | Default phone number |
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

# Send "processing..." then edit to result
MSG=$(tg-cli send team-chat "⏳ Processing...")
tg-cli edit team-chat "$MSG_ID" "✅ Done!"

# React to acknowledge without replying
tg-cli react team-chat 12345 👍

# Forward an important message to another chat
tg-cli forward team-chat 12345 @boss

# Send a file
tg-cli send-file @alice ./report.pdf

# Find out who's in the group
tg-cli members team-chat | jq '.members[].username'

# Watch a dialog for new messages (agent event loop)
tg-cli watch team-chat | while read line; do
  TEXT=$(echo "$line" | jq -r '.text')
  echo "New: $TEXT"
done

# Search across all chats for a keyword
tg-cli search-all "urgent" --limit 10 | jq '.results[].text'

# Export and analyze
tg-cli export project-chat --limit 100 | jq '.messages[].text' | grep -i "deploy"
```

## License

MIT
