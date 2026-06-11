# Rocket.Chat Message Purger

A small CLI for purging message history from selected Rocket.Chat rooms visible to a personal access token user.

The command defaults to dry-run mode. It does not send destructive cleanup requests unless `--confirm-purge` is present. It also requires an explicit target: use `--room` for one room, or `--all` for every accessible room.

## Safety

This is a destructive admin tool. Start with a dry run, use `--max-messages` while testing message mode, and keep real credentials in environment variables or an untracked local config file. The included `purger.example.json` is only a placeholder template.

## Requirements

- A Rocket.Chat personal access token.
- The matching Rocket.Chat user ID.
- Rocket.Chat permission: `clean-channel-history` for history mode, or `delete-own-message` for message mode.
- Enough room visibility for the token user to see the rooms you want to purge.
- Go 1.26 or newer, only if building from source.

Tested against Rocket.Chat 7.10.

This tool uses:

- `GET /api/v1/rooms.get` to discover rooms.
- `POST /api/v1/rooms.cleanHistory` in `history` mode.
- `GET /api/v1/channels.history`, `GET /api/v1/groups.history`, or `GET /api/v1/im.history` in `messages` mode.
- `POST /api/v1/chat.delete` in `messages` mode.

It cannot bypass Rocket.Chat permissions. To purge an entire workspace, use an admin or service account that can access the target rooms and clean their history.

## Install

