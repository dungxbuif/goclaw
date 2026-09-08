# Mezon Channel Context and Rich Message Parity

## Goal

Bring GoClaw's Mezon adapter to the same practical context and presentation quality as its Discord adapter without changing shared agent/provider behavior.

## Scope

- Enrich inbound Mezon messages with reply context, topic history, attachments, and stable clan/channel metadata.
- Emit native Mezon markdown spans for ordinary bot responses.
- Deliver URL-based outbound media through Mezon attachments.
- Resolve Mezon channel context for memory extraction.
- Keep interactive cards/buttons explicit through the existing `mezon_interactive` tool.
- Add only the missing history-list primitive to `mezon-sdk-go`.

Out of scope: changes to Discord/Telegram behavior, agent-loop prompt construction, provider routing, cron security policy, or automatic conversion of every response into an interactive card.

## Design

### Inbound context

The adapter derives the current message text as before, then prepends a bounded reply block when `References` or `ReferencedMessage` contains the replied-to message. In a Mezon topic, the adapter asks the SDK for up to 25 older messages before the current message, orders them chronologically, excludes the bot/current message, and adds a bounded context block. Failure to fetch history is non-fatal.

Attachments are downloaded with time and size limits. Their MIME type and original filename are retained in `bus.MediaFile`, while a compact media tag is added to textual context. This follows the Discord adapter's safety limits and keeps downstream media handling provider-independent.

### Outbound presentation

Normal text is sent as Mezon `ChannelMessageContent` containing plain `t` text and native `mk` spans. Markdown delimiters are removed, while spans annotate fenced code, inline code, bold, and links using end-exclusive UTF-16 offsets, matching Mezon's renderer contract. Chunk sizing is calculated from the actual rich payload.

URL media is sent in `SendOptions.Attachments`. Local-file upload is not hidden behind an unsafe ad-hoc HTTP implementation; it remains unsupported until the SDK exposes a verified upload primitive.

### Channel and memory identity

The stable session key remains `channelID:thread:topicID`. Metadata includes clan, channel and category names available on the event/cache. A Mezon memory context resolver maps a channel to its clan and display labels without changing the shared extraction contract.

### Capability reporting

The catalog stays conservative: it reports adapter-supported operations but does not claim server-side permission checks that Mezon does not expose reliably. Clan validation remains mandatory for cross-target delivery.

## Failure behavior

History and attachment enrichment degrade to the current text-only path and log a warning. Send, clan validation and ownership failures remain hard errors. Reply/interaction source ownership checks remain unchanged.

## Testing

Unit tests cover reply extraction, topic backfill ordering/filtering, attachment limits and metadata, rich payload spans/chunk limits, URL media delivery, memory context, and all existing clan/ownership boundaries. Package tests and the race detector are the release gate.
