// Package mezon connects GoClaw to Mezon through mezon-sdk-go.
package mezon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	mezonsdk "github.com/dungxbuif/mezon-sdk-go"
	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/systemmessages"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	maxOutboundChunkBytes = 7000
	maxContentWireUnits   = 8000
	pairingDebounceTime   = 60 * time.Second
)

type sdkClient interface {
	LoginContext(context.Context) error
	Close()
	OnChannelMessage(func(*mezonsdk.ChannelMessage)) func()
	Send(context.Context, string, string) error
	SendMessage(context.Context, string, string, string, string) (string, error)
	UpdateMessage(context.Context, string, string, string, string) error
	ReactMessage(context.Context, string, string, string) error
	DeleteOwnMessage(context.Context, string, string) error
}

type sdkClanDestinationValidator interface {
	ValidateClanDestination(context.Context, string, string) error
}

type sdkInteractiveClient interface {
	SendInteractive(context.Context, string, string, channels.InteractiveMessage) (string, error)
}

type sdkInteraction struct {
	Kind      string
	ClanID    string
	ChannelID string
	MessageID string
	SenderID  string
	OwnerID   string
	ControlID string
	Values    []string
	ExtraData string
}

type sdkInteractionSubscriber interface {
	OnInteraction(func(*sdkInteraction)) func()
}

type sdkInteractionSource struct {
	SenderID    string
	TopicID     string
	Text        string
	ClanName    string
	ChannelName string
}

type sdkInteractionSourceFetcher interface {
	FetchInteractionSource(context.Context, string, string) (sdkInteractionSource, error)
}

type liveSDKClient struct {
	client *mezonsdk.MezonClient
	botID  string
}

func (c *liveSDKClient) LoginContext(ctx context.Context) error { return c.client.LoginContext(ctx) }
func (c *liveSDKClient) Close()                                 { c.client.Close() }
func (c *liveSDKClient) OnChannelMessage(handler func(*mezonsdk.ChannelMessage)) func() {
	return c.client.OnChannelMessage(handler)
}

func (c *liveSDKClient) OnInteraction(handler func(*sdkInteraction)) func() {
	if handler == nil {
		return func() {}
	}
	dispatch := func(kind, channelID, messageID, senderID, ownerID, controlID, extraData string, values []string) {
		channel, err := c.client.Channels.Fetch(channelID)
		if err != nil || channel == nil || channel.Clan == nil || channel.Clan.ID == "" || channel.Clan.ID == "0" {
			slog.Warn("mezon: dropped interaction without trusted clan", "channel_id", channelID, "kind", kind, "error", err)
			return
		}
		handler(&sdkInteraction{
			Kind: kind, ClanID: channel.Clan.ID, ChannelID: channelID, MessageID: messageID,
			SenderID: senderID, OwnerID: ownerID, ControlID: controlID,
			Values: append([]string(nil), values...), ExtraData: extraData,
		})
	}
	unsubButton := c.client.OnMessageButtonClicked(func(event *mezonsdk.MessageButtonClick) {
		if event != nil {
			dispatch("button", event.ChannelID, event.MessageID, event.SenderID, event.UserID, event.ButtonID, event.ExtraData, nil)
		}
	})
	unsubSelect := c.client.OnDropdownBoxSelected(func(event *mezonsdk.DropdownBoxSelect) {
		if event != nil {
			dispatch("select", event.ChannelID, event.MessageID, event.SenderID, event.UserID, event.SelectID, "", event.Values)
		}
	})
	return func() {
		unsubButton()
		unsubSelect()
	}
}

