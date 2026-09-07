package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func catalogToolContext() context.Context {
	ctx := WithToolChannel(context.Background(), "mezon-prod")
	return store.WithRunContext(ctx, &store.RunContext{Channel: "mezon-prod", ChannelType: channels.TypeMezon, ContainerID: "clan-1"})
}

func newCatalogToolForTest(t *testing.T, entries []channels.ChannelCatalogEntry) *ChannelCatalogTool {
	t.Helper()
	tool := NewChannelCatalogTool()
	tool.SetChannelCatalogLister(func(_ context.Context, channel string, opts channels.ChannelCatalogOptions) ([]channels.ChannelCatalogEntry, error) {
		if channel != "mezon-prod" || opts.ScopeID != "clan-1" {
			t.Fatalf("unexpected trusted scope: channel=%q opts=%#v", channel, opts)
		}
		return entries, nil
	})
	return tool
}

func TestChannelCatalogToolListFiltersAndLimits(t *testing.T) {
	tool := newCatalogToolForTest(t, []channels.ChannelCatalogEntry{
		{ContainerID: "clan-1", ChannelID: "1", Name: "general", CategoryName: "Main"},
		{ContainerID: "clan-1", ChannelID: "2", Name: "support", CategoryName: "Help"},
		{ContainerID: "clan-1", ChannelID: "3", Name: "support-private", CategoryName: "Help"},
	})
	res := tool.Execute(catalogToolContext(), map[string]any{"action": "list", "query": "support", "limit": float64(1)})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.ForLLM)
	}
	var out struct {
		Count     int                            `json:"count"`
		Truncated bool                           `json:"truncated"`
		Channels  []channels.ChannelCatalogEntry `json:"channels"`
	}
	if err := json.Unmarshal([]byte(res.ForLLM), &out); err != nil {
		t.Fatal(err)
	}
	if out.Count != 1 || !out.Truncated || len(out.Channels) != 1 || out.Channels[0].ChannelID != "2" {
		t.Fatalf("output = %#v", out)
	}
}

func TestChannelCatalogToolResolveUsesExactRules(t *testing.T) {
	entries := []channels.ChannelCatalogEntry{
		{ContainerID: "clan-1", ChannelID: "10", Name: "Product   Support"},
		{ContainerID: "clan-1", ChannelID: "11", Name: "engineering"},
	}
	tool := newCatalogToolForTest(t, entries)
	for _, name := range []string{"10", "product support", " PRODUCT SUPPORT "} {
		res := tool.Execute(catalogToolContext(), map[string]any{"action": "resolve", "name": name})
		if res.IsError || !strings.Contains(res.ForLLM, `"channel_id":"10"`) {
			t.Fatalf("resolve %q = %#v", name, res)
		}
	}
}

func TestChannelCatalogToolResolveRejectsAmbiguousAndFuzzyNames(t *testing.T) {
	tool := newCatalogToolForTest(t, []channels.ChannelCatalogEntry{
		{ContainerID: "clan-1", ChannelID: "10", Name: "support"},
		{ContainerID: "clan-1", ChannelID: "11", Name: "Support"},
	})
	ambiguous := tool.Execute(catalogToolContext(), map[string]any{"action": "resolve", "name": "support"})
	if !ambiguous.IsError || !strings.Contains(ambiguous.ForLLM, `"code":"ambiguous"`) {
		t.Fatalf("ambiguous result = %#v", ambiguous)
	}
	notFound := tool.Execute(catalogToolContext(), map[string]any{"action": "resolve", "name": "supp"})
	if !notFound.IsError || !strings.Contains(notFound.ForLLM, `"code":"not_found"`) {
		t.Fatalf("not-found result = %#v", notFound)
	}
}

func TestChannelCatalogToolRequiresTrustedContext(t *testing.T) {
	tool := NewChannelCatalogTool()
	tool.SetChannelCatalogLister(func(context.Context, string, channels.ChannelCatalogOptions) ([]channels.ChannelCatalogEntry, error) {
		t.Fatal("lister must not run without trusted context")
		return nil, nil
	})
	ctx := WithToolChannel(context.Background(), "mezon-prod")
	res := tool.Execute(ctx, map[string]any{"action": "list", "container_id": "attacker-clan"})
	if !res.IsError {
		t.Fatalf("expected error, got %#v", res)
	}
}

func TestChannelCatalogToolChannelTypes(t *testing.T) {
	got := NewChannelCatalogTool().RequiredChannelTypes()
	if len(got) != 2 || got[0] != channels.TypeDiscord || got[1] != channels.TypeMezon {
		t.Fatalf("channel types = %#v", got)
	}
}
