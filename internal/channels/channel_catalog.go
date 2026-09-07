package channels

import (
	"context"
	"sort"
	"strings"
)

// ChannelCapability is a positively proven operation for a catalog entry.
type ChannelCapability string

const (
	CapabilityView             ChannelCapability = "view"
	CapabilitySendMessages     ChannelCapability = "send_messages"
	CapabilityReadHistory      ChannelCapability = "read_history"
	CapabilityAttachFiles      ChannelCapability = "attach_files"
	CapabilityAddReactions     ChannelCapability = "add_reactions"
	CapabilityEditOwnMessage   ChannelCapability = "edit_own_message"
	CapabilityDeleteOwnMessage ChannelCapability = "delete_own_message"
	CapabilityManageMessages   ChannelCapability = "manage_messages"
	CapabilityCreateThreads    ChannelCapability = "create_threads"
	CapabilitySendInThreads    ChannelCapability = "send_in_threads"
	CapabilityInteractive      ChannelCapability = "interactive_messages"
)

// ChannelCatalogEntry describes one channel visible in the trusted container.
type ChannelCatalogEntry struct {
	Platform           string              `json:"platform"`
	Instance           string              `json:"instance"`
	ContainerID        string              `json:"container_id"`
	ContainerName      string              `json:"container_name"`
	ContainerKind      string              `json:"container_kind"`
	CategoryID         string              `json:"category_id,omitempty"`
	CategoryName       string              `json:"category_name,omitempty"`
	ChannelID          string              `json:"channel_id"`
	Name               string              `json:"name"`
	Kind               string              `json:"kind"`
	ParentID           string              `json:"parent_id,omitempty"`
	Private            bool                `json:"private,omitempty"`
	Position           int                 `json:"position,omitempty"`
	Capabilities       []ChannelCapability `json:"capabilities"`
	CapabilityEvidence string              `json:"capability_evidence"`
}

type ChannelCatalogOptions struct {
	ScopeID        string
	IncludeThreads bool
	Refresh        bool
}

type ChannelCatalogProvider interface {
	ListChannelCatalog(context.Context, ChannelCatalogOptions) ([]ChannelCatalogEntry, error)
}

// SortChannelCatalog orders entries consistently for tool output and tests.
func SortChannelCatalog(entries []ChannelCatalogEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		for _, pair := range [][2]string{
			{a.ContainerName, b.ContainerName},
			{a.CategoryName, b.CategoryName},
		} {
			left, right := strings.ToLower(pair[0]), strings.ToLower(pair[1])
			if left != right {
				return left < right
			}
		}
		if a.Position != b.Position {
			return a.Position < b.Position
		}
		if left, right := strings.ToLower(a.Name), strings.ToLower(b.Name); left != right {
			return left < right
		}
		return a.ChannelID < b.ChannelID
	})
}
