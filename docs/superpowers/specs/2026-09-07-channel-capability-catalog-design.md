# Channel Capability Catalog Design

**Date:** 2026-09-07  
**Status:** Proposed  
**Scope:** GoClaw channel abstraction, Discord adapter, Mezon adapter, agent tools, tests, and operator documentation

## Problem

GoClaw can already receive and send messages through Discord and Mezon, but the
agent cannot discover the channels visible to its connected bot account. This
causes incorrect responses such as claiming that it has no way to list channels
and asking the user to supply a name or ID that the adapter could have resolved.

The capability gap is in GoClaw rather than in either platform SDK:

- The Discord adapter already calls `GuildChannels` during contact refresh and
  keeps guild channels and threads in session state.
- `mezon-sdk-go` exposes the client's clan cache, `Clan.LoadChannels`,
  `Clan.ReloadChannels`, and the per-clan channel cache.
- The Mezon SDK message model supports reply, update, react, and delete, while
  the GoClaw Mezon adapter currently exposes only send and update internally.
- GoClaw's only inventory tool is the platform-specific
  `zalo_list_groups`. `sessions_list` lists conversations already observed by
  GoClaw; it is not an authoritative platform inventory.

The design must solve discovery without teaching the model to infer permissions
or invent channel IDs.

## Goals

1. Give agents a generic `channel_catalog` tool on Discord and Mezon.
2. List only channels visible inside the clan/guild of the current conversation.
3. Resolve human names to stable IDs deterministically and reject ambiguity.
4. Report capabilities from adapter support and platform permission evidence,
   never from assumptions in the prompt.
5. Reuse cached platform state and avoid a REST request on every model tool call.
6. Keep tenant, channel-instance, and current-clan/guild boundaries intact.
7. Establish an extensible foundation for thread discovery and message
   edit/react/delete support.

## Non-goals

- Per-channel fixed prompts or per-channel memory policy. Session isolation is a
  separate concern and is explicitly outside this change.
- Channel, role, or permission administration.
- Listing members, audit logs, hidden channels, or secret configuration.
- A universal abstraction for every GoClaw channel platform in the first
  release.
- Claiming that Mezon and Discord have identical permission semantics.

## Source Evidence

The platform design is based on primary sources and checked against the code
pinned in this workspace:

- Discord's Get Guild Channels endpoint returns guild channels but excludes
  threads. Discord documents `VIEW_CHANNEL`, `SEND_MESSAGES`,
  `READ_MESSAGE_HISTORY`, `ADD_REACTIONS`, `MANAGE_MESSAGES`, attachment, poll,
  and thread permissions as distinct permission bits.
  <https://docs.discord.com/developers/resources/guild#get-guild-channels>
  <https://docs.discord.com/developers/topics/permissions>
- Mezon describes clan channels, DMs, and threads as `TextChannel` variants,
  managed by cached channel managers, and documents reply, update, delete, and
  react on messages.
  <https://mezon.ai/docs/developer/mezon-sdk/integration-bot-sdk/core-concepts/>
- Mezon documents interactive inputs, selects, radio choices, buttons, and
  associated interaction events. These are platform features, but they should
  only be advertised by GoClaw after an agent-facing tool implements them.
  <https://mezon.ai/docs/developer/mezon-topics/interactive-message/>

## Considered Approaches

### A. Generic provider and generic tool — selected

Add a small catalog interface to the channel abstraction, implement it in the
Discord and Mezon adapters, and expose one `channel_catalog` tool. Resolution is
centralized so both adapters have identical ambiguity and normalization rules.

Advantages:

- one stable tool contract for the model;
- platform-specific collection and permission logic remain inside adapters;
- straightforward to add another platform;
- capability claims can be tested independently from prompts.

Trade-off: the shared entry model must preserve platform differences instead of
flattening all channel features to booleans.

### B. Separate `discord_list_channels` and `mezon_list_channels` tools

This is easy initially but duplicates schemas, resolution behavior, wiring,
tests, and model instructions. It also scales poorly as more platforms gain
inventory support.

### C. Derive inventory from contacts and sessions

This avoids platform requests but only sees channels where GoClaw has observed
activity. It cannot answer authoritative inventory questions and can retain
stale names or deleted channels. Contacts remain useful presentation metadata,
not the source of truth.

## Domain Model

Add shared types under `internal/channels`:

```go
type ChannelCapability string

const (
    CapabilityView           ChannelCapability = "view"
    CapabilitySendMessages   ChannelCapability = "send_messages"
    CapabilityReadHistory    ChannelCapability = "read_history"
    CapabilityAttachFiles    ChannelCapability = "attach_files"
    CapabilityAddReactions   ChannelCapability = "add_reactions"
    CapabilityEditOwnMessage ChannelCapability = "edit_own_message"
    CapabilityDeleteOwn      ChannelCapability = "delete_own_message"
    CapabilityManageMessages ChannelCapability = "manage_messages"
    CapabilityCreateThreads  ChannelCapability = "create_threads"
    CapabilitySendInThreads  ChannelCapability = "send_in_threads"
    CapabilityInteractive    ChannelCapability = "interactive_messages"
)

type ChannelCatalogEntry struct {
    Platform     string              `json:"platform"`
    Instance     string              `json:"instance"`
    ContainerID  string              `json:"container_id"`
    Container    string              `json:"container_name"`
    CategoryID   string              `json:"category_id,omitempty"`
    Category     string              `json:"category_name,omitempty"`
    ChannelID    string              `json:"channel_id"`
    Name         string              `json:"name"`
    Kind         string              `json:"kind"`
    ParentID     string              `json:"parent_id,omitempty"`
    Private      bool                `json:"private,omitempty"`
    Capabilities []ChannelCapability `json:"capabilities"`
    Evidence     string              `json:"capability_evidence"`
}

type ChannelCatalogOptions struct {
    // ScopeID is trusted runtime context: clan ID on Mezon, guild ID on Discord.
    // It is never accepted from model arguments.
    ScopeID         string
    IncludeThreads bool
    Refresh        bool
}

type ChannelCatalogProvider interface {
    ListChannelCatalog(ctx context.Context, opts ChannelCatalogOptions) ([]ChannelCatalogEntry, error)
}
```

`Container` means guild on Discord and clan on Mezon. The neutral name keeps the
tool contract stable while the JSON result can also include `container_kind` to
make output understandable to humans.

`Evidence` is an enum-like value, not arbitrary prose:

- `permission`: computed from current platform permissions;
- `visibility`: the platform returned a visible channel but detailed
  permissions are unavailable;
- `adapter`: the adapter implements the operation and the platform API does not
  expose a reliable preflight permission;
- `unknown`: capability must not be advertised.

Capabilities are positive grants only. Absence means unsupported or not proven.
The tool must never turn `unknown` into `true`.

## Provider Behavior

### Discord

1. Snapshot guilds, channels, and known active threads from `discordgo.State`
   while holding its read lock for the shortest possible duration.
2. Select only the guild whose ID came from the inbound run context. On an
   empty/missing guild cache, or explicit `refresh: true`, call `GuildChannels`
   for that guild. Threads are fetched separately because Discord's guild
   channel endpoint excludes them.
3. Compute permissions for the bot member per channel, including channel
   overwrites. Filter out entries without `VIEW_CHANNEL` even before Discord's
   announced server-side channel-obfuscation behavior becomes universal.
4. Translate channel types to stable kinds: `category`, `text`, `voice`,
   `announcement`, `forum`, `media`, `thread_public`, `thread_private`, and
   `thread_announcement`. Unknown values use `unknown:<numeric-value>`.
5. Derive capabilities from permission bits and adapter support. For example,
   `SEND_MESSAGES` does not imply `SEND_MESSAGES_IN_THREADS` and
   `MANAGE_MESSAGES` is distinct from deleting the bot's own message.

Discord's state and permission helpers are concurrency-safe only when used
according to discordgo's state locking rules. Provider tests must run with
`-race`.

### Mezon

1. Extend the private `sdkClient` adapter interface with a catalog method rather
   than leaking the full SDK client into GoClaw's channel abstraction.
2. Fetch only the clan whose ID came from the inbound run context. Call
   `Clan.LoadChannels()` for an unloaded clan; use `ReloadChannels()` only for
   explicit refresh after the refresh limiter permits it. Never iterate other
   clans for an agent-originated catalog request.
3. Snapshot each `Clan.Channels.Values()` and map `TextChannel` fields:
   `ID`, `Name`, `ChannelType`, `CategoryID`, `CategoryName`, `ParentID`, and
   `IsPrivate`.
4. The fact that a channel appears in the authenticated clan list proves
   visibility. It does not by itself prove send, history, or moderation
   permission. Until Mezon permission evaluation is implemented and tested,
   advertise only adapter-backed operations known to work for this bot session,
   with `adapter` evidence, and surface permission-sensitive capabilities as
   absent.
