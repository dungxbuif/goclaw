package mezon

import (
	"context"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/channelmemory"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func (c *Channel) ResolveMemoryExtractionContext(ctx context.Context, inst *store.ChannelInstanceData, group store.PendingMessageGroup) (channelmemory.ExtractionContext, error) {
	out := channelmemory.ExtractionContext{Platform: channels.TypeMezon, ChannelInstance: group.ChannelName, HistoryKey: group.HistoryKey, ChannelID: mezonChannelIDFromHistoryKey(group.HistoryKey), ParentChannelID: group.ParentHistoryKey}
	if inst != nil && inst.Name != "" {
		out.ChannelInstance = inst.Name
	}
	client, ok := c.client.(sdkChannelContextClient)
	if !ok || out.ChannelID == "" {
		return out, nil
	}
	entry, err := client.ResolveChannel(ctx, out.ChannelID)
	if err != nil {
		return out, nil
	}
	out.ChannelName = channels.SanitizeDisplayName(entry.Name)
	out.CategoryID = entry.CategoryID
	out.CategoryName = channels.SanitizeDisplayName(entry.CategoryName)
	if out.ParentChannelID == "" {
		out.ParentChannelID = entry.ParentID
	}
	if out.ParentChannelID != "" {
		if parent, parentErr := client.ResolveChannel(ctx, out.ParentChannelID); parentErr == nil {
			out.ParentChannelName = channels.SanitizeDisplayName(parent.Name)
			if out.CategoryID == "" {
				out.CategoryID, out.CategoryName = parent.CategoryID, channels.SanitizeDisplayName(parent.CategoryName)
			}
		}
	}
	return out, nil
}

func mezonChannelIDFromHistoryKey(key string) string {
	if before, _, ok := strings.Cut(key, ":thread:"); ok {
		return before
	}
	return key
}
