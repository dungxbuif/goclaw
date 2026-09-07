package mezon

import (
	"context"
	"fmt"
	"time"

	mezonsdk "github.com/dungxbuif/mezon-sdk-go"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

const channelCatalogRefreshCooldown = 5 * time.Second

type sdkCatalogEntry struct {
	ClanID       string
	ClanName     string
	ChannelID    string
	Name         string
	Type         int
	CategoryID   string
	CategoryName string
	ParentID     string
	Private      bool
}

type sdkChannelCatalogClient interface {
	ListChannels(context.Context, string, bool) ([]sdkCatalogEntry, error)
}

func (c *liveSDKClient) ListChannels(ctx context.Context, clanID string, refresh bool) ([]sdkCatalogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	clan, ok := c.client.Clans.Get(clanID)
	if !ok || clan == nil {
		return nil, fmt.Errorf("mezon clan %q is not available to this bot", clanID)
	}
	var err error
	if refresh {
		err = clan.ReloadChannels()
	} else if clan.Channels == nil || clan.Channels.Size() == 0 {
		err = clan.LoadChannels()
	}
	if err != nil {
		return nil, fmt.Errorf("load channels for mezon clan %s: %w", clanID, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if clan.Channels == nil {
		return nil, nil
	}
	values := clan.Channels.Values()
	out := make([]sdkCatalogEntry, 0, len(values))
	for _, channel := range values {
		if channel == nil || channel.ID == "" || channel.Clan == nil || channel.Clan.ID != clanID {
			continue
		}
		out = append(out, sdkCatalogEntry{
			ClanID: clanID, ClanName: clan.Name, ChannelID: channel.ID,
			Name: channel.Name, Type: channel.ChannelType, CategoryID: channel.CategoryID,
			CategoryName: channel.CategoryName, ParentID: channel.ParentID, Private: channel.IsPrivate,
		})
	}
	return out, nil
}

func (c *Channel) ListChannelCatalog(ctx context.Context, opts channels.ChannelCatalogOptions) ([]channels.ChannelCatalogEntry, error) {
	if opts.ScopeID == "" || opts.ScopeID == "0" {
		return nil, fmt.Errorf("mezon channel catalog requires a clan scope")
	}
	client, ok := c.client.(sdkChannelCatalogClient)
	if !ok {
		return nil, fmt.Errorf("mezon SDK client does not support channel catalog")
	}
	refresh, finishRefresh := c.beginCatalogRefresh(opts.ScopeID, opts.Refresh)
	refreshSucceeded := false
	defer func() { finishRefresh(refreshSucceeded) }()
	items, err := client.ListChannels(ctx, opts.ScopeID, refresh)
	if err != nil {
		return nil, err
	}
	refreshSucceeded = refresh
	out := make([]channels.ChannelCatalogEntry, 0, len(items))
	for _, item := range items {
		if item.ClanID != opts.ScopeID || item.ChannelID == "" || item.Type == int(mezonsdk.ChannelTypeDM) {
			continue
		}
		kind := mezonChannelKind(item.Type)
		if kind == "thread" && !opts.IncludeThreads {
			continue
		}
		capabilities := []channels.ChannelCapability{channels.CapabilityView}
		if mezonKindCanSend(kind) {
			capabilities = append(capabilities,
				channels.CapabilitySendMessages,
				channels.CapabilityAddReactions,
				channels.CapabilityEditOwnMessage,
				channels.CapabilityDeleteOwnMessage,
				channels.CapabilityInteractive,
			)
		}
		out = append(out, channels.ChannelCatalogEntry{
			Platform: channels.TypeMezon, Instance: c.Name(), ContainerID: item.ClanID,
			ContainerName: item.ClanName, ContainerKind: "clan", CategoryID: item.CategoryID,
			CategoryName: item.CategoryName, ChannelID: item.ChannelID, Name: item.Name,
			Kind: kind, ParentID: item.ParentID, Private: item.Private,
			Capabilities: capabilities, CapabilityEvidence: "adapter",
		})
	}
	channels.SortChannelCatalog(out)
	return out, nil
}

func (c *Channel) beginCatalogRefresh(scopeID string, requested bool) (bool, func(bool)) {
	if !requested {
		return false, func(bool) {}
	}
	c.catalogRefreshMu.Lock()
	if last := c.catalogRefreshed[scopeID]; !last.IsZero() && time.Since(last) < channelCatalogRefreshCooldown {
		return false, func(bool) { c.catalogRefreshMu.Unlock() }
	}
	return true, func(succeeded bool) {
		if succeeded {
			if c.catalogRefreshed == nil {
				c.catalogRefreshed = make(map[string]time.Time)
			}
			c.catalogRefreshed[scopeID] = time.Now()
		}
		c.catalogRefreshMu.Unlock()
	}
}

func mezonChannelKind(channelType int) string {
	switch mezonsdk.ChannelType(channelType) {
	case mezonsdk.ChannelTypeChannel, mezonsdk.ChannelTypeGroup:
		return "text"
	case mezonsdk.ChannelTypeForum:
		return "forum"
	case mezonsdk.ChannelTypeThread:
		return "thread"
	case mezonsdk.ChannelTypeAnnouncement:
		return "announcement"
	case mezonsdk.ChannelTypeGmeetVoice, mezonsdk.ChannelTypeMezonVoice:
		return "voice"
	case mezonsdk.ChannelTypeStreaming:
		return "streaming"
	case mezonsdk.ChannelTypeApp:
		return "app"
	default:
		return fmt.Sprintf("unknown:%d", channelType)
	}
}

func mezonKindCanSend(kind string) bool {
	switch kind {
	case "text", "forum", "thread", "announcement":
		return true
	default:
		return false
	}
}