5. Preserve Mezon's actual channel type integer when no stable semantic mapping
   exists. Do not label every `TextChannel` as a normal text channel because the
   SDK uses the type for channels, DMs, voice, and threads.

The existing Mezon SDK cache is insertion ordered and provides snapshot copies
through `Values()`, so enumeration does not require a new SDK API.

## Manager and Tool API

Add a manager delegate:

```go
func (m *Manager) ListChannelCatalog(
    ctx context.Context,
    channelName string,
    opts ChannelCatalogOptions,
) ([]ChannelCatalogEntry, error)
```

The current channel instance comes exclusively from `ToolChannelFromCtx`. The
current clan/guild ID comes from authenticated inbound metadata propagated
through `RunRequest` into typed tool context. The model can select neither
value. This preserves tenant, credential, and conversation-container boundaries.

Register `channel_catalog` with channel type requirements `discord` and `mezon`.
Its schema has two read-only actions:

### `list`

Parameters:

- `action: "list"` (default)
- `include_threads` optional, default `false`
- `refresh` optional, default `false`
- `query` optional local filter over channel/category names
- `limit` optional, bounded to prevent oversized model context

The response includes count, truncation state, source freshness, and entries.
Results sort by container, category, platform position when available, then name
and ID. IDs remain strings to avoid JSON integer precision loss.

### `resolve`

Parameters:

- `action: "resolve"`
- `name` required
- `kind` optional
- `include_threads` optional

Resolution order:

1. exact channel ID;
2. exact case-insensitive name;
3. exact normalized name after trimming and collapsing whitespace;
4. no fuzzy guessing.

One match returns the entry. Zero matches returns a structured `not_found`
error. Multiple matches returns `ambiguous` with all candidates and requires an
ID or a more specific name. The model must never silently pick the first match,
and candidates outside the current clan/guild never participate.

## Same-clan Reply Boundary

For Mezon, catalog isolation alone is insufficient because the generic message
tool currently accepts a raw channel ID. The runtime therefore enforces the
same-clan invariant again at delivery time:

1. The inbound handler already records `clan_id`. The gateway propagates it as
   a typed `ConversationContainerID` on `RunRequest` and `store.RunContext`.
2. Tool context exposes a private accessor for the trusted value. There is no
   `clan_id` field in the model-facing tool schema.
3. The generic message tool copies the trusted source clan into outbound
   metadata. Final assistant replies retain the original inbound metadata.
4. Before any Mezon send, reply, or update, the adapter fetches the destination
   channel and verifies `channel.Clan.ID == source_clan_id`. A mismatch fails
   closed before network delivery.
5. If a user-originated Mezon group run has no trusted clan scope, catalog and
   cross-target message operations fail closed. Normal reply to the current
   inbound channel may only proceed after its fetched channel proves a nonzero
   clan ID.
6. Mezon direct messages are disabled in the production channel configuration,
   satisfying the requested policy that this bot replies only inside a clan.
   The reusable open-source adapter keeps the existing explicit DM policy rather
   than hard-coding a global ban for every downstream user.

This is defense in depth: model instructions, name resolution, and tool schema
are usability controls; the adapter's destination-clan comparison is the actual
security boundary.

## Capability Awareness

The tool itself is the first source of truth: when available, its description
states that the bot can inspect channel inventory and resolve channel names.
This fixes the observed failure without injecting a long static capability list
into every system prompt.

A later shared `ChannelCapabilityProvider` may expose current-channel operations
to the message tool. That provider should drive both JSON schema and help text so
the model only sees actions the current adapter implements. It must not be a
hand-maintained prompt paragraph.

Initial capability matrix discovered during this design:

| Operation | Discord adapter | Mezon SDK | Mezon adapter | First release |
|---|---:|---:|---:|---:|
| List channels | internal data exists | yes | no | yes |
| Resolve channel name | no generic tool | data exists | no | yes |
| List threads | partial state/REST support | channel types/API | no | optional flag |
| Send message | yes | yes | yes | report when proven |
| Reply to user message | platform support | yes | yes, recently added | report |
| Edit own message | platform support | yes | internal placeholder only | follow-up |
| React | platform support | yes | no | follow-up |
| Delete own message | platform support | yes | no | follow-up |
| Delete others' message | permission-gated | permission-gated | no | excluded |
| Interactive components | platform support | yes | partial GoClaw support | follow-up |
| Polls/pins/search | platform support | SDK API exists | no | follow-up |

This distinction is intentional: SDK capability is not agent capability until a
GoClaw tool exposes and tests it.

## Cache, Refresh, and Failure Handling

