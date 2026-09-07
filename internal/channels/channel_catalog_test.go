package channels

import (
	"context"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

type catalogTestChannel struct {
	*mockChannel
	entries []ChannelCatalogEntry
	gotOpts ChannelCatalogOptions
}

type interactiveTestChannel struct {
	*mockChannel
	gotChatID string
	gotClanID string
}

func (c *interactiveTestChannel) SendInteractiveMessage(_ context.Context, chatID, clanID string, _ InteractiveMessage) (string, error) {
	c.gotChatID, c.gotClanID = chatID, clanID
	return "message-1", nil
}

func (c *catalogTestChannel) ListChannelCatalog(_ context.Context, opts ChannelCatalogOptions) ([]ChannelCatalogEntry, error) {
	c.gotOpts = opts
	return c.entries, nil
}

func TestManagerListChannelCatalogDelegatesTrustedScope(t *testing.T) {
	mgr := NewManager(bus.New())
	provider := &catalogTestChannel{
		mockChannel: newMockChannel("mezon-prod", TypeMezon),
		entries:     []ChannelCatalogEntry{{ChannelID: "20", Name: "support"}},
	}
	mgr.RegisterChannel("mezon-prod", provider)

	got, err := mgr.ListChannelCatalog(context.Background(), "mezon-prod", ChannelCatalogOptions{ScopeID: "clan-1", IncludeThreads: true})
	if err != nil {
		t.Fatal(err)
	}
	if provider.gotOpts.ScopeID != "clan-1" || !provider.gotOpts.IncludeThreads {
		t.Fatalf("provider options = %#v", provider.gotOpts)
	}
	if len(got) != 1 || got[0].ChannelID != "20" {
		t.Fatalf("entries = %#v", got)
	}
}

func TestManagerListChannelCatalogFailsClosedWithoutScope(t *testing.T) {
	mgr := NewManager(bus.New())
	mgr.RegisterChannel("mezon-prod", &catalogTestChannel{mockChannel: newMockChannel("mezon-prod", TypeMezon)})
	if _, err := mgr.ListChannelCatalog(context.Background(), "mezon-prod", ChannelCatalogOptions{}); err == nil {
		t.Fatal("expected empty scope to be rejected")
	}
}

func TestManagerListChannelCatalogRejectsUnknownAndUnsupportedChannels(t *testing.T) {
	mgr := NewManager(bus.New())
	if _, err := mgr.ListChannelCatalog(context.Background(), "missing", ChannelCatalogOptions{ScopeID: "clan-1"}); err == nil {
		t.Fatal("expected unknown channel error")
	}
	mgr.RegisterChannel("telegram", newMockChannel("telegram", TypeTelegram))
	if _, err := mgr.ListChannelCatalog(context.Background(), "telegram", ChannelCatalogOptions{ScopeID: "group-1"}); err == nil {
		t.Fatal("expected unsupported provider error")
	}
}

func TestManagerSendInteractiveMessageDelegatesTrustedScope(t *testing.T) {
	mgr := NewManager(bus.New())
	provider := &interactiveTestChannel{mockChannel: newMockChannel("mezon-prod", TypeMezon)}
	mgr.RegisterChannel("mezon-prod", provider)
	messageID, err := mgr.SendInteractiveMessage(context.Background(), "mezon-prod", "channel-1", "clan-1", InteractiveMessage{Text: "Choose"})
	if err != nil {
		t.Fatal(err)
	}
	if messageID != "message-1" || provider.gotChatID != "channel-1" || provider.gotClanID != "clan-1" {
		t.Fatalf("result=%q scope=%q/%q", messageID, provider.gotChatID, provider.gotClanID)
	}
	if _, err := mgr.SendInteractiveMessage(context.Background(), "mezon-prod", "channel-1", "", InteractiveMessage{}); err == nil {
		t.Fatal("expected empty trusted scope to fail closed")
	}
}

func TestSortChannelCatalogIsDeterministic(t *testing.T) {
	entries := []ChannelCatalogEntry{
		{ContainerName: "Clan", CategoryName: "Ops", Name: "zeta", ChannelID: "3"},
		{ContainerName: "Clan", CategoryName: "General", Name: "beta", ChannelID: "2"},
		{ContainerName: "Clan", CategoryName: "General", Name: "Alpha", ChannelID: "1"},
	}
	SortChannelCatalog(entries)
	if entries[0].ChannelID != "1" || entries[1].ChannelID != "2" || entries[2].ChannelID != "3" {
		t.Fatalf("unexpected order: %#v", entries)
	}
}