func (c *liveSDKClient) FetchInteractionSource(ctx context.Context, channelID, messageID string) (sdkInteractionSource, error) {
	message, err := c.fetchMessage(ctx, channelID, messageID)
	if err != nil {
		return sdkInteractionSource{}, err
	}
	source := sdkInteractionSource{SenderID: message.SenderID, TopicID: strings.TrimSpace(message.TopicID)}
	parsed := mezonsdk.ParseContent(message.Content)
	source.Text = strings.TrimSpace(parsed.Text)
	if source.Text == "" && len(message.Content) > 0 {
		source.Text = strings.TrimSpace(string(message.Content))
	}
	if message.Channel != nil {
		source.ChannelName = strings.TrimSpace(message.Channel.Name)
		if message.Channel.Clan != nil {
			source.ClanName = strings.TrimSpace(message.Channel.Clan.Name)
		}
	}
	return source, nil
}
func (c *liveSDKClient) Send(ctx context.Context, channelID, content string) error {
	_, err := c.SendMessage(ctx, channelID, content, "", "")
	return err
}

func (c *liveSDKClient) SendMessage(ctx context.Context, channelID, content, topicID, replyToID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	channel, err := c.client.Channels.Fetch(channelID)
	if err != nil {
		return "", fmt.Errorf("fetch mezon channel %s: %w", channelID, err)
	}
	if channel == nil {
		return "", fmt.Errorf("fetch mezon channel %s: %w", channelID, errors.New("empty channel"))
	}
	var message *mezonsdk.Message
	if replyToID != "" {
		if channel.Messages == nil {
			return "", errors.New("mezon channel message cache unavailable")
		}
		source, fetchErr := channel.Messages.Fetch(replyToID)
		if fetchErr != nil {
			return "", fmt.Errorf("fetch mezon reply target %s: %w", replyToID, fetchErr)
		}
		if source == nil {
			return "", fmt.Errorf("fetch mezon reply target %s: %w", replyToID, errors.New("empty message"))
		}
		message, err = source.Reply(mezonsdk.Text(content), &mezonsdk.SendOptions{TopicID: topicID})
	} else {
		message, err = channel.Send(mezonsdk.Text(content), &mezonsdk.SendOptions{TopicID: topicID})
	}
	if err != nil {
		return "", fmt.Errorf("send mezon message: %w", err)
	}
	if message == nil {
		return "", nil
	}
	return message.MessageID(), ctx.Err()
}

func (c *liveSDKClient) SendInteractive(ctx context.Context, channelID, topicID string, spec channels.InteractiveMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	channel, err := c.client.Channels.Fetch(channelID)
	if err != nil {
		return "", fmt.Errorf("fetch mezon channel %s: %w", channelID, err)
	}
	if channel == nil {
		return "", fmt.Errorf("fetch mezon channel %s: empty channel", channelID)
	}
	content, err := buildInteractiveContent(spec)
	if err != nil {
		return "", err
	}
	message, err := channel.Send(content, &mezonsdk.SendOptions{TopicID: topicID})
	if err != nil {
		return "", fmt.Errorf("send mezon interactive message: %w", err)
	}
	if message == nil {
		return "", nil
	}
	return message.MessageID(), ctx.Err()
}

func buildInteractiveContent(spec channels.InteractiveMessage) (mezonsdk.Content, error) {
	rows := make([][]map[string]any, 0, len(spec.ButtonRows))
	for _, row := range spec.ButtonRows {
		builder := mezonsdk.NewButtonBuilder()
		for _, button := range row {
			style, err := mezonButtonStyle(button.Style)
			if err != nil {
				return nil, err
			}
			builder.AddButton(button.ID, button.Label, style)
		}
		rows = append(rows, builder.Build())
	}

	var embed map[string]any
	if spec.Embed != nil {
		builder := mezonsdk.NewInteractiveBuilder(spec.Embed.Title)
		builder.SetDescription(spec.Embed.Description)
		if spec.Embed.Author != nil {
			builder.SetAuthor(spec.Embed.Author.Name, spec.Embed.Author.IconURL, spec.Embed.Author.URL)
		}
		if spec.Embed.ThumbnailURL != "" {
			builder.SetThumbnail(spec.Embed.ThumbnailURL)
		}
		if spec.Embed.Image != nil {
			builder.SetImage(spec.Embed.Image.URL, spec.Embed.Image.Width, spec.Embed.Image.Height)
		}
		for _, field := range spec.Embed.Fields {
			builder.AddField(field.Name, field.Value, field.Inline)
		}
		for _, input := range spec.Embed.Inputs {
			if err := addMezonInteractiveInput(builder, input); err != nil {
				return nil, err
			}
		}
		embed = builder.Build()
	}

	content := map[string]any{}
	if spec.Text != "" {
		content["t"] = spec.Text
	}
	if embed != nil {
		content["embed"] = []map[string]any{embed}
	}
	if actionRows := mezonsdk.ActionRows(rows...); len(actionRows) > 0 {
		content["components"] = actionRows
	}
	return content, nil
}

