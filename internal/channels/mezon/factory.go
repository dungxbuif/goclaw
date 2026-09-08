package mezon

import (
	"encoding/json"
	"fmt"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type mezonCredentials struct {
	BotID string `json:"bot_id"`
	Token string `json:"token"`
}

type mezonInstanceConfig struct {
	Host           string                     `json:"host,omitempty"`
	Port           string                     `json:"port,omitempty"`
	UseSSL         *bool                      `json:"use_ssl,omitempty"`
	AllowFrom      []string                   `json:"allow_from,omitempty"`
	DMPolicy       string                     `json:"dm_policy,omitempty"`
	GroupPolicy    string                     `json:"group_policy,omitempty"`
	RequireMention *bool                      `json:"require_mention,omitempty"`
	HistoryLimit   *int                       `json:"history_limit,omitempty"`
	BlockReply     *bool                      `json:"block_reply,omitempty"`
	ChatBehavior   *config.ChatBehaviorConfig `json:"chat_behavior,omitempty"`
}

// Factory creates a Mezon channel from a channel_instances row.
func Factory(name string, credentials, rawConfig json.RawMessage, msgBus *bus.MessageBus, pairingSvc store.PairingStore) (channels.Channel, error) {
	return buildChannel(name, credentials, rawConfig, msgBus, pairingSvc, nil)
}

func buildChannel(name string, credentials, rawConfig json.RawMessage, msgBus *bus.MessageBus, pairingSvc store.PairingStore, pendingStore store.PendingMessageStore) (channels.Channel, error) {
	var creds mezonCredentials
	if len(credentials) > 0 {
		if err := json.Unmarshal(credentials, &creds); err != nil {
			return nil, fmt.Errorf("decode mezon credentials: %w", err)
		}
	}
	var instance mezonInstanceConfig
	if len(rawConfig) > 0 {
		if err := json.Unmarshal(rawConfig, &instance); err != nil {
			return nil, fmt.Errorf("decode mezon config: %w", err)
		}
	}
	historyLimit := channels.DefaultGroupHistoryLimit
	if instance.HistoryLimit != nil {
		historyLimit = *instance.HistoryLimit
	}
	groupPolicy := instance.GroupPolicy
	if groupPolicy == "" {
		groupPolicy = "pairing"
	}
	channel, err := New(config.MezonConfig{
		Enabled:        true,
		BotID:          creds.BotID,
		Token:          creds.Token,
		Host:           instance.Host,
		Port:           instance.Port,
		UseSSL:         instance.UseSSL,
		AllowFrom:      instance.AllowFrom,
		DMPolicy:       instance.DMPolicy,
		GroupPolicy:    groupPolicy,
		RequireMention: instance.RequireMention,
		HistoryLimit:   historyLimit,
		BlockReply:     instance.BlockReply,
		ChatBehavior:   instance.ChatBehavior,
	}, msgBus, pairingSvc, pendingStore)
	if err != nil {
		return nil, err
	}
	channel.SetName(name)
	channel.SetType(channels.TypeMezon)
	return channel, nil
}

// FactoryWithPendingStore returns a DB factory with persistent group history.
func FactoryWithPendingStore(pendingStore store.PendingMessageStore) channels.ChannelFactory {
	return FactoryWithStores(nil, nil, pendingStore)
}

// FactoryWithStores returns a Mezon factory with agent lookup, scoped cron
// permissions, and persistent pending group history.
func FactoryWithStores(agentStore store.AgentStore, configPermStore store.ConfigPermissionStore, pendingStore store.PendingMessageStore) channels.ChannelFactory {
	return func(name string, credentials, rawConfig json.RawMessage, msgBus *bus.MessageBus, pairingSvc store.PairingStore) (channels.Channel, error) {
		built, err := buildChannel(name, credentials, rawConfig, msgBus, pairingSvc, pendingStore)
		if err != nil {
			return nil, err
		}
		channel := built.(*Channel)
		channel.agentStore = agentStore
		channel.configPermStore = configPermStore
		return channel, nil
	}
}
