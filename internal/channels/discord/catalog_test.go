package discord

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

func catalogDiscordChannel(t *testing.T) *Channel {
	t.Helper()
	session, err := discordgo.New("Bot token")
	if err != nil {
		t.Fatal(err)
	}
	session.State = discordgo.NewState()
	permissions := int64(discordgo.PermissionViewChannel | discordgo.PermissionSendMessages | discordgo.PermissionReadMessageHistory | discordgo.PermissionAttachFiles)
	guild := &discordgo.Guild{
		ID: "guild-1", Name: "Production",
		Roles:   []*discordgo.Role{{ID: "guild-1", Permissions: permissions}},
		Members: []*discordgo.Member{{User: &discordgo.User{ID: "bot-1"}}},
		Channels: []*discordgo.Channel{
			{ID: "cat-1", GuildID: "guild-1", Name: "Help", Type: discordgo.ChannelTypeGuildCategory, Position: 1},
			{ID: "visible-1", GuildID: "guild-1", ParentID: "cat-1", Name: "support", Type: discordgo.ChannelTypeGuildText, Position: 2},
			{ID: "hidden-1", GuildID: "guild-1", Name: "admin", Type: discordgo.ChannelTypeGuildText, PermissionOverwrites: []*discordgo.PermissionOverwrite{{ID: "guild-1", Type: discordgo.PermissionOverwriteTypeRole, Deny: discordgo.PermissionViewChannel}}},
		},
		Threads: []*discordgo.Channel{{ID: "thread-1", GuildID: "guild-1", ParentID: "visible-1", Name: "incident", Type: discordgo.ChannelTypeGuildPublicThread}},
	}
	if err := session.State.GuildAdd(guild); err != nil {
		t.Fatal(err)
	}
	if err := session.State.GuildAdd(&discordgo.Guild{ID: "guild-2", Name: "Other", Roles: []*discordgo.Role{{ID: "guild-2", Permissions: permissions}}, Members: []*discordgo.Member{{User: &discordgo.User{ID: "bot-1"}}}, Channels: []*discordgo.Channel{{ID: "other-1", GuildID: "guild-2", Name: "other", Type: discordgo.ChannelTypeGuildText}}}); err != nil {
		t.Fatal(err)
	}
	ch := &Channel{BaseChannel: channels.NewBaseChannel(channels.TypeDiscord, bus.New(), nil), session: session, botUserID: "bot-1"}
	ch.SetName("discord-prod")
	ch.SetType(channels.TypeDiscord)
	return ch
}

func TestDiscordCatalogIsGuildScopedAndPermissionAware(t *testing.T) {
	ch := catalogDiscordChannel(t)
	entries, err := ch.ListChannelCatalog(context.Background(), channels.ChannelCatalogOptions{ScopeID: "guild-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %#v", entries)
	}
	var support *channels.ChannelCatalogEntry
	for i := range entries {
		if entries[i].ChannelID == "hidden-1" || entries[i].ChannelID == "other-1" || entries[i].ChannelID == "thread-1" {
			t.Fatalf("out-of-scope entry leaked: %#v", entries[i])
		}
		if entries[i].ChannelID == "visible-1" {
			support = &entries[i]
		}
		if entries[i].ChannelID == "cat-1" && len(entries[i].Capabilities) != 1 {
			t.Fatalf("category must advertise view only, got %#v", entries[i].Capabilities)
		}
	}
	if support == nil || support.CategoryID != "cat-1" || support.CategoryName != "Help" || support.ContainerName != "Production" {
		t.Fatalf("support entry = %#v", support)
	}
	want := []channels.ChannelCapability{channels.CapabilityView, channels.CapabilitySendMessages, channels.CapabilityReadHistory, channels.CapabilityAttachFiles}
	if len(support.Capabilities) != len(want) {
		t.Fatalf("capabilities = %#v", support.Capabilities)
	}
	for i := range want {
		if support.Capabilities[i] != want[i] {
			t.Fatalf("capabilities = %#v", support.Capabilities)
		}
	}
}