func addMezonInteractiveInput(builder *mezonsdk.InteractiveBuilder, input channels.InteractiveInput) error {
	switch input.Kind {
	case "input":
		builder.AddInputField(input.ID, input.Name, input.Placeholder, &mezonsdk.InputFieldOption{
			DefaultValue: input.DefaultValue, Type: input.InputType, Textarea: input.Textarea, Disabled: input.Disabled,
		}, input.Description)
	case "select":
		opts := make([]mezonsdk.SelectFieldOption, 0, len(input.Options))
		var selected *mezonsdk.SelectFieldOption
		for _, option := range input.Options {
			converted := mezonsdk.SelectFieldOption{Label: option.Label, Value: option.Value}
			opts = append(opts, converted)
			if option.Value == input.Selected {
				copy := converted
				selected = &copy
			}
		}
		builder.AddSelectField(input.ID, input.Name, opts, selected, input.Description)
	case "radio":
		opts := make([]mezonsdk.RadioFieldOption, 0, len(input.Options))
		for _, option := range input.Options {
			style, err := mezonButtonStyle(option.Style)
			if err != nil {
				return err
			}
			opts = append(opts, mezonsdk.RadioFieldOption{
				Label: option.Label, Value: option.Value, Name: option.Name,
				Description: option.Description, Style: style, Disabled: option.Disabled,
			})
		}
		builder.AddRadioField(input.ID, input.Name, opts, input.Description, input.MaxOptions)
	case "date":
		builder.AddDatePickerField(input.ID, input.Name, input.Description)
	case "animation":
		builder.AddAnimation(input.ID, mezonsdk.AnimationConfig{
			URLImage: input.URLImage, URLPosition: input.URLPosition, Pool: input.Pool,
			Repeat: input.Repeat, Duration: input.Duration,
		}, input.Name, input.Description)
	default:
		return fmt.Errorf("unsupported mezon interactive input %q", input.Kind)
	}
	return nil
}

func mezonButtonStyle(style string) (mezonsdk.EButtonMessageStyle, error) {
	switch style {
	case "", "primary":
		return mezonsdk.ButtonPrimary, nil
	case "secondary":
		return mezonsdk.ButtonSecondary, nil
	case "success":
		return mezonsdk.ButtonSuccess, nil
	case "danger":
		return mezonsdk.ButtonDanger, nil
	case "link":
		return mezonsdk.ButtonLink, nil
	default:
		return 0, fmt.Errorf("unsupported mezon button style %q", style)
	}
}

func (c *liveSDKClient) UpdateMessage(ctx context.Context, channelID, messageID, content, topicID string) error {
	message, err := c.fetchMessage(ctx, channelID, messageID)
	if err != nil {
		return err
	}
	if err := ensureBotOwnsMessage(message, c.botID); err != nil {
		return err
	}
	if _, err := message.Update(mezonsdk.Text(content), nil, nil); err != nil {
		return fmt.Errorf("update mezon message: %w", err)
	}
	return nil
}

func (c *liveSDKClient) ReactMessage(ctx context.Context, channelID, messageID, emoji string) error {
	message, err := c.fetchMessage(ctx, channelID, messageID)
	if err != nil {
		return err
	}
	if _, err := message.React(mezonsdk.ReactPayload{Emoji: emoji, Count: 1}); err != nil {
		return fmt.Errorf("react to mezon message: %w", err)
	}
	return ctx.Err()
}

