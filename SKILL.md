---
name: tg-cli
description: >
  Manage the user's personal Telegram account directly from the command line.
  Use when the user asks to: read Telegram messages or chats, list dialogs or unread messages,
  send a message as themselves on Telegram, search messages inside a chat, join or leave a
  group or channel, export full chat history, mark messages as read, or search for public
  Telegram groups. This is for the user's personal account (MTProto, not a bot).
  Two-step agent-friendly auth: call auth-request, get the code from the user, then call
  auth-complete — no interactive TTY needed.
version: 1.0.0
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
      - kind: go
        package: github.com/privateclaw-com/tg-cli@latest
        bins: [tg-cli]
---

# tg-cli — Agentic Telegram CLI

Standalone CLI for managing a personal Telegram account via MTProto. No subprocesses, no browser, no interactive prompts. Every command returns JSON on stdout; progress and errors go to stderr.

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

## Authorization (Two Steps, No stdin)

### Step 1 — Request code

```bash
tg-cli auth-request +12025551234
```

Returns `{"status":"code_sent","phone":"+12025551234"}`. A verification code is sent to the user's Telegram app (or SMS).

Tell the user: **"Check your Telegram — I've sent a code. Please share it with me."**

### Step 2 — Complete auth

```bash
# Without 2FA
tg-cli auth-complete +12025551234 --code 12345

# With 2FA password
tg-cli auth-complete +12025551234 --code 12345 --password MySecret2FA
```

Returns `{"status":"authorized","phone":"...","username":"..."}`.

If the user has 2FA enabled and you didn't pass `--password`, re-run with it.

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
  {"id": 123, "name": "Alice", "username": "alice", "type": "user", "unread_count": 3},
  {"id": 456, "name": "Dev Team", "type": "supergroup", "unread_count": 0}
]
```

Types: `user`, `group`, `supergroup`, `channel`.

### Read messages

```bash
tg-cli read alice
tg-cli read team-chat
tg-cli read @username
tg-cli read +12025551234
tg-cli read team-chat --offset 1000   # paginate backwards
```

Output:
```json
{"messages": [{"id": 1, "who": "Alice", "when": "2024-01-01T10:00:00Z", "text": "Hello"}], "offset": 1}
```

Use `offset` value from response as `--offset` to load older messages.

### Send message

```bash
tg-cli send @alice "Hello!"
tg-cli send team-chat "Build is done ✅"
tg-cli send +12025551234 "Hey there"
```

### Mark as read

```bash
tg-cli mark-read team-chat
```

### Search messages in a dialog

```bash
tg-cli search team-chat "deploy" --limit 20
```

Output:
```json
{"results": [...], "total": 5, "query": "deploy", "dialog": "team-chat"}
```

### Search public groups/channels

```bash
tg-cli search-groups "golang" --limit 10
```

Output:
```json
{"results": [{"id": 1, "title": "Golang", "username": "golang", "type": "supergroup", "members": 50000}], "total": 1}
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

Works for channels, supergroups, and regular groups.

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
  "messages": [{"id": 1, "who": "Alice", "when": "...", "text": "..."}]
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

| Error | Meaning | Fix |
|-------|---------|-----|
| `app-id and api-hash are required` | Not configured | `tg-cli config set app-id ...` |
| `auth code expired` | 5-min TTL on code | Re-run `auth-request` |
| `2FA required` | User has 2FA | Re-run `auth-complete` with `--password` |
| `cannot find "..."` | Unknown dialog name | Try `@username` format or full name |
| `session invalid or expired` | Session gone | Re-authorize with `auth-request` / `auth-complete` |