Download a prebuilt binary or archive from the [releases page](https://github.com/RXWatcher/rocketchat-message-purger/releases) (macOS arm64, Linux amd64/arm64, with `SHA256SUMS` for verification), or install with Go:

```bash
go install github.com/RXWatcher/rocketchat-message-purger/cmd/rocketchat-message-purger@latest
```

Or build from source:

```bash
go build -o rocketchat-message-purger ./cmd/rocketchat-message-purger
```

## Setup

Copy `.env.example` values into your shell or another local env file:

```bash
export ROCKETCHAT_URL="https://chat.example.com"
export ROCKETCHAT_USER_ID="your-user-id"
export ROCKETCHAT_AUTH_TOKEN="your-personal-access-token"
```

Do not commit real tokens.

## Config File

You can put options in a JSON config file:

```bash
cp purger.example.json purger.json
```

Then edit `purger.json` and run:

```bash
./rocketchat-message-purger --config purger.json
```

`purger.json` is ignored by git. CLI flags override the config file. Environment variables override config-file credentials, so you can keep tokens out of the file:

```bash
export ROCKETCHAT_AUTH_TOKEN="your-personal-access-token"
./rocketchat-message-purger --config purger.json --room general
```

## Dry Run

Dry-run is the default:

```bash
go run ./cmd/rocketchat-message-purger --room general
```

Equivalent explicit form:

```bash
go run ./cmd/rocketchat-message-purger --room general --dry-run
```

The dry run lists rooms that would be purged and rooms skipped by exclusions. It does not call `rooms.cleanHistory` or `chat.delete`.

## Confirmed Purge

Default mode is `history`, which uses Rocket.Chat's room history cleanup API. This removes messages from one room:

```bash
go run ./cmd/rocketchat-message-purger --room general --confirm-purge
```

This removes messages from every accessible room:

```bash
go run ./cmd/rocketchat-message-purger --all --confirm-purge
```

This removes your own messages from every accessible channel and private room while skipping DMs:

```bash
go run ./cmd/rocketchat-message-purger --all --exclude-dms --mode messages --verbose --confirm-purge
```

If `history` mode is blocked by `clean-channel-history` permissions, use message-by-message mode:

```bash
go run ./cmd/rocketchat-message-purger --room general --mode messages --max-messages 10
go run ./cmd/rocketchat-message-purger --room general --mode messages --max-messages 10 --confirm-purge
```

Message mode fetches message IDs first, filters to messages authored by the configured `user_id`, then deletes each own message with `chat.delete` and `asUser: true`. It may require `delete-own-message` and `bypass-time-limit-edit-and-delete` depending on your workspace settings.

You can also pass credentials as flags:

```bash
go run ./cmd/rocketchat-message-purger \
  --url "https://chat.example.com" \
  --user-id "your-user-id" \
  --token "your-personal-access-token" \
  --room general \
  --confirm-purge
```

## Options

```text
--url <url>                 Rocket.Chat base URL, or ROCKETCHAT_URL
--user-id <id>              Rocket.Chat user ID, or ROCKETCHAT_USER_ID
--token <token>             Personal access token, or ROCKETCHAT_AUTH_TOKEN
--config <path>             JSON config file
--room <id-or-name>         Target one room by ID, name, or display name. Repeatable
--all                       Target every accessible room
--mode <history|messages>   Purge mode. Defaults to history
--exclude-dms               Skip direct message rooms when targeting rooms
--page-size <n>             Message history page size for messages mode. Defaults to 100
--max-messages <n>          Maximum messages to delete in messages mode. 0 means no limit
--verbose                   Print each deleted or failed message ID in messages mode
--debug                     Stream chat.delete request/response diagnostics to stderr
--dry-run                   Show what would be purged. This is the default
--confirm-purge             Actually purge room histories
--exclude-room <id-or-name> Skip a room by ID, name, or display name. Repeatable
--include-discussions       Include discussion messages in cleanup
--include-threads           Include thread messages in cleanup
--preserve-pinned           Preserve pinned messages
--concurrency <n>           Room purge concurrency. Defaults to 1
--timeout-ms <n>            HTTP request timeout. Defaults to 30000
```

## Examples

Skip important rooms by ID or name:

```bash
go run ./cmd/rocketchat-message-purger \
  --all \
  --exclude-dms \
  --confirm-purge \
  --exclude-room general \
  --exclude-room announcements
```

Preserve pinned messages:

```bash
go run ./cmd/rocketchat-message-purger --room general --confirm-purge --preserve-pinned
```

Include discussion and thread messages:

```bash
go run ./cmd/rocketchat-message-purger --room general --confirm-purge --include-discussions --include-threads
```

Verbose message deletion output:

```bash
go run ./cmd/rocketchat-message-purger --room general --mode messages --max-messages 10 --verbose --confirm-purge
```

`--verbose` streams scan progress, then `deleting message ...` and `deleted message ...` lines as each message delete runs. `--verbose` only applies to `--mode messages`; the default `history` mode uses Rocket.Chat's room history cleanup endpoint and does not delete one message at a time.

All accessible channels and private rooms, excluding direct messages:

```bash
go run ./cmd/rocketchat-message-purger --all --exclude-dms --mode messages --verbose --confirm-purge
```

Live delete API flow:

```bash
go run ./cmd/rocketchat-message-purger --room general --mode messages --max-messages 10 --debug --confirm-purge
```

## Message Mode Behavior

Message mode works as a find/delete cycle: scan the room history newest-first, delete the first message authored by the configured `user_id`, verify it is gone, then resume scanning from the same page. A delete shifts everything below it up one position, so re-reading the same page catches the message pulled in from the next page without rescanning the whole room — purging is roughly linear in the room's history size.

A message is not counted as deleted until the delete succeeds and a follow-up `chat.getMessage` confirms the message is gone. Deleting a thread parent that still has replies leaves a Rocket.Chat `rm` tombstone with the same message ID; verification counts that as deleted, and later scans skip such tombstones. Any 2xx response that is not valid JSON with `"success": true` (for example a proxy error page) is treated as a failure rather than a successful delete, and only the missing-message response from `chat.getMessage` counts as proof of deletion.

If Rocket.Chat responds with HTTP `429 Too Many Requests`, the client waits for `Retry-After` when provided and retries the request. The `--timeout-ms` budget applies to each HTTP attempt, so a long `Retry-After` wait does not abort the retry.

When `--max-messages` stops a room scan early, the room result is marked with `(stopped at max-messages limit, more of your messages may remain)` so a capped run is not mistaken for a complete purge.

Messages inside threads are only found when `--include-threads` is set. Without it, your thread replies are left in place even when a room reports success. Discussions are separate rooms returned by `rooms.get` and are scanned as rooms of their own; Rocket.Chat 7.x only accepts the `showDiscussion` history parameter on `groups.history`, so the tool sends it only there.

## Scheduling

To keep a workspace clean, run the purge on a schedule. On macOS use a launchd agent, which also catches up runs missed while the machine was asleep. Save this as `~/Library/LaunchAgents/com.example.rocketchat-purger.plist` (adjust paths), then load it with `launchctl bootstrap gui/$UID ~/Library/LaunchAgents/com.example.rocketchat-purger.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.example.rocketchat-purger</string>
	<key>ProgramArguments</key>
	<array>
		<string>/path/to/rocketchat-message-purger</string>
		<string>--config</string>
		<string>/path/to/purger.json</string>
		<string>--confirm-purge</string>
		<string>--verbose</string>
	</array>
	<key>StartCalendarInterval</key>
	<dict>
		<key>Hour</key>
		<integer>4</integer>
		<key>Minute</key>
		<integer>0</integer>
	</dict>
	<key>StandardOutPath</key>
	<string>/tmp/rocketchat-purger.log</string>
	<key>StandardErrorPath</key>
	<string>/tmp/rocketchat-purger.log</string>
</dict>
</plist>
```

On Linux, a cron entry works the same way:

```cron
0 4 * * * /path/to/rocketchat-message-purger --config /path/to/purger.json --confirm-purge >> /var/log/rocketchat-purger.log 2>&1
```

The first scheduled run after enabling this clears any backlog; later runs only find recent messages near the top of each room's history and finish in minutes.

## Exit Codes

- `0`: dry-run completed or confirmed purge completed with no room failures.
- `1`: network/API failure, room listing failure, or at least one room failed during confirmed purge.
- `2`: invalid configuration or CLI usage.

## Development

```bash
go test ./...
go build -o rocketchat-message-purger ./cmd/rocketchat-message-purger
```