func (c *liveSDKClient) DeleteOwnMessage(ctx context.Context, channelID, messageID string) error {
	message, err := c.fetchMessage(ctx, channelID, messageID)
	if err != nil {
		return err
	}
	if err := ensureBotOwnsMessage(message, c.botID); err != nil {
		return err
	}
	if _, err := message.Delete(); err != nil {
		return fmt.Errorf("delete mezon message: %w", err)
	}
	return ctx.Err()
}

func (c *liveSDKClient) fetchMessage(ctx context.Context, channelID, messageID string) (*mezonsdk.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	channel, err := c.client.Channels.Fetch(channelID)
	if err != nil {
		return nil, fmt.Errorf("fetch mezon channel %s: %w", channelID, err)
	}
	if channel == nil || channel.Messages == nil {
		return nil, errors.New("mezon channel message cache unavailable")
	}
	message, err := channel.Messages.Fetch(messageID)
	if err != nil {
		return nil, fmt.Errorf("fetch mezon message %s: %w", messageID, err)
	}
	if message == nil {
		return nil, fmt.Errorf("fetch mezon message %s: empty message", messageID)
	}
	return message, nil
}

func ensureBotOwnsMessage(message *mezonsdk.Message, botID string) error {
	if message == nil || botID == "" || message.SenderID != botID {
		return errors.New("mezon can edit or delete only messages owned by this bot")
	}
	return nil
}

func (c *liveSDKClient) ValidateClanDestination(ctx context.Context, channelID, clanID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	channel, err := c.client.Channels.Fetch(channelID)
	if err != nil {
		return fmt.Errorf("fetch mezon destination channel %s: %w", channelID, err)
	}
	if channel == nil || channel.Clan == nil || channel.Clan.ID == "" || channel.Clan.ID == "0" {
		return fmt.Errorf("mezon destination channel %s is not inside a clan", channelID)
	}
	if channel.Clan.ID != clanID {
		return fmt.Errorf("mezon destination channel %s is outside source clan", channelID)
	}
	return nil
}

// Channel is a GoClaw channel backed by a Mezon bot session.
type Channel struct {
	*channels.BaseChannel
	config           config.MezonConfig
	client           sdkClient
	agentStore       mezonAgentStore
	configPermStore  store.ConfigPermissionStore
	placeholders     sync.Map
	catalogRefreshMu sync.Mutex
	catalogRefreshed map[string]time.Time
	unsubscribe      func()
	stopOnce         sync.Once
	done             chan struct{}
}

