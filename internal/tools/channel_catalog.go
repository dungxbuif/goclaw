package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

type ChannelCatalogLister func(context.Context, string, channels.ChannelCatalogOptions) ([]channels.ChannelCatalogEntry, error)

type ChannelCatalogListerAware interface {
	SetChannelCatalogLister(ChannelCatalogLister)
}

type ChannelCatalogTool struct {
	lister ChannelCatalogLister
}

func NewChannelCatalogTool() *ChannelCatalogTool { return &ChannelCatalogTool{} }

func (t *ChannelCatalogTool) SetChannelCatalogLister(lister ChannelCatalogLister) {
	t.lister = lister
}

func (t *ChannelCatalogTool) RequiredChannelTypes() []string {
	return []string{channels.TypeDiscord, channels.TypeMezon}
}

func (t *ChannelCatalogTool) Name() string { return "channel_catalog" }

func (t *ChannelCatalogTool) Description() string {
	return "List channels visible to this bot in the current Discord guild or Mezon clan, or resolve an exact channel name to its ID. Use this before claiming channels cannot be listed and before sending to a channel named by a user. Scope is fixed by the current conversation and cannot cross guilds or clans."
}

func (t *ChannelCatalogTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":          map[string]any{"type": "string", "enum": []string{"list", "resolve"}, "default": "list"},
			"name":            map[string]any{"type": "string", "description": "Exact channel ID or name; required for resolve."},
			"kind":            map[string]any{"type": "string", "description": "Optional exact channel kind for resolve."},
			"query":           map[string]any{"type": "string", "description": "Optional case-insensitive list filter over channel and category names."},
			"include_threads": map[string]any{"type": "boolean", "default": false},
			"refresh":         map[string]any{"type": "boolean", "default": false},
			"limit":           map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "default": 100},
		},
	}
}

func (t *ChannelCatalogTool) Execute(ctx context.Context, args map[string]any) *Result {
	if t.lister == nil {
		return ErrorResult("channel_catalog: no channel catalog provider available")
	}
	channel := ToolChannelFromCtx(ctx)
	scopeID := ToolContainerIDFromCtx(ctx)
	if channel == "" || scopeID == "" {
		return ErrorResult("channel_catalog requires the current channel and trusted clan/guild context")
	}
	entries, err := t.lister(ctx, channel, channels.ChannelCatalogOptions{
		ScopeID:        scopeID,
		IncludeThreads: argBool(args, "include_threads"),
		Refresh:        argBool(args, "refresh"),
	})
	if err != nil {
		return ErrorResult(fmt.Sprintf("channel_catalog failed: %v", err))
	}
	channels.SortChannelCatalog(entries)
	action := strings.ToLower(strings.TrimSpace(argString(args, "action")))
	if action == "" || action == "list" {
		return channelCatalogList(entries, scopeID, args)
	}
	if action == "resolve" {
		return channelCatalogResolve(entries, scopeID, args)
	}
	return ErrorResult(fmt.Sprintf("unsupported channel_catalog action %q", action))
}

func channelCatalogList(entries []channels.ChannelCatalogEntry, scopeID string, args map[string]any) *Result {
	query := strings.ToLower(strings.TrimSpace(argString(args, "query")))
	filtered := make([]channels.ChannelCatalogEntry, 0, len(entries))
	for _, entry := range entries {
		if query != "" && !strings.Contains(strings.ToLower(entry.Name), query) && !strings.Contains(strings.ToLower(entry.CategoryName), query) {
			continue
		}
		filtered = append(filtered, entry)
	}
	limit := argInt(args, "limit")
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	truncated := len(filtered) > limit
	if truncated {
		filtered = filtered[:limit]
	}
	return catalogJSONResult(false, map[string]any{
		"action": "list", "scope": scopeID, "count": len(filtered),
		"truncated": truncated, "channels": filtered,
	})
}

func channelCatalogResolve(entries []channels.ChannelCatalogEntry, scopeID string, args map[string]any) *Result {
	name := strings.TrimSpace(argString(args, "name"))
	if name == "" {
		return catalogError("invalid_argument", "name is required for resolve", nil)
	}
	kind := strings.ToLower(strings.TrimSpace(argString(args, "kind")))
	candidates := filterCatalogKind(entries, kind)
	for _, entry := range candidates {
		if entry.ChannelID == name {
			return catalogJSONResult(false, map[string]any{"action": "resolve", "scope": scopeID, "channel": entry})
		}
	}
	normalized := normalizeCatalogName(name)
	matches := make([]channels.ChannelCatalogEntry, 0, 2)
	for _, entry := range candidates {
		if normalizeCatalogName(entry.Name) == normalized {
			matches = append(matches, entry)
		}
	}
	if len(matches) == 1 {
		return catalogJSONResult(false, map[string]any{"action": "resolve", "scope": scopeID, "channel": matches[0]})
	}
	if len(matches) > 1 {
		return catalogError("ambiguous", "multiple channels have that exact name; use a channel ID or kind", matches)
	}
	return catalogError("not_found", "no channel with that exact ID or name exists in the current clan/guild", nil)
}

func filterCatalogKind(entries []channels.ChannelCatalogEntry, kind string) []channels.ChannelCatalogEntry {
	if kind == "" {
		return entries
	}
	out := make([]channels.ChannelCatalogEntry, 0, len(entries))
	for _, entry := range entries {
		if strings.EqualFold(entry.Kind, kind) {
			out = append(out, entry)
		}
	}
	return out
}

func normalizeCatalogName(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func catalogError(code, message string, candidates []channels.ChannelCatalogEntry) *Result {
	return catalogJSONResult(true, map[string]any{"code": code, "message": message, "candidates": candidates})
}

func catalogJSONResult(isError bool, value map[string]any) *Result {
	data, err := json.Marshal(value)
	if err != nil {
		return ErrorResult(fmt.Sprintf("channel_catalog: encode result: %v", err))
	}
	result := NewResult(string(data))
	result.IsError = isError
	return result
}