func TestDiscordCatalogLoadsColdGuildCache(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"fresh-1","guild_id":"guild-1","name":"fresh","type":0}]`))
	}))
	defer server.Close()
	old := discordgo.EndpointGuildChannels
	discordgo.EndpointGuildChannels = func(string) string { return server.URL + "/guilds/guild-1/channels" }
	t.Cleanup(func() { discordgo.EndpointGuildChannels = old })

	ch := catalogDiscordChannel(t)
	ch.session.Client = server.Client()
	guild, err := ch.session.State.Guild("guild-1")
	if err != nil {
		t.Fatal(err)
	}
	guild.Channels = nil
	entries, err := ch.ListChannelCatalog(context.Background(), channels.ChannelCatalogOptions{ScopeID: "guild-1"})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || len(entries) != 1 || entries[0].ChannelID != "fresh-1" {
		t.Fatalf("requests=%d entries=%#v", requests, entries)
	}
}

func TestDiscordCatalogIncludesThreadsOnlyWhenRequested(t *testing.T) {
	ch := catalogDiscordChannel(t)
	entries, err := ch.ListChannelCatalog(context.Background(), channels.ChannelCatalogOptions{ScopeID: "guild-1", IncludeThreads: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.ChannelID == "thread-1" {
			if entry.Kind != "thread_public" || entry.ParentID != "visible-1" {
				t.Fatalf("thread = %#v", entry)
			}
			return
		}
	}
	t.Fatal("thread missing")
}

func TestDiscordCatalogRefreshUsesOnlyScopedGuild(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/guilds/guild-1/channels":
			_, _ = w.Write([]byte(`[{"id":"fresh-1","guild_id":"guild-1","name":"fresh","type":0}]`))
		case "/guilds/guild-1/threads/active":
			_, _ = w.Write([]byte(`{"threads":[],"members":[],"has_more":false}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	oldChannels, oldThreads := discordgo.EndpointGuildChannels, discordgo.EndpointGuildActiveThreads
	discordgo.EndpointGuildChannels = func(string) string { return server.URL + "/guilds/guild-1/channels" }
	discordgo.EndpointGuildActiveThreads = func(string) string { return server.URL + "/guilds/guild-1/threads/active" }
	t.Cleanup(func() {
		discordgo.EndpointGuildChannels, discordgo.EndpointGuildActiveThreads = oldChannels, oldThreads
	})

	ch := catalogDiscordChannel(t)
	ch.session.Client = server.Client()
	entries, err := ch.ListChannelCatalog(context.Background(), channels.ChannelCatalogOptions{ScopeID: "guild-1", IncludeThreads: true, Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || len(entries) != 1 || entries[0].ChannelID != "fresh-1" {
		t.Fatalf("paths=%v entries=%#v", paths, entries)
	}
}

func TestDiscordCatalogRefreshHasPerGuildCooldown(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"fresh-1","guild_id":"guild-1","name":"fresh","type":0}]`))
	}))
	defer server.Close()
	old := discordgo.EndpointGuildChannels
	discordgo.EndpointGuildChannels = func(string) string { return server.URL + "/guilds/guild-1/channels" }
	t.Cleanup(func() { discordgo.EndpointGuildChannels = old })

	ch := catalogDiscordChannel(t)
	ch.session.Client = server.Client()
	opts := channels.ChannelCatalogOptions{ScopeID: "guild-1", Refresh: true}
	if _, err := ch.ListChannelCatalog(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.ListChannelCatalog(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("refresh requests = %d, want 1", requests)
	}
}

func TestDiscordCatalogHonorsCancellation(t *testing.T) {
	ch := catalogDiscordChannel(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ch.ListChannelCatalog(ctx, channels.ChannelCatalogOptions{ScopeID: "guild-1", Refresh: true}); err == nil {
		t.Fatal("expected cancellation error")
	}
}