// New creates a Mezon channel. Network I/O starts in Start.
func New(cfg config.MezonConfig, msgBus *bus.MessageBus, pairingSvc store.PairingStore, pendingStore store.PendingMessageStore) (*Channel, error) {
	if strings.TrimSpace(cfg.BotID) == "" {
		return nil, errors.New("mezon bot_id is required")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("mezon token is required")
	}
	client, err := mezonsdk.NewMezonClient(mezonsdk.ClientConfig{
		BotID:  cfg.BotID,
		Token:  cfg.Token,
		Host:   cfg.Host,
		Port:   cfg.Port,
		UseSSL: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("create mezon client: %w", err)
	}
	return newWithClient(cfg, msgBus, pairingSvc, pendingStore, &liveSDKClient{client: client, botID: cfg.BotID}), nil
}

func newWithClient(cfg config.MezonConfig, msgBus *bus.MessageBus, pairingSvc store.PairingStore, pendingStore store.PendingMessageStore, client sdkClient) *Channel {
	base := channels.NewBaseChannel(channels.TypeMezon, msgBus, cfg.AllowFrom)
	base.ValidatePolicy(cfg.DMPolicy, cfg.GroupPolicy)
	requireMention := true
	if cfg.RequireMention != nil {
		requireMention = *cfg.RequireMention
	}
	base.SetRequireMention(requireMention)
	base.SetHistoryLimit(cfg.HistoryLimit)
	base.SetPairingService(pairingSvc)
	base.SetGroupHistory(channels.MakeHistory(channels.TypeMezon, pendingStore, base.TenantID()))
	return &Channel{BaseChannel: base, config: cfg, client: client, done: make(chan struct{})}
}

// BlockReplyEnabled returns the per-channel block_reply override.
func (c *Channel) BlockReplyEnabled() *bool { return c.config.BlockReply }

// ChatBehaviorConfig returns the per-channel chat_behavior override.
func (c *Channel) ChatBehaviorConfig() *config.ChatBehaviorConfig { return c.config.ChatBehavior }

// SetPendingHistoryTenantID propagates the DB instance tenant to Mezon's
// pending group history, which is constructed before InstanceLoader assigns it.
func (c *Channel) SetPendingHistoryTenantID(id uuid.UUID) {
	if gh := c.GroupHistory(); gh != nil {
		gh.SetTenantID(id)
	}
}

func (c *Channel) EditMessage(ctx context.Context, chatID, messageID, content string) error {
	return c.client.UpdateMessage(ctx, chatID, messageID, content, "")
}

func (c *Channel) ReactToMessage(ctx context.Context, chatID, messageID, emoji string) error {
	if strings.TrimSpace(emoji) == "" {
		return errors.New("mezon reaction emoji is required")
	}
	return c.client.ReactMessage(ctx, chatID, messageID, emoji)
}

func (c *Channel) DeleteMessage(ctx context.Context, chatID, messageID string) error {
	return c.client.DeleteOwnMessage(ctx, chatID, messageID)
}

func (c *Channel) SendInteractiveMessage(ctx context.Context, chatID, clanID string, message channels.InteractiveMessage) (string, error) {
	if !c.IsRunning() {
		return "", errors.New("mezon bot not running")
	}
	if err := c.validateOutboundClan(ctx, bus.OutboundMessage{
		ChatID:   chatID,
		Metadata: map[string]string{bus.MetaSourceContainerID: clanID, "clan_id": clanID, "group_id": chatID},
	}); err != nil {
		return "", err
	}
	client, ok := c.client.(sdkInteractiveClient)
	if !ok {
		return "", errors.New("mezon SDK client does not support interactive messages")
	}
	return client.SendInteractive(ctx, chatID, mezonTopicFromLocalKey(chatID, tools.ToolLocalKeyFromCtx(ctx)), message)
}

func mezonTopicFromLocalKey(channelID, localKey string) string {
	prefix := channelID + ":thread:"
	if strings.HasPrefix(localKey, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(localKey, prefix))
	}
	return ""
}

// Start authenticates and opens the Mezon realtime session.
func (c *Channel) Start(ctx context.Context) error {
	if c.client == nil {
		return errors.New("mezon client is not configured")
	}
	c.GroupHistory().StartFlusher()
	unsubMessage := c.client.OnChannelMessage(c.handleMessage)
	unsubInteraction := func() {}
	if subscriber, ok := c.client.(sdkInteractionSubscriber); ok {
		unsubInteraction = subscriber.OnInteraction(c.handleInteraction)
	}
	c.unsubscribe = func() {
		unsubMessage()
		unsubInteraction()
	}
	if err := c.client.LoginContext(ctx); err != nil {
		if c.unsubscribe != nil {
			c.unsubscribe()
			c.unsubscribe = nil
		}
		c.GroupHistory().StopFlusher()
		c.client.Close()
		return fmt.Errorf("login mezon bot: %w", err)
	}
	c.SetRunning(true)
	slog.Info("mezon bot connected", "channel", c.Name(), "bot_id", c.config.BotID)
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Stop(context.Background())
		case <-c.done:
		}
	}()
	return nil
}