- Normal calls read adapter caches.
- Explicit refresh is protected by a per-instance singleflight and cooldown.
- A refresh failure returns a structured error and does not replace a healthy
  cached snapshot with an empty list.
- Context cancellation is checked before network refresh and during multi-guild
  collection.
- Failure of the current container is an error; data from another container is
  never used as fallback.
- Output has a conservative default limit and a hard maximum.
- Secrets, tokens, user IDs, role IDs, and raw permission structures are never
  included.

## Security and Authorization

1. Inventory is scoped to the active GoClaw channel instance, tenant, and the
   clan/guild ID authenticated by the inbound event.
2. Discord channels without computed `VIEW_CHANNEL` are filtered locally.
3. Mezon returns only channels supplied to the authenticated client's clan
   cache/API; private status is metadata, not proof of broader access.
4. Resolution and message delivery cannot cross channel instances or the
   current clan/guild.
5. Catalog operations are read-only. No create/delete/permission operation is
   bundled into this tool.
6. The tool returns stable IDs only as data; actual send/edit/delete operations
   continue through their own policy and channel checks.

## Testing Strategy

Implementation follows test-first development.

### Shared and manager tests

- delegates to a supporting channel;
- rejects an unknown instance;
- rejects an unsupported channel type;
- preserves context cancellation;
- output uses string IDs and deterministic ordering.

### Tool tests

- visibility by `RequiredChannelTypes`;
- list with filters, limit, and truncation;
- exact ID and exact normalized-name resolution;
- duplicate names produce structured ambiguity;
- no fuzzy or first-result fallback;
- current channel instance is always taken from context;
- current clan/guild is always taken from context and cannot be overridden by
  tool arguments;
- provider, refresh, cancellation, and partial errors are safe and useful.

### Discord tests

- state-only inventory makes no REST call;
- refresh calls the expected endpoints;
- hidden channels are filtered;
- categories and parent links are preserved;
- threads require `include_threads`;
- permission bits map independently;
- state access passes `go test -race`.

### Mezon tests

- fake SDK client returns clans, categories, channels, and threads;
- initial list loads uncached channels once;
- normal repeated list does not reload;
- explicit refresh observes changes and honors cancellation/cooldown;
- private, category, parent, and type fields survive mapping;
- another cached clan is never returned or accepted as a message destination;
- the provider does not claim unsupported permission-sensitive operations.

### Regression verification

- existing Mezon reply behavior remains intact;
- existing Discord contact refresh remains intact;
- all channel, tool, gateway wiring, and full repository tests pass;
- race tests run for touched packages;
- production smoke test asks the bot to list and resolve channels without
  sending messages to unrelated channels.
- production smoke test confirms a destination from another clan is rejected
  before network delivery and Mezon DMs are disabled.

## Rollout and Documentation

1. Ship `channel_catalog` behind normal channel-type tool filtering; no config
   migration is required.
2. Document examples for listing, filtering, resolving duplicate names, and
   reading capability evidence in `docs/mezon-channel.md` and the general tools
   documentation.
3. Add an unreleased changelog entry describing behavior and security scope.
4. Deploy the pinned SDK revision, restart the single production service, and
   verify process uniqueness and health.
5. Run a read-only live catalog smoke test. Sending to a newly resolved channel
   requires an explicit user request and remains a separate tool action.

## Follow-up Phases

1. Extend the generic message tool with adapter-backed edit, react, and delete
   operations. Deleting another user's message must require explicit permission
   evidence and policy authorization.
2. Add Mezon permission evaluation once the server permission model and bot role
   responses are covered by fixtures and integration tests.
3. Expose interactive-message builders through a generic component schema with
   exhaustive combination tests.
4. Add pins, polls, history search, and thread management as narrowly scoped
   tools instead of one broad administrative tool.
5. Generalize `zalo_list_groups` onto the catalog contract only after preserving
   its member-count and personal-account semantics.

## Acceptance Criteria

- In a Discord or Mezon conversation, the agent has a visible
  `channel_catalog` tool.
- Asking “liệt kê các channel” returns visible channel names and IDs from the
  current clan/guild only.
- Asking for a duplicate channel name does not select a target silently.
- Hidden Discord channels are absent.
- The response distinguishes proven permissions from adapter/API support.
- The model no longer claims it cannot list channels while the tool is present.
- A Mezon tool call cannot send, reply, or update outside the source clan, even
  when given a valid channel ID from another clan.
- The production Mezon bot does not respond to direct messages.
- Existing messaging behavior and tests remain green.
