package mezon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mezonsdk "github.com/dungxbuif/mezon-sdk-go"
	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type mezonAgentStore interface {
	GetByKey(context.Context, string) (*store.AgentData, error)
}

func (c *Channel) resolveAgentUUID(ctx context.Context) (uuid.UUID, error) {
	key := strings.TrimSpace(c.AgentID())
	if id, err := uuid.Parse(key); err == nil {
		return id, nil
	}
	if key == "" || c.agentStore == nil {
		return uuid.Nil, fmt.Errorf("Mezon channel has no resolvable agent")
	}
	agent, err := c.agentStore.GetByKey(store.WithTenantID(ctx, c.TenantID()), key)
	if err != nil {
		return uuid.Nil, err
	}
	return agent.ID, nil
}

func (c *Channel) tryHandleCronCommand(parent context.Context, message *mezonsdk.ChannelMessage, direct bool) bool {
	command := strings.ToLower(strings.TrimSpace(message.ContentText()))
	if command != "/addcron" && command != "/removecron" && command != "/croners" {
		return false
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	send := func(text string) {
		if _, err := c.client.SendMessage(ctx, message.ChannelID, text, strings.TrimSpace(message.TopicID), message.MessageID); err != nil {
			slog.Warn("mezon: cron permission command response failed", "channel_id", message.ChannelID, "error", err)
		}
	}
	if direct {
		send("This command only works in clan channels.")
		return true
	}
	if c.configPermStore == nil {
		send("Cron permission management is not available for this Mezon bot.")
		return true
	}
	agentID, err := c.resolveAgentUUID(ctx)
	if err != nil {
		slog.Warn("mezon: resolve agent for cron permission command failed", "error", err)
		send("Cron permission management is not available because the channel has no agent binding.")
		return true
	}
	scope := fmt.Sprintf("group:%s:%s", c.Name(), message.ChannelID)
	croners, err := c.configPermStore.List(ctx, agentID, store.ConfigTypeCron, scope)
	if err != nil {
		send("Failed to read cron managers. Please try again.")
		return true
	}
	writers, _ := c.configPermStore.ListFileWriters(ctx, agentID, scope)
	if command == "/croners" {
		send(formatMezonCroners(croners, writers))
		return true
	}
	action := "add"
	if command == "/removecron" {
		action = "remove"
	}
	if len(croners) > 0 || len(writers) > 0 {
		if !hasAllowedMezonPermission(message.SenderID, croners) && !hasAllowedMezonPermission(message.SenderID, writers) {
			send("Only an existing cron manager or file writer can manage cron access.")
			return true
		}
	} else if action == "remove" {
		send("No cron managers are configured. Reply to a user's message with /addcron to add the first one.")
		return true
	}
	if len(message.References) == 0 || strings.TrimSpace(message.References[0].MessageSenderID) == "" {
		send(fmt.Sprintf("Reply to the target user's message with /%scron.", action))
		return true
	}
	target := message.References[0]
	targetID := strings.TrimSpace(target.MessageSenderID)
	targetName := firstNonEmptyString(target.MessageSenderDisplayName, target.MessageSenderClanNick, target.MessageSenderUsername, targetID)
	if action == "add" {
		meta, _ := json.Marshal(map[string]string{"displayName": targetName, "username": target.MessageSenderUsername})
		err = c.configPermStore.Grant(ctx, &store.ConfigPermission{AgentID: agentID, Scope: scope, ConfigType: store.ConfigTypeCron, UserID: targetID, Permission: "allow", Metadata: meta})
		if err != nil {
			send("Failed to add cron manager. Please try again.")
			return true
		}
		send(fmt.Sprintf("Added %s as a cron manager for this Mezon channel.", targetName))
		return true
	}
	if len(croners) <= 1 && len(writers) == 0 {
		send("Cannot remove the last cron manager while no file writer can manage cron access.")
		return true
	}
	if err := c.configPermStore.Revoke(ctx, agentID, scope, store.ConfigTypeCron, targetID); err != nil {
		send("Failed to remove cron manager. Please try again.")
		return true
	}
	send(fmt.Sprintf("Removed %s from cron managers for this Mezon channel.", targetName))
	return true
}

func hasAllowedMezonPermission(userID string, rows []store.ConfigPermission) bool {
	for _, row := range rows {
		if row.UserID == userID && row.Permission == "allow" {
			return true
		}
	}
	return false
}

func formatMezonCroners(croners, writers []store.ConfigPermission) string {
	if len(croners) == 0 && len(writers) == 0 {
		return "No cron managers are configured for this Mezon channel. Reply to a user's message with /addcron to add one."
	}
	var out strings.Builder
	if len(croners) == 0 {
		out.WriteString("No explicit cron managers are configured.")
	} else {
		fmt.Fprintf(&out, "Cron managers for this Mezon channel (%d):", len(croners))
		for i, row := range croners {
			fmt.Fprintf(&out, "\n%d. %s (ID: %s) — %s", i+1, channels.WriterLabel(row.Metadata, row.UserID), row.UserID, row.Permission)
		}
	}
	if len(writers) > 0 {
		fmt.Fprintf(&out, "\n\nPlus %d file writer(s) with implicit cron access.", len(writers))
	}
	return out.String()
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