func (c *Channel) handleInteraction(event *sdkInteraction) {
	if event == nil || event.ClanID == "" || event.ClanID == "0" || event.ChannelID == "" || event.MessageID == "" || event.ControlID == "" || event.SenderID == "" || event.SenderID == c.config.BotID {
		return
	}
	if event.Kind != "button" && event.Kind != "select" {
		return
	}
	ctx := store.WithTenantID(context.Background(), c.TenantID())
	// Mezon broadcasts component events visible to the bot. Accept only events
	// addressed to this bot and independently verify the source message owner.
	if event.OwnerID != c.config.BotID {
		return
	}
	fetcher, ok := c.client.(sdkInteractionSourceFetcher)
	if !ok {
		slog.Warn("mezon: dropped interaction without source verification", "channel_id", event.ChannelID, "message_id", event.MessageID)
		return
	}
	source, err := fetcher.FetchInteractionSource(ctx, event.ChannelID, event.MessageID)
	if err != nil || source.SenderID != c.config.BotID {
		slog.Warn("mezon: dropped interaction from unowned source", "channel_id", event.ChannelID, "message_id", event.MessageID, "error", err)
		return
	}
	policyMessage := &mezonsdk.ChannelMessage{SenderID: event.SenderID, ChannelID: event.ChannelID, ClanID: event.ClanID}
	if !c.policyAllows(ctx, policyMessage, false, true) {
		return
	}
	content := fmt.Sprintf("User activated Mezon %s %q on bot message %q.", event.Kind, event.ControlID, event.MessageID)
	if len(event.Values) > 0 {
		values, _ := json.Marshal(event.Values)
		content = fmt.Sprintf("User selected Mezon %s %q with values %s on message %q.", event.Kind, event.ControlID, values, event.MessageID)
	}
	if source.Text != "" {
		content += "\nOriginal interactive message: " + truncateInteractionContext(source.Text, 1000)
	}
	metadata := map[string]string{
		"message_id": event.MessageID, "channel_id": event.ChannelID, "clan_id": event.ClanID,
		"is_dm": "false", "interaction_type": event.Kind, "interaction_id": event.ControlID,
	}
	if event.ExtraData != "" {
		metadata["interaction_extra_data"] = event.ExtraData
	}
	if source.TopicID != "" && source.TopicID != "0" {
		metadata["topic_id"] = source.TopicID
		metadata["local_key"] = fmt.Sprintf("%s:thread:%s", event.ChannelID, source.TopicID)
	}
	if title := mezonChatTitle(source.ClanName, source.ChannelName); title != "" {
		metadata[tools.MetaChatTitle] = title
	}
	if len(event.Values) > 0 {
		values, _ := json.Marshal(event.Values)
		metadata["interaction_values"] = string(values)
	}
	if event.MessageID != "" {
		if placeholderID, err := c.client.SendMessage(ctx, event.ChannelID, "⏳ Đang xử lý...", source.TopicID, event.MessageID); err == nil && placeholderID != "" {
			c.placeholders.Store(event.MessageID, placeholderID)
			metadata["placeholder_key"] = event.MessageID
		} else if err != nil {
			slog.Warn("mezon: interaction placeholder send failed", "channel_id", event.ChannelID, "error", err)
		}
	}
	c.HandleAuthorizedMessage(event.SenderID, event.ChannelID, content, nil, metadata, "group")
}

func truncateInteractionContext(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "…"
}

func mezonChatTitle(clanName, channelName string) string {
	clanName = strings.TrimSpace(clanName)
	channelName = strings.TrimSpace(channelName)
	if clanName != "" && channelName != "" {
		return clanName + " / " + channelName
	}
	if channelName != "" {
		return channelName
	}
	return clanName
}

// Stop closes the Mezon session and releases event handlers.
func (c *Channel) Stop(_ context.Context) error {
	c.stopOnce.Do(func() {
		close(c.done)
		c.SetRunning(false)
		if c.unsubscribe != nil {
			c.unsubscribe()
		}
		c.GroupHistory().StopFlusher()
		if c.client != nil {
			c.client.Close()
		}
	})
	return nil
}

// Send delivers an outbound response, splitting long Markdown at safe boundaries.
func (c *Channel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return errors.New("mezon bot not running")
	}
	if msg.ChatID == "" {
		return errors.New("empty chat ID for mezon send")
	}
	if len(msg.Media) > 0 {
		return fmt.Errorf("%w: mezon", channels.ErrMediaUnsupported)
	}
	if err := c.validateOutboundClan(ctx, msg); err != nil {
		return err
	}
	topicID := msg.Metadata["topic_id"]
	chunks := splitOutboundContent(msg.Content)
	placeholderKey := msg.Metadata["placeholder_key"]
	placeholderID := ""
	if placeholderKey != "" {
		if value, ok := c.placeholders.LoadAndDelete(placeholderKey); ok {
			placeholderID, _ = value.(string)
		}
	}
	for i, chunk := range chunks {
		if i == 0 && placeholderID != "" {
			if err := c.client.UpdateMessage(ctx, msg.ChatID, placeholderID, chunk, topicID); err == nil {
				continue
			} else {
				slog.Warn("mezon: placeholder update failed, sending new message", "message_id", placeholderID, "error", err)
			}
		}
		replyToID := ""
		if i == 0 {
			replyToID = placeholderKey
		}
		if _, err := c.client.SendMessage(ctx, msg.ChatID, chunk, topicID, replyToID); err != nil {
			return err
		}
	}
	return nil
}

