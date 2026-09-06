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
    "dm_policy": "pairing",
    "group_policy": "pairing",
    "require_mention": true,
    "history_limit": 200
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
      dm_policy: "pairing",
      group_policy: "pairing",
      require_mention: true,
      history_limit: 200
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
- Tenant ID, agent ID, sender/user ID, clan ID, channel ID, and message ID are
  preserved for routing and isolation.
- Long Markdown responses are split before the SDK's 8,000 UTF-16-unit limit.
- Shutdown unregisters the event callback, stops history flushing, cancels login,
  and closes the SDK client exactly once.

## Current limitation

Text is production-supported. GoClaw does not advertise Mezon as media-capable
because the current SDK has attachment metadata but no complete upload transport
for local agent files. A media send returns `channels.ErrMediaUnsupported`, so
existing API fallback policy can reject it or degrade explicitly instead of
silently dropping files.

Use a dedicated Mezon bot and clan for canary testing before enabling a production
instance. Never put the token in source control or logs.
