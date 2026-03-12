---
name: tg-cli
description: >
  Manage the user's personal Telegram account directly from the command line.
  Use when the user asks to: read Telegram messages or chats, list dialogs or unread messages,
  send a message or file as themselves on Telegram, schedule a message for a future time,
  reply to or edit a specific message, delete messages, add reactions, get reactions on a message,
  forward messages between chats, copy-forward without attribution, search messages inside a chat
  or across all chats, join or leave a group or channel, export full chat history, mark messages
  as read, search for public Telegram groups, get info about a chat or user, list group members
  or admins, list forum topics, get or create an invite link, invite a user into a group,
  find common chats with a user, get a user's profile photos, download media from a message,
  pin or unpin a message, mute or unmute a chat, scan a message ID range, get a specific
  message by ID, watch a dialog for new messages in real time, create a group,
  list channels/groups where the user is admin (my-channels), kick/ban/unban users,
  promote or demote admins, set group title or description or photo,
  list or add contacts.
  This is for the user's personal account (MTProto, not a bot).
  Two-step agent-friendly auth: call auth-request, get the code from the user, then call
  auth-complete — no interactive TTY needed. QR auth also available via auth-qr.
version: 1.4.0
metadata:
  openclaw:
    emoji: '✈️'
    homepage: https://github.com/privateclaw-com/tg-cli
    requires:
      bins:
        - tg-cli
      config:
        - '~/.tg-cli/config.json'
    install:
      - kind: shell
        bins: [tg-cli]
        run: |
          OS=$(uname -s | tr '[:upper:]' '[:lower:]')
          ARCH=$(uname -m)
          case "$ARCH" in
            x86_64) ARCH=amd64 ;;
            arm64|aarch64) ARCH=arm64 ;;
          esac
          BASE="https://github.com/privateclaw-com/tg-cli/releases/latest/download"
          BIN="tg-cli-${OS}-${ARCH}"
          TMP=$(mktemp)
          curl -fsSL "${BASE}/${BIN}" -o "$TMP"
          EXPECTED=$(curl -fsSL "${BASE}/checksums.txt" | grep "${BIN}" | awk '{print $1}')
          if command -v sha256sum >/dev/null 2>&1; then
            ACTUAL=$(sha256sum "$TMP" | awk '{print $1}')
          elif command -v shasum >/dev/null 2>&1; then
            ACTUAL=$(shasum -a 256 "$TMP" | awk '{print $1}')
          else
            echo "Warning: no sha256 tool found, skipping checksum" >&2
            ACTUAL="$EXPECTED"
          fi
          if [ "$EXPECTED" != "$ACTUAL" ]; then
            echo "Checksum verification failed" >&2; rm -f "$TMP"; exit 1
          fi
          install -m 755 "$TMP" /usr/local/bin/tg-cli
          rm -f "$TMP"
---

# tg-cli — Agentic Telegram CLI

Standalone CLI for managing a personal Telegram account via MTProto. No subprocesses, no browser, no interactive prompts. Every command returns JSON on stdout; progress and errors go to stderr.

## Security Notes