func (c *Channel) validateOutboundClan(ctx context.Context, msg bus.OutboundMessage) error {
	sourceClanID := strings.TrimSpace(msg.Metadata[bus.MetaSourceContainerID])
	if sourceClanID == "" {
		sourceClanID = strings.TrimSpace(msg.Metadata["clan_id"])
	}
	if sourceClanID == "0" {
		sourceClanID = ""
	}
	isUserGroupDelivery := msg.Metadata["group_id"] != "" || msg.Metadata["is_dm"] == "false" || msg.Metadata["clan_id"] != ""
	if sourceClanID == "" {
		if isUserGroupDelivery {
			return errors.New("mezon group delivery requires a trusted source clan")
		}
		return nil
	}
	validator, ok := c.client.(sdkClanDestinationValidator)
	if !ok {
		return errors.New("mezon SDK client cannot validate destination clan")
	}
	return validator.ValidateClanDestination(ctx, msg.ChatID, sourceClanID)
}

func splitOutboundContent(content string) []string {
	initial := channels.ChunkMarkdown(content, maxOutboundChunkBytes)
	out := make([]string, 0, len(initial))
	var appendSafe func(string)
	appendSafe = func(chunk string) {
		encoded, err := json.Marshal(mezonsdk.Text(chunk))
		if err == nil && mezonsdk.UTF16Len(string(encoded)) <= maxContentWireUnits {
			out = append(out, chunk)
			return
		}
		if len(chunk) <= 1 {
			out = append(out, chunk)
			return
		}
		parts := channels.ChunkMarkdown(chunk, len(chunk)/2)
		if len(parts) < 2 {
			parts = []string{chunk[:len(chunk)/2], chunk[len(chunk)/2:]}
		}
		for _, part := range parts {
			appendSafe(part)
		}
	}
	for _, chunk := range initial {
		appendSafe(chunk)
	}
	return out
}

