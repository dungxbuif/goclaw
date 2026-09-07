package mezon

import (
	"context"
	"errors"
	"testing"

	mezonsdk "github.com/dungxbuif/mezon-sdk-go"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/config"
)

func testMezonConfig() config.MezonConfig {
	return config.MezonConfig{BotID: "bot-1", Token: "token", DMPolicy: "disabled", GroupPolicy: "open"}
}

type catalogSDKClient struct {
	*fakeSDKClient
	entries []sdkCatalogEntry
	gotClan string
	refresh bool
	err     error
	calls   int
}

func (f *catalogSDKClient) ListChannels(_ context.Context, clanID string, refresh bool) ([]sdkCatalogEntry, error) {
	f.calls++
	f.gotClan = clanID
	f.refresh = refresh
	return f.entries, f.err
}

func TestMezonCatalogRefreshHasPerClanCooldown(t *testing.T) {
	sdk := &catalogSDKClient{fakeSDKClient: &fakeSDKClient{}, entries: []sdkCatalogEntry{{ClanID: "clan-1", ChannelID: "10", Name: "support", Type: int(mezonsdk.ChannelTypeChannel)}}}
	ch := newWithClient(testMezonConfig(), bus.New(), nil, nil, sdk)
	opts := channels.ChannelCatalogOptions{ScopeID: "clan-1", Refresh: true}
	if _, err := ch.ListChannelCatalog(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.ListChannelCatalog(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if sdk.calls != 2 {
		t.Fatalf("calls = %d, want 2", sdk.calls)
	}
	if sdk.refresh {
		t.Fatal("second request inside cooldown must use cached catalog")
	}
}

func TestListChannelCatalogMapsOnlyTrustedClan(t *testing.T) {
	sdk := &catalogSDKClient{
		fakeSDKClient: &fakeSDKClient{},
		entries: []sdkCatalogEntry{
			{ClanID: "clan-1", ClanName: "Production", ChannelID: "10", Name: "support", Type: int(mezonsdk.ChannelTypeChannel), CategoryID: "2", CategoryName: "Help"},
			{ClanID: "clan-1", ClanName: "Production", ChannelID: "11", Name: "incident-42", Type: int(mezonsdk.ChannelTypeThread), ParentID: "10", Private: true},
		},
	}
	ch := newWithClient(testMezonConfig(), bus.New(), nil, nil, sdk)
	ch.SetName("mezon-prod")

	entries, err := ch.ListChannelCatalog(context.Background(), channels.ChannelCatalogOptions{ScopeID: "clan-1", Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if sdk.gotClan != "clan-1" || !sdk.refresh {
		t.Fatalf("SDK scope = %q refresh=%v", sdk.gotClan, sdk.refresh)
	}
	if len(entries) != 1 || entries[0].ContainerID != "clan-1" || entries[0].ChannelID != "10" || entries[0].Kind != "text" {
		t.Fatalf("entries = %#v", entries)
	}
	if entries[0].CategoryID != "2" || entries[0].CategoryName != "Help" || entries[0].Instance != "mezon-prod" {
		t.Fatalf("metadata = %#v", entries[0])
	}
	wantCapabilities := []channels.ChannelCapability{
		channels.CapabilityView,
		channels.CapabilitySendMessages,
		channels.CapabilityAddReactions,
		channels.CapabilityEditOwnMessage,
		channels.CapabilityDeleteOwnMessage,
		channels.CapabilityInteractive,
	}
	if len(entries[0].Capabilities) != len(wantCapabilities) {
		t.Fatalf("capabilities = %#v", entries[0].Capabilities)
	}
	for i := range wantCapabilities {
		if entries[0].Capabilities[i] != wantCapabilities[i] {
			t.Fatalf("capabilities = %#v", entries[0].Capabilities)
		}
	}
}

func TestListChannelCatalogIncludesThreadsOnlyWhenRequested(t *testing.T) {
	sdk := &catalogSDKClient{fakeSDKClient: &fakeSDKClient{}, entries: []sdkCatalogEntry{
		{ClanID: "clan-1", ChannelID: "10", Name: "support", Type: int(mezonsdk.ChannelTypeChannel)},
		{ClanID: "clan-1", ChannelID: "11", Name: "incident", Type: int(mezonsdk.ChannelTypeThread), ParentID: "10"},
	}}
	ch := newWithClient(testMezonConfig(), bus.New(), nil, nil, sdk)
	entries, err := ch.ListChannelCatalog(context.Background(), channels.ChannelCatalogOptions{ScopeID: "clan-1", IncludeThreads: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || (entries[0].Kind != "thread" && entries[1].Kind != "thread") {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestListChannelCatalogFailsClosed(t *testing.T) {
	ch := newWithClient(testMezonConfig(), bus.New(), nil, nil, &fakeSDKClient{})
	if _, err := ch.ListChannelCatalog(context.Background(), channels.ChannelCatalogOptions{ScopeID: "clan-1"}); err == nil {
		t.Fatal("expected client without catalog support to fail")
	}

	sdk := &catalogSDKClient{fakeSDKClient: &fakeSDKClient{}, err: errors.New("not a member")}
	ch = newWithClient(testMezonConfig(), bus.New(), nil, nil, sdk)
	if _, err := ch.ListChannelCatalog(context.Background(), channels.ChannelCatalogOptions{ScopeID: "clan-2"}); err == nil {
		t.Fatal("expected SDK scope error")
	}
}

func TestLiveSDKListChannelsUsesScopedCache(t *testing.T) {
	client, err := mezonsdk.NewMezonClient(mezonsdk.ClientConfig{BotID: "bot-1", Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	clan1 := &mezonsdk.Clan{ID: "clan-1", Name: "One", Channels: mezonsdk.NewCacheManager[string, *mezonsdk.TextChannel](nil, 0)}
	clan1.Channels.Set("10", &mezonsdk.TextChannel{ID: "10", Name: "support", ChannelType: int(mezonsdk.ChannelTypeChannel), Clan: clan1})
	clan2 := &mezonsdk.Clan{ID: "clan-2", Name: "Two", Channels: mezonsdk.NewCacheManager[string, *mezonsdk.TextChannel](nil, 0)}
	clan2.Channels.Set("20", &mezonsdk.TextChannel{ID: "20", Name: "secret", ChannelType: int(mezonsdk.ChannelTypeChannel), Clan: clan2})
	client.Clans.Set(clan1.ID, clan1)
	client.Clans.Set(clan2.ID, clan2)

	entries, err := (&liveSDKClient{client: client}).ListChannels(context.Background(), "clan-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ChannelID != "10" || entries[0].ClanID != "clan-1" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestLiveSDKListChannelsHonorsCancellation(t *testing.T) {
	client, err := mezonsdk.NewMezonClient(mezonsdk.ClientConfig{BotID: "bot-1", Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (&liveSDKClient{client: client}).ListChannels(ctx, "clan-1", false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
