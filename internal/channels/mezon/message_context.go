package mezon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mezonsdk "github.com/dungxbuif/mezon-sdk-go"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	mezonTopicHistoryLimit   = 25
	mezonTopicHistoryTimeout = 5 * time.Second
)

func withMezonReplyContext(message *mezonsdk.ChannelMessage, content string) string {
	if message == nil || len(message.References) == 0 {
		return content
	}
	ref := message.References[0]
	author := firstNonBlank(ref.MessageSenderDisplayName, ref.MessageSenderClanNick, ref.MessageSenderUsername, ref.MessageSenderID, "unknown")
	body := mezonContentText(ref.Content)
	if body == "" && len(message.ReferencedMessage) > 0 {
		var referenced mezonsdk.ChannelMessage
		if json.Unmarshal(message.ReferencedMessage, &referenced) == nil {
			body = referenced.ContentText()
			author = firstNonBlank(referenced.DisplayName, referenced.ClanNick, referenced.Username, referenced.SenderID, author)
		}
	}
	if body == "" {
		return content
	}
	reply := fmt.Sprintf("[Replying to %s]\n%s\n[/Replying]", author, channels.Truncate(body, 500))
	if strings.TrimSpace(content) == "" {
		return reply
	}
	return reply + "\n\n" + content
}

func (c *Channel) enrichMessageChannelMetadata(ctx context.Context, message *mezonsdk.ChannelMessage, metadata map[string]string) {
	client, ok := c.client.(sdkChannelContextClient)
	if !ok || message == nil || message.ChannelID == "" {
		return
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	entry, err := client.ResolveChannel(lookupCtx, message.ChannelID)
	if err != nil {
		return
	}
	if title := mezonChatTitle(entry.ClanName, firstNonBlank(entry.Name, message.ChannelLabel)); title != "" {
		metadata[tools.MetaChatTitle] = title
	}
	if entry.ClanName != "" {
		metadata["clan_name"] = entry.ClanName
	}
	if entry.CategoryID != "" {
		metadata["category_id"] = entry.CategoryID
	}
	if entry.CategoryName != "" {
		metadata["category_name"] = entry.CategoryName
	}
	if entry.ParentID != "" {
		metadata["parent_channel_id"] = entry.ParentID
	}
}

func mezonContentText(raw string) string {
	var content struct {
		Text string `json:"t"`
	}
	if json.Unmarshal([]byte(raw), &content) == nil && content.Text != "" {
		return strings.TrimSpace(content.Text)
	}
	return strings.TrimSpace(raw)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (c *Channel) withTopicHistory(ctx context.Context, current *mezonsdk.ChannelMessage, content string) string {
	client, ok := c.client.(sdkHistoryClient)
	if !ok || current == nil || current.TopicID == "" || current.TopicID == "0" || current.MessageID == "" {
		return content
	}
	historyCtx, cancel := context.WithTimeout(ctx, mezonTopicHistoryTimeout)
	defer cancel()
	messages, err := client.ListChannelMessages(historyCtx, current.ClanID, current.ChannelID, current.MessageID, current.TopicID, mezonTopicHistoryLimit)
	if err != nil {
		slog.Warn("mezon: topic history backfill failed", "channel_id", current.ChannelID, "topic_id", current.TopicID, "error", err)
		return content
	}
	lines := []string{"[Mezon topic messages before this mention - for context]"}
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message == nil || message.MessageID == current.MessageID || message.SenderID == c.config.BotID {
			continue
		}
		body := strings.TrimSpace(message.ContentText())
		if body == "" {
			continue
		}
		author := firstNonBlank(message.DisplayName, message.ClanNick, message.Username, message.SenderID, "unknown")
		lines = append(lines, fmt.Sprintf("%s: %s", author, channels.Truncate(body, 1000)))
	}
	if len(lines) == 1 {
		return content
	}
	return strings.Join(lines, "\n") + "\n\n" + content
}