func (c *Channel) handleMessage(message *mezonsdk.ChannelMessage) {
	if message == nil || message.ChannelID == "" || message.SenderID == "" || message.SenderID == c.config.BotID {
		return
	}
	ctx := store.WithTenantID(context.Background(), c.TenantID())
	direct := message.ClanID == "" || message.ClanID == "0" || message.Mode == int32(mezonsdk.StreamModeDM)
	if c.tryHandleCronCommand(ctx, message, direct) {
		return
	}
	peerKind := "group"
	if direct {
		peerKind = "direct"
	}
	mentioned := c.isMentioned(message)
	if !c.policyAllows(ctx, message, direct, mentioned) {
		return
	}

	content := strings.TrimSpace(message.ContentText())
	if content == "" {
		content = "[empty message]"
	}
	author := strings.TrimSpace(message.DisplayName)
	if author == "" {
		author = strings.TrimSpace(message.ClanNick)
	}
	if author == "" {
		author = strings.TrimSpace(message.Username)
	}
	if author == "" {
		author = message.SenderID
	}
	// Mezon topics map to Discord-style thread sessions. Keep ChatID as the
	// real channel ID so outbound delivery still targets the channel, while
	// local_key scopes the agent session and pending group history to the topic.
	historyKey := message.ChannelID
	localKey := ""
	if !direct && strings.TrimSpace(message.TopicID) != "" && message.TopicID != "0" {
		localKey = fmt.Sprintf("%s:thread:%s", message.ChannelID, strings.TrimSpace(message.TopicID))
		historyKey = localKey
	}

	if !direct && c.RequireMention() && !mentioned {
		c.GroupHistory().Record(historyKey, channels.HistoryEntry{
			Sender:    author,
			SenderID:  message.SenderID,
			Body:      content,
			Timestamp: time.Unix(int64(message.CreateTimeSeconds), 0),
			MessageID: message.MessageID,
		}, c.HistoryLimit())
		return
	}

	if !direct {
		annotated := fmt.Sprintf("[From: %s (@%s)]\n%s", author, message.Username, content)
		content = c.GroupHistory().BuildContext(historyKey, annotated, c.HistoryLimit())
	}
	metadata := map[string]string{
		"message_id":   message.MessageID,
		"channel_id":   message.ChannelID,
		"clan_id":      message.ClanID,
		"display_name": author,
		"username":     message.Username,
		"is_dm":        fmt.Sprintf("%t", direct),
	}
	if title := strings.TrimSpace(message.ChannelLabel); title != "" {
		metadata[tools.MetaChatTitle] = title
	}
	placeholderKey := message.MessageID
	if placeholderKey == "" {
		placeholderKey = message.ID
	}
	if placeholderKey != "" {
		if placeholderID, err := c.client.SendMessage(ctx, message.ChannelID, "⏳ Đang xử lý...", strings.TrimSpace(message.TopicID), placeholderKey); err == nil && placeholderID != "" {
			c.placeholders.Store(placeholderKey, placeholderID)
			metadata["placeholder_key"] = placeholderKey
		} else if err != nil {
			slog.Warn("mezon: placeholder send failed", "channel_id", message.ChannelID, "error", err)
		}
	}
	if localKey != "" {
		metadata["local_key"] = localKey
		metadata["topic_id"] = strings.TrimSpace(message.TopicID)
	}
	c.HandleAuthorizedMessage(message.SenderID, message.ChannelID, content, nil, metadata, peerKind)
	if !direct {
		c.GroupHistory().Clear(historyKey)
	}
}

func (c *Channel) isMentioned(message *mezonsdk.ChannelMessage) bool {
	for _, mention := range message.Mentions {
		if mention.UserID == c.config.BotID {
			return true
		}
	}
	for _, reference := range message.References {
		if reference.MessageSenderID == c.config.BotID {
			return true
		}
	}
	return false
}

func (c *Channel) policyAllows(ctx context.Context, message *mezonsdk.ChannelMessage, direct, mentioned bool) bool {
	var result channels.PolicyResult
	if direct {
		result = c.CheckDMPolicy(ctx, message.SenderID, c.config.DMPolicy)
	} else {
		result = c.CheckGroupPolicy(ctx, message.SenderID, message.ChannelID, c.config.GroupPolicy)
	}
	switch result {
	case channels.PolicyAllow:
		return true
	case channels.PolicyNeedsPairing:
		if !direct && c.RequireMention() && !mentioned {
			return false
		}
		c.sendPairingReply(ctx, message, direct)
	}
	return false
}

func (c *Channel) sendPairingReply(ctx context.Context, message *mezonsdk.ChannelMessage, direct bool) {
	pairing := c.PairingService()
	if pairing == nil {
		return
	}
	senderID := message.SenderID
	if !direct {
		senderID = "group:" + message.ChannelID
	}
	if !c.CanSendPairingNotif(senderID, pairingDebounceTime) {
		return
	}
	code, err := pairing.RequestPairing(ctx, senderID, c.Name(), message.ChannelID, "default", nil)
	if err != nil {
		slog.Warn("security.mezon_pairing_request_failed", "sender_id", senderID, "error", err)
		return
	}
	reply := c.SystemMessage("", systemmessages.KeyPairingAccountRequired, systemmessages.Vars{
		"platform": "Mezon", "sender_id": senderID, "code": code,
	})
	if err := c.client.Send(ctx, message.ChannelID, reply); err != nil {
		slog.Warn("mezon pairing reply failed", "sender_id", senderID, "error", err)
		return
	}
	c.MarkPairingNotifSent(senderID)
}