- **Verification codes and 2FA passwords** are required only during initial authorization (`auth-request` / `auth-complete`). These are standard Telegram credentials — never share them outside of this auth flow.
- **The install script verifies SHA-256 checksums** against the official release manifest before installing the binary.
- **Source code and release artifacts** are open and auditable at [github.com/privateclaw-com/tg-cli](https://github.com/privateclaw-com/tg-cli).
- You can build from source instead of using the pre-built binary: `go install github.com/privateclaw-com/tg-cli@latest`

---

## Setup (First Run)

### Step 1: Check if configured

```bash
tg-cli config list
```

If `app-id` and `api-hash` are missing — help the user get them from [my.telegram.org/apps](https://my.telegram.org/apps):

```bash
tg-cli config set app-id <id>
tg-cli config set api-hash <hash>
```

### Step 2: Check accounts

```bash
tg-cli accounts
```

If no authorized accounts — start auth (see **Authorization** below).

---

## Authorization

### Two-step (phone + code)

#### Step 1 — Request code

```bash
tg-cli auth-request +12025551234
```

Returns `{"status":"code_sent","phone":"+12025551234"}`. A verification code is sent to the user's Telegram app (or SMS).

Tell the user: **"Check your Telegram — I've sent a code. Please share it with me."**

#### Step 2 — Complete auth

```bash
# Without 2FA
tg-cli auth-complete +12025551234 --code 12345

# With 2FA password
tg-cli auth-complete +12025551234 --code 12345 --password MySecret2FA
```

Returns `{"status":"authorized","phone":"...","username":"..."}`.

If the user has 2FA enabled and you didn't pass `--password`, re-run with it.

### QR code (scan from existing device)

```bash
tg-cli auth-qr
```

Displays a QR code in the terminal. The user opens Telegram on their phone: Settings → Devices → Link Desktop Device, then scans the QR. Once scanned, the session is saved automatically.

---

## Commands

### Account info

```bash
tg-cli me
tg-cli status
tg-cli accounts
tg-cli accounts use +12025551234
```

### List dialogs

```bash
# All dialogs
tg-cli dialogs

# Only unread
tg-cli dialogs --unread

# Limit results
tg-cli dialogs --limit 50
```

Output:

```json
[
  { "id": 123, "name": "Alice", "username": "alice", "type": "user", "unread_count": 3 },
  { "id": 456, "name": "Dev Team", "type": "supergroup", "unread_count": 0 }
]
```

Types: `user`, `group`, `supergroup`, `channel`.

### Read messages

```bash
tg-cli read alice
tg-cli read team-chat
tg-cli read @username
tg-cli read +12025551234
tg-cli read team-chat --offset 1000       # paginate backwards
tg-cli read team-chat --since 1h          # messages from last 1 hour
tg-cli read team-chat --since 30m         # messages from last 30 minutes
tg-cli read team-chat --since 7d          # messages from last 7 days
tg-cli read team-chat --format text       # human-readable: [2024-01-01T10:00:00Z] Alice: Hello
```

Output:

```json
{
  "messages": [
    {
      "id": 1,
      "who": "Alice",
      "when": "2024-01-01T10:00:00Z",
      "text": "Hello",
      "views": 100,
      "forwards": 2,
      "reply_to": 0,
      "reactions": [{ "emoji": "👍", "count": 5 }]
    }
  ],
  "offset": 1
}
```

Use `offset` value from response as `--offset` to load older messages.

### Get a specific message by ID

```bash
tg-cli get-message team-chat 12345
tg-cli get-message @channel 99
```

### Scan a range of message IDs

Fetches all messages (including media-only) in the given ID range. Useful for gap analysis in channels.

```bash
tg-cli scan team-chat 100 200
```

### Send message

```bash
tg-cli send @alice "Hello!"
tg-cli send team-chat "Build is done ✅"
tg-cli send +12025551234 "Hey there"

# Schedule a message (local time)
tg-cli send team-chat "Meeting in 5 min!" --at "2026-03-15 09:55"
```

### Reply to a message

```bash
tg-cli reply team-chat 12345 "Got it, thanks!"
tg-cli reply @alice 99 "Sure, see you then"
```

### Edit a message

```bash
tg-cli edit team-chat 12345 "Updated text here"
```

Only works on your own messages.

### Delete messages

```bash
tg-cli delete team-chat 12345
tg-cli delete team-chat 100 101 102   # delete multiple
```

### React to a message

```bash
tg-cli react team-chat 12345 👍
tg-cli react @alice 99 ❤️
```

### Get reactions on a message

```bash
tg-cli reactions team-chat 12345
```

Output:

```json
{
  "reactions": [
    { "emoji": "👍", "count": 10 },
    { "emoji": "❤️", "count": 3 }
  ]
}
```

### Forward a message

```bash
tg-cli forward team-chat 12345 @alice
tg-cli forward inbox 99 project-chat
```

### Copy-forward without attribution

Sends the message content without the "Forwarded from" header.

```bash
tg-cli forward-copy team-chat 12345 @alice
```

### Send a file

```bash
tg-cli send-file @alice /path/to/report.pdf
tg-cli send-file team-chat ./screenshot.png
```

### Download media from a message

```bash
tg-cli download-media team-chat 12345
tg-cli download-media team-chat 12345 --out /tmp/file.jpg
```

### Mark as read

```bash
tg-cli mark-read team-chat
```

### Pin / unpin a message

```bash
tg-cli pin team-chat 12345
tg-cli unpin team-chat 12345
```

### Mute / unmute notifications

```bash
tg-cli mute team-chat 1h       # mute for 1 hour
tg-cli mute team-chat 7d       # mute for 7 days
tg-cli mute team-chat forever  # mute indefinitely
tg-cli unmute team-chat
```

Duration formats: `30m`, `1h`, `7d`, `forever`.

### Search messages in a dialog

```bash
tg-cli search team-chat "deploy" --limit 20
```

Output:

```json
{"results": [...], "total": 5, "query": "deploy", "dialog": "team-chat"}
```

### Search messages across all chats

```bash
tg-cli search-all "deployment failed" --limit 20
```

Output:

```json
{"results": [...], "total": 3, "query": "deployment failed"}
```

### Get info about a user, group, or channel

```bash
tg-cli info @alice
tg-cli info team-chat
tg-cli info @golang_digest
```

### List group members

```bash
tg-cli members team-chat
tg-cli members @golang_digest --limit 50
```

### List group admins

```bash
tg-cli admins team-chat
```

Output:

```json
{ "admins": [{ "id": 1, "username": "alice", "first_name": "Alice", "role": "creator" }] }
```

### List forum topics

```bash
tg-cli topics team-chat
```

### Get invite link

```bash
tg-cli invite-link team-chat
```

Output:

```json
{ "link": "https://t.me/+AbCdEfGhIjK" }
```

### Invite a user into a group

```bash
tg-cli invite team-chat @alice
tg-cli invite team-chat +12025551234
```

Works for both regular groups and supergroups.

### Common chats with a user

```bash
tg-cli common-chats @alice
```

### User's profile photos

```bash
tg-cli user-photos @alice
tg-cli user-photos @alice --limit 5
```

### Watch for new messages

```bash
tg-cli watch team-chat             # poll every 5s (default)
tg-cli watch @alice --interval 10  # poll every 10s
```

Prints each new message as a JSON object to stdout as it arrives. Runs until Ctrl+C or `--timeout`.

### Search public groups/channels

```bash
tg-cli search-groups "golang" --limit 10
```

### Join a group or channel

```bash
# By username
tg-cli join @golang_digest

# By t.me invite link
tg-cli join https://t.me/+AbCdEfGhIjK
```

### Leave a group or channel

```bash
tg-cli leave golang_digest
tg-cli leave team-chat
```

### My channels / managed groups

List channels and supergroups where the current user is admin or creator:

```bash
# All admin channels
tg-cli my-channels

# Only channels you own (creator)
tg-cli my-channels --owned
```

Output: `{"channels": [{"id": 123, "title": "...", "type": "supergroup", "members": 500, "is_owner": true, "admin_rights": {...}}], "total": 1}`

### Kick / ban / unban

```bash
# Kick user (removed, can re-join) — works for both regular groups and supergroups/channels
tg-cli kick team-chat @alice

# Ban user permanently (channel/supergroup only)
tg-cli ban team-chat @alice

# Ban until a specific date/time
tg-cli ban team-chat @alice --until "2026-04-01 00:00"

# Unban
tg-cli unban team-chat @alice
```

### Promote / demote admins

```bash
# Promote with all standard permissions
tg-cli promote team-chat @alice

# Promote with specific permissions + custom title
tg-cli promote team-chat @alice --perms post,edit,delete,pin --rank "Editor"

# Demote (remove admin rights)
tg-cli demote team-chat @alice
```

Available `--perms` values: `post`, `edit`, `delete`, `ban`, `invite`, `pin`, `add_admins`, `manage`, `anonymous`, `change_info`, `topics`, `all`.

### Edit group/channel properties

```bash
# Rename a group or channel
tg-cli set-title team-chat "Dev Team 2.0"

# Change description
tg-cli set-description team-chat "All things development"

# Set a new photo from a local file
tg-cli set-photo team-chat ./logo.png
```

### Contacts

```bash
# List all contacts
tg-cli contacts

# Add a contact by phone number
tg-cli contacts add +12025551234 John
tg-cli contacts add +12025551234 John Doe
```

### Export full chat history

```bash
# Full export — can take a while for large chats
tg-cli export team-chat > history.json

# Last N messages only
tg-cli export team-chat --limit 500 > recent.json
```

Progress is printed to stderr. Output:

```json
{
  "account": "+12025551234",
  "dialog": "team-chat",
  "total_messages": 1234,
  "incomplete": false,
  "messages": [
    { "id": 1, "who": "Alice", "when": "...", "text": "...", "views": 100, "forwards": 2 }
  ]
}
```

`"incomplete": true` means the export was interrupted (FLOOD_WAIT or timeout) — partial data is returned.

---

## Multiple Accounts

```bash
tg-cli --account +19005551234 dialogs
tg-cli --account +79001234567 send @alice "Hello from my Russian number"
```

Set a default account:

```bash
tg-cli accounts use +12025551234
```

---

## Timeout

```bash
tg-cli --timeout 30 dialogs   # 30-second timeout
```

---

## Config

- Config file: `~/.tg-cli/config.json`
- Sessions: `~/.tg-cli/sessions/<phone>/session.json`

```bash
tg-cli config list
tg-cli config set app-id 12345
tg-cli config set api-hash abc123...
tg-cli config set default-account +12025551234
```

---

## Common Errors

| Error                              | Meaning             | Fix                                                |
| ---------------------------------- | ------------------- | -------------------------------------------------- |
| `app-id and api-hash are required` | Not configured      | `tg-cli config set app-id ...`                     |
| `auth code expired`                | 5-min TTL on code   | Re-run `auth-request`                              |
| `2FA required`                     | User has 2FA        | Re-run `auth-complete` with `--password`           |
| `cannot find "..."`                | Unknown dialog name | Try `@username` format or full name                |
| `session invalid or expired`       | Session gone        | Re-authorize with `auth-request` / `auth-complete` |
