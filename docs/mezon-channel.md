# Mezon channel

GoClaw connects to Mezon with `github.com/dungxbuif/mezon-sdk-go`. The adapter
authenticates a bot, consumes realtime channel messages, and translates them to
the shared `bus.InboundMessage` contract. Agent responses are sent back through
the SDK's channel cache.

## Configure from the dashboard or DB

Create a `channel_instance` with type `mezon`. Credentials are encrypted by the
existing channel-instance store:

```json
{
  "name": "mezon-prod",
  "channel_type": "mezon",
  "agent_id": "<agent UUID>",
  "credentials": {
    "bot_id": "<Mezon application ID>",
    "token": "<Mezon bot token>"
  },
  "config": {
    "dm_policy": "disabled",
    "group_policy": "open",
    "require_mention": true,
    "history_limit": 200,
    "media_max_bytes": 20971520
  },
  "enabled": true
}
```

The Web Dashboard exposes the same fields. `host`, `port`, and `use_ssl` are
advanced overrides for development or self-hosted gateways; production should
use the SDK defaults and TLS verification.

## Configure from file or environment

```json5
{
  channels: {
    mezon: {
      enabled: true,
      bot_id: "<Mezon application ID>",
      token: "<Mezon bot token>",
      dm_policy: "disabled",
      group_policy: "open",
      require_mention: true,
      history_limit: 200,
      media_max_bytes: 20971520
    }
  }
}
```

`GOCLAW_MEZON_BOT_ID` and `GOCLAW_MEZON_TOKEN` override file values and
auto-enable the channel when both are present. The token is masked by config
responses and removed by secret-stripping paths; the bot ID is not secret.

## Message behavior

- Messages from the configured bot ID are ignored to prevent echo loops.
- Clan channels are groups; direct-mode or clan `0` messages are DMs.
- DM/group policies use the common allowlist and pairing service.
- Groups require a bot mention by default. Unmentioned messages are retained in
  pending history and supplied when a later message mentions the bot.
- Replies include the referenced author's name and a bounded copy of the
  referenced text. Topic mentions also backfill up to 25 older server messages,
  in chronological order, so context survives restarts and missed events.
- Inbound attachments are downloaded with a configurable size bound (20 MiB by
  default), preserving MIME type and original filename. Text documents are
  extracted into context; other files remain available to downstream media tools.
- Tenant ID, agent ID, sender/user ID, clan ID, channel ID, and message ID are
  preserved for routing and isolation.
- Ordinary responses carry Mezon-native markdown spans for bold, inline/fenced
  code, and links. Long responses are split before the SDK's 8,000 UTF-16-unit
  wire limit using the real rich payload size.
- Shutdown unregisters the event callback, stops history flushing, cancels login,
  and closes the SDK client exactly once.

For a clan-only production bot, keep `dm_policy: disabled`. The adapter derives
the trusted clan ID from the inbound event. Agent-generated tool arguments cannot
replace it. Replies, sends, and placeholder updates targeting a different clan
are rejected before the SDK sends a request.

## Discover channels and capabilities

On a Mezon clan message, the agent receives the read-only `channel_catalog`
tool. It can list channels in the current clan or resolve one exact channel ID or
name:

```json
{"action":"list","query":"support","include_threads":true,"limit":50}
```

```json
{"action":"resolve","name":"support"}
```

The tool deliberately has no `clan_id`, `guild_id`, or account selector. Scope
comes from the authenticated inbound message. Duplicate exact names produce an
ambiguity error containing safe candidates instead of selecting one
arbitrarily. `refresh: true` reloads the current clan catalog, with concurrent
requests coalesced and repeated refreshes served from cache for five seconds.

Each result includes the clan, category, parent, channel kind, privacy flag, and
an explicit capability list. Mezon capabilities are conservative and marked
with `capability_evidence: "adapter"`. Supported text-like channels advertise
`view`, `send_messages`, `add_reactions`, `edit_own_message`,
`delete_own_message`, and `interactive_messages`. Edit and delete fetch the target from the current channel
and fail closed unless the SDK proves `SenderID` is this bot. Message IDs are
kept as strings end to end so 64-bit Mezon snowflakes are never rounded by JSON.
The adapter does not claim attachments or moderation of other users' messages.

Examples for the generic `message` tool:

```json
{"action":"react","message_id":"2088492982985560042","emoji":"👍"}
```

```json
{"action":"edit","message_id":"2088492982985560042","message":"Updated status"}
```

```json
{"action":"delete","message_id":"2088492982985560042"}
```

These operations always use the current chat from trusted runtime context;
`channel` and `target` arguments are not accepted as overrides for edit,
reaction, or delete. Delete means bot-owned messages only.

## Interactive cards

The Mezon-only `mezon_interactive` tool sends native controls to the current
channel. It cannot accept a destination or clan override. Supported renderers
are button rows and embed fields containing input/textarea, dropdown select,
radio or multi-choice, date picker, and animation components. The SDK currently
defines a grid protocol constant but has no grid builder/wire contract, so the
adapter does not advertise or synthesize grid payloads.

```json
{
  "text": "Choose an action",
  "embed": {
    "title": "Release approval",
    "inputs": [
      {"kind":"select","id":"environment","name":"Environment","options":[{"label":"Production","value":"prod"}]},
      {"kind":"input","id":"note","name":"Note","placeholder":"Optional note","textarea":true},
      {"kind":"date","id":"release_date","name":"Release date"}
    ]
  },
  "button_rows": [[
    {"id":"approve","label":"Approve","style":"success"},
    {"id":"reject","label":"Reject","style":"danger"}
  ]]
}
```

Button-click and dropdown-selection events are converted into ordinary inbound
user turns. The adapter verifies both the event's owner ID and the fetched
source message belong to the current bot before applying group policy. It adds
the original card text and component extra data to the turn, and preserves the
source topic as `topic_id` and `local_key`. Rich cards, processing placeholders,
and final replies therefore remain in the same clan channel and topic, including
after a process restart because source-message state is recovered from the SDK
cache/API. Events from DMs, unknown channels, other bots, or the bot itself are
dropped.

Normal inbound messages and rich component callbacks publish `clan / channel`
as `chat_title` when both names are available, with channel-label fallback.
DB-backed pending group history receives the channel instance tenant before the
flusher starts, so unmentioned clan context is isolated and survives restarts.
Passive memory extraction resolves a topic history key back to its real channel,
category, and parent metadata instead of storing an unnamed Mezon context.

## Cron permissions in clan channels

Cron mutations remain denied by default in group conversations. They are not
Web-UI-only: Mezon operators can manage the channel-scoped allowlist directly:

- Reply to a user's message with `/addcron` to grant cron access.
- Reply to a user's message with `/removecron` to revoke it.
- Send `/croners` to list explicit cron managers. File writers are also shown
  as having implicit cron access.

The first `/addcron` caller bootstraps an empty allowlist. Once any cron manager
or file writer exists, only an existing manager can change it. Permissions use
the exact scope `group:<mezon instance>:<channel id>` and do not apply globally
to the clan or other channels.

## Current limitations

Text and inbound media are production-supported. Outbound HTTPS media URLs are
sent as native Mezon attachments. Local agent files still return
`channels.ErrMediaUnsupported` because the SDK does not yet expose a verified
upload transport; files are never silently dropped.

Mezon's platform and SDK may expose additional APIs, but the bot is instructed
from the adapter capability matrix, not from assumptions about Discord or
Telegram. This prevents the agent from promising operations it cannot execute.

Use a dedicated Mezon bot and clan for canary testing before enabling a production
instance. Never put the token in source control or logs.
