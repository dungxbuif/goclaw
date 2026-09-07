package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

func TestMezonInteractiveToolBuildsEverySupportedComponent(t *testing.T) {
	tool := NewMezonInteractiveTool()
	var got channels.InteractiveMessage
	tool.SetInteractiveMessageSender(func(_ context.Context, channel, chatID, clanID string, message channels.InteractiveMessage) (string, error) {
		if channel != "mezon-prod" || chatID != "channel-1" || clanID != "clan-1" {
			t.Fatalf("scope = %q/%q/%q", channel, chatID, clanID)
		}
		got = message
		return "1876543210987654321", nil
	})
	ctx := WithToolChatID(catalogToolContext(), "channel-1")
	res := tool.Execute(ctx, map[string]any{
		"text": "Choose",
		"embed": map[string]any{
			"title": "All controls", "description": "exercise every renderer",
			"fields": []any{map[string]any{"name": "Status", "value": "Ready", "inline": true}},
			"inputs": []any{
				map[string]any{"kind": "input", "id": "note", "name": "Note", "placeholder": "Type", "input_type": "text", "textarea": true},
				map[string]any{"kind": "select", "id": "region", "name": "Region", "options": []any{map[string]any{"label": "VN", "value": "vn"}}, "selected": "vn"},
				map[string]any{"kind": "radio", "id": "plan", "name": "Plan", "max_options": 2, "options": []any{map[string]any{"label": "Pro", "value": "pro", "style": "success"}, map[string]any{"label": "Team", "value": "team"}}},
				map[string]any{"kind": "date", "id": "due", "name": "Due"},
				map[string]any{"kind": "animation", "id": "spin", "name": "Spin", "url_image": "https://example.com/sprite.png", "pool": []any{"a", "b"}, "repeat": 2, "duration": 500},
			},
		},
		"button_rows": []any{[]any{
			map[string]any{"id": "confirm", "label": "Confirm", "style": "success"},
			map[string]any{"id": "cancel", "label": "Cancel", "style": "danger"},
		}},
	})
	if res.IsError || !strings.Contains(res.ForLLM, `"message_id":"1876543210987654321"`) {
		t.Fatalf("result = %#v", res)
	}
	if len(got.ButtonRows) != 1 || len(got.ButtonRows[0]) != 2 || got.ButtonRows[0][0].Style != "success" {
		t.Fatalf("buttons = %#v", got.ButtonRows)
	}
	if got.Embed == nil || len(got.Embed.Inputs) != 5 || got.Embed.Inputs[4].Kind != "animation" {
		t.Fatalf("embed = %#v", got.Embed)
	}
}

func TestMezonInteractiveToolFailsClosedOutsideTrustedClanContext(t *testing.T) {
	tool := NewMezonInteractiveTool()
	tool.SetInteractiveMessageSender(func(context.Context, string, string, string, channels.InteractiveMessage) (string, error) {
		t.Fatal("sender must not run")
		return "", nil
	})
	ctx := WithToolChatID(WithToolChannel(context.Background(), "mezon-prod"), "channel-1")
	for _, args := range []map[string]any{
		{"text": "missing clan", "target": "channel-2"},
		{"text": "no controls"},
		{"text": "bad", "button_rows": []any{[]any{map[string]any{"id": "x", "label": "X", "style": "unknown"}}}},
	} {
		if res := tool.Execute(ctx, args); !res.IsError {
			t.Fatalf("args %#v unexpectedly succeeded: %#v", args, res)
		}
	}
}

func TestMezonInteractiveToolIsMezonOnly(t *testing.T) {
	got := NewMezonInteractiveTool().RequiredChannelTypes()
	if len(got) != 1 || got[0] != channels.TypeMezon {
		t.Fatalf("channel types = %#v", got)
	}
}
