package mezon

import (
	"encoding/json"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

func TestFactoryBuildsNamedDBChannel(t *testing.T) {
	requireMention := false
	limit := 12
	creds := json.RawMessage(`{"bot_id":"123","token":"secret"}`)
	cfg, err := json.Marshal(mezonInstanceConfig{
		DMPolicy:       "open",
		GroupPolicy:    "pairing",
		RequireMention: &requireMention,
		HistoryLimit:   &limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := Factory("mezon-prod", creds, cfg, bus.New(), nil)
	if err != nil {
		t.Fatalf("Factory: %v", err)
	}
	got := channel.(*Channel)
	if got.Name() != "mezon-prod" || got.Type() != channels.TypeMezon {
		t.Fatalf("identity = (%q, %q)", got.Name(), got.Type())
	}
	if got.config.BotID != "123" || got.config.Token != "secret" || got.config.DMPolicy != "open" || got.config.GroupPolicy != "pairing" {
		t.Fatalf("config = %+v", got.config)
	}
	if got.RequireMention() || got.HistoryLimit() != 12 {
		t.Fatalf("require mention = %t, history = %d", got.RequireMention(), got.HistoryLimit())
	}
}

func TestFactoryDefaultsGroupPolicySecurely(t *testing.T) {
	channel, err := Factory("mezon-prod", json.RawMessage(`{"bot_id":"123","token":"secret"}`), nil, bus.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := channel.(*Channel)
	if got.config.GroupPolicy != "pairing" || got.HistoryLimit() != channels.DefaultGroupHistoryLimit {
		t.Fatalf("config = %+v, history = %d", got.config, got.HistoryLimit())
	}
}

func TestFactoryRejectsMissingCredentials(t *testing.T) {
	if _, err := Factory("mezon-prod", json.RawMessage(`{"bot_id":"123"}`), nil, bus.New(), nil); err == nil {
		t.Fatal("Factory error = nil")
	}
}
