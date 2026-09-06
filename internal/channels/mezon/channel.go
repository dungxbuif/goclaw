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

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/systemmessages"
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
}

type liveSDKClient struct {
	client *mezonsdk.MezonClient
}

func (c *liveSDKClient) LoginContext(ctx context.Context) error { return c.client.LoginContext(ctx) }
func (c *liveSDKClient) Close()                                 { c.client.Close() }
func (c *liveSDKClient) OnChannelMessage(handler func(*mezonsdk.ChannelMessage)) func() {
	return c.client.OnChannelMessage(handler)
}
func (c *liveSDKClient) Send(ctx context.Context, channelID, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	channel, err := c.client.Channels.Fetch(channelID)
	if err != nil {
		return fmt.Errorf("fetch mezon channel %s: %w", channelID, err)
	}
	if channel == nil {
		return fmt.Errorf("fetch mezon channel %s: %w", channelID, errors.New("empty channel"))
	}
	if _, err := channel.Send(mezonsdk.Text(content), nil); err != nil {
		return fmt.Errorf("send mezon message: %w", err)
	}
	return ctx.Err()
}

// Channel is a GoClaw channel backed by a Mezon bot session.
type Channel struct {
	*channels.BaseChannel
	config      config.MezonConfig
	client      sdkClient
	unsubscribe func()
	stopOnce    sync.Once
	done        chan struct{}
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
	return newWithClient(cfg, msgBus, pairingSvc, pendingStore, &liveSDKClient{client: client}), nil
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

// Start authenticates and opens the Mezon realtime session.
func (c *Channel) Start(ctx context.Context) error {
	if c.client == nil {
		return errors.New("mezon client is not configured")
	}
	c.GroupHistory().StartFlusher()
	c.unsubscribe = c.client.OnChannelMessage(c.handleMessage)
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
	for _, chunk := range splitOutboundContent(msg.Content) {
		if err := c.client.Send(ctx, msg.ChatID, chunk); err != nil {
			return err
		}
	}
	return nil
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

	if !direct && c.RequireMention() && !mentioned {
		c.GroupHistory().Record(message.ChannelID, channels.HistoryEntry{
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
		content = c.GroupHistory().BuildContext(message.ChannelID, annotated, c.HistoryLimit())
	}
	metadata := map[string]string{
		"message_id": message.MessageID,
		"channel_id": message.ChannelID,
		"clan_id":    message.ClanID,
		"username":   message.Username,
		"is_dm":      fmt.Sprintf("%t", direct),
	}
	c.HandleAuthorizedMessage(message.SenderID, message.ChannelID, content, nil, metadata, peerKind)
	if !direct {
		c.GroupHistory().Clear(message.ChannelID)
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
