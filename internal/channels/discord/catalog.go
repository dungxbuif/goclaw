package discord

import (
	"context"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

const channelCatalogRefreshCooldown = 5 * time.Second

func (c *Channel) ListChannelCatalog(ctx context.Context, opts channels.ChannelCatalogOptions) ([]channels.ChannelCatalogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.ScopeID == "" {
		return nil, fmt.Errorf("discord channel catalog requires a guild scope")
	}
	if c.session == nil || c.session.State == nil || c.botUserID == "" {
		return nil, fmt.Errorf("discord session is not ready")
	}
	guild, err := c.session.State.Guild(opts.ScopeID)
	if err != nil || guild == nil {
		return nil, fmt.Errorf("discord guild %q is not available to this bot", opts.ScopeID)
	}
	needsRefresh := opts.Refresh || len(guild.Channels) == 0
	refresh, finishRefresh := c.beginCatalogRefresh(opts.ScopeID, needsRefresh)
	refreshSucceeded := false
	defer func() { finishRefresh(refreshSucceeded) }()

	var platformChannels []*discordgo.Channel
	if refresh {
		platformChannels, err = c.session.GuildChannels(opts.ScopeID, discordgo.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("refresh discord guild channels: %w", err)
		}
		for _, channel := range platformChannels {
			if channel != nil {
				_ = c.session.State.ChannelAdd(channel)
			}
		}
	} else {
		platformChannels = append(platformChannels, guild.Channels...)
	}

	if opts.IncludeThreads {
		if refresh {
			threads, threadErr := c.session.GuildThreadsActive(opts.ScopeID, discordgo.WithContext(ctx))
			if threadErr != nil {
				return nil, fmt.Errorf("refresh discord active threads: %w", threadErr)
			}
			for _, thread := range threads.Threads {
				if thread != nil {
					_ = c.session.State.ChannelAdd(thread)
					platformChannels = append(platformChannels, thread)
				}
			}
		} else {
			platformChannels = append(platformChannels, guild.Threads...)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	refreshSucceeded = refresh

	categoryNames := make(map[string]string)
	for _, channel := range platformChannels {
		if channel != nil && channel.GuildID == opts.ScopeID && channel.Type == discordgo.ChannelTypeGuildCategory {
			categoryNames[channel.ID] = channel.Name
		}
	}
	out := make([]channels.ChannelCatalogEntry, 0, len(platformChannels))
	for _, channel := range platformChannels {
		if channel == nil || channel.ID == "" || channel.GuildID != opts.ScopeID {
			continue
		}
		if discordChannelIsThread(channel.Type) && !opts.IncludeThreads {
			continue
		}
		permissions, permErr := c.session.State.UserChannelPermissions(c.botUserID, channel.ID)
		if permErr != nil || permissions&discordgo.PermissionViewChannel == 0 {
			continue
		}
		out = append(out, channels.ChannelCatalogEntry{
			Platform: channels.TypeDiscord, Instance: c.Name(), ContainerID: opts.ScopeID,
			ContainerName: guild.Name, ContainerKind: "guild", CategoryID: discordCategoryID(channel),
			CategoryName: categoryNames[discordCategoryID(channel)], ChannelID: channel.ID,
			Name: channel.Name, Kind: discordChannelKind(channel.Type), ParentID: channel.ParentID,
			Position: channel.Position, Capabilities: discordCatalogCapabilities(channel.Type, permissions),
			CapabilityEvidence: "permission",
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

func discordCategoryID(channel *discordgo.Channel) string {
	if channel == nil || channel.Type == discordgo.ChannelTypeGuildCategory || discordChannelIsThread(channel.Type) {
		return ""
	}
	return channel.ParentID
}

func discordChannelIsThread(channelType discordgo.ChannelType) bool {
	switch channelType {
	case discordgo.ChannelTypeGuildPublicThread, discordgo.ChannelTypeGuildPrivateThread, discordgo.ChannelTypeGuildNewsThread:
		return true
	default:
		return false
	}
}

func discordChannelKind(channelType discordgo.ChannelType) string {
	switch channelType {
	case discordgo.ChannelTypeGuildText:
		return "text"
	case discordgo.ChannelTypeGuildVoice:
		return "voice"
	case discordgo.ChannelTypeGuildCategory:
		return "category"
	case discordgo.ChannelTypeGuildNews:
		return "announcement"
	case discordgo.ChannelTypeGuildStageVoice:
		return "stage"
	case discordgo.ChannelTypeGuildForum:
		return "forum"
	case discordgo.ChannelTypeGuildMedia:
		return "media"
	case discordgo.ChannelTypeGuildPublicThread:
		return "thread_public"
	case discordgo.ChannelTypeGuildPrivateThread:
		return "thread_private"
	case discordgo.ChannelTypeGuildNewsThread:
		return "thread_announcement"
	default:
		return fmt.Sprintf("unknown:%d", channelType)
	}
}

func discordCatalogCapabilities(channelType discordgo.ChannelType, permissions int64) []channels.ChannelCapability {
	result := []channels.ChannelCapability{channels.CapabilityView}
	if !discordChannelSupportsMessages(channelType) {
		return result
	}
	canSend := permissions&discordgo.PermissionSendMessages != 0
	if discordChannelIsThread(channelType) {
		canSend = permissions&discordgo.PermissionSendMessagesInThreads != 0
	}
	if canSend {
		result = append(result, channels.CapabilitySendMessages)
	}
	if permissions&discordgo.PermissionReadMessageHistory != 0 {
		result = append(result, channels.CapabilityReadHistory)
	}
	if permissions&discordgo.PermissionAttachFiles != 0 {
		result = append(result, channels.CapabilityAttachFiles)
	}
	return result
}

func discordChannelSupportsMessages(channelType discordgo.ChannelType) bool {
	switch channelType {
	case discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews,
		discordgo.ChannelTypeGuildPublicThread, discordgo.ChannelTypeGuildPrivateThread,
		discordgo.ChannelTypeGuildNewsThread:
		return true
	default:
		return false
	}
}
