package mezon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	mezonsdk "github.com/dungxbuif/mezon-sdk-go"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/config"
)

type fakeSDKClient struct {
	mu       sync.Mutex
	handler  func(*mezonsdk.ChannelMessage)
	loginErr error
	closed   bool
	sent     []fakeSDKSend
}

type fakeSDKSend struct {
	channelID string
	content   string
}

func (f *fakeSDKClient) LoginContext(context.Context) error { return f.loginErr }

func (f *fakeSDKClient) Close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
}

func (f *fakeSDKClient) OnChannelMessage(handler func(*mezonsdk.ChannelMessage)) func() {
	f.mu.Lock()
	f.handler = handler
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.handler = nil
		f.mu.Unlock()
	}
}

func (f *fakeSDKClient) Send(_ context.Context, channelID, content string) error {
	encoded, err := json.Marshal(mezonsdk.Text(content))
	if err != nil {
		return err
	}
	if mezonsdk.UTF16Len(string(encoded)) > 8000 {
		return errors.New("content exceeds Mezon wire limit")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, fakeSDKSend{channelID: channelID, content: content})
	return nil
}

func (f *fakeSDKClient) emit(message *mezonsdk.ChannelMessage) {
	f.mu.Lock()
	handler := f.handler
	f.mu.Unlock()
	if handler != nil {
		handler(message)
	}
}

func TestNewRejectsMissingCredentials(t *testing.T) {
	for _, cfg := range []config.MezonConfig{
		{Token: "token"},
		{BotID: "bot"},
	} {
		if _, err := New(cfg, bus.New(), nil, nil); err == nil {
			t.Fatalf("New(%+v) error = nil", cfg)
		}
	}
}

func TestConfigBasedChannelPreservesExplicitZeroHistory(t *testing.T) {
	channel := newWithClient(config.MezonConfig{BotID: "bot", Token: "token", HistoryLimit: 0}, bus.New(), nil, nil, &fakeSDKClient{})
	if channel.HistoryLimit() != 0 {
		t.Fatalf("history limit = %d, want 0", channel.HistoryLimit())
	}
}

func TestStartPublishesDirectMessageAndStopClosesSDK(t *testing.T) {
	client := &fakeSDKClient{}
	msgBus := bus.New()
	channel := newWithClient(config.MezonConfig{
		BotID:    "bot-1",
		Token:    "token",
		DMPolicy: "open",
	}, msgBus, nil, nil, client)

	ctx := t.Context()
	if err := channel.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	client.emit(&mezonsdk.ChannelMessage{
		MessageID: "message-1",
		ChannelID: "dm-1",
		ClanID:    "0",
		SenderID:  "user-1",
		Username:  "alice",
		Content:   []byte(`{"t":"hello"}`),
		Mode:      int32(mezonsdk.StreamModeDM),
	})

	consumeCtx, consumeCancel := context.WithTimeout(context.Background(), time.Second)
	defer consumeCancel()
	got, ok := msgBus.ConsumeInbound(consumeCtx)
	if !ok {
		t.Fatal("no inbound message published")
	}
	if got.Channel != channels.TypeMezon || got.ChatID != "dm-1" || got.SenderID != "user-1" || got.Content != "hello" || got.PeerKind != "direct" {
		t.Fatalf("inbound = %+v", got)
	}
	if got.Metadata["message_id"] != "message-1" || got.Metadata["clan_id"] != "0" {
		t.Fatalf("metadata = %+v", got.Metadata)
	}

	if err := channel.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !client.closed || channel.IsRunning() {
		t.Fatalf("closed = %t, running = %t", client.closed, channel.IsRunning())
	}
}

func TestInboundIgnoresOwnMessages(t *testing.T) {
	client := &fakeSDKClient{}
	msgBus := bus.New()
	channel := newWithClient(config.MezonConfig{BotID: "bot-1", Token: "token", DMPolicy: "open"}, msgBus, nil, nil, client)
	if err := channel.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = channel.Stop(context.Background()) })

	client.emit(&mezonsdk.ChannelMessage{ChannelID: "dm-1", ClanID: "0", SenderID: "bot-1", Content: []byte(`{"t":"echo"}`)})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if got, ok := msgBus.ConsumeInbound(ctx); ok {
		t.Fatalf("published own message: %+v", got)
	}
}

func TestGroupRequiresMentionAndCarriesPendingHistory(t *testing.T) {
	requireMention := true
	client := &fakeSDKClient{}
	msgBus := bus.New()
	channel := newWithClient(config.MezonConfig{
		BotID:          "bot-1",
		Token:          "token",
		GroupPolicy:    "open",
		RequireMention: &requireMention,
		HistoryLimit:   20,
	}, msgBus, nil, nil, client)
	if err := channel.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = channel.Stop(context.Background()) })

	client.emit(&mezonsdk.ChannelMessage{
		MessageID: "message-1", ChannelID: "channel-1", ClanID: "clan-1",
		SenderID: "user-1", Username: "alice", Content: []byte(`{"t":"earlier context"}`),
		Mode: int32(mezonsdk.StreamModeChannel),
	})
	client.emit(&mezonsdk.ChannelMessage{
		MessageID: "message-2", ChannelID: "channel-1", ClanID: "clan-1",
		SenderID: "user-2", Username: "bob", Content: []byte(`{"t":"@bot help"}`),
		Mentions: []mezonsdk.Mention{{UserID: "bot-1", Username: "bot"}},
		Mode:     int32(mezonsdk.StreamModeChannel),
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, ok := msgBus.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("no mentioned group message published")
	}
	if got.PeerKind != "group" || !strings.Contains(got.Content, "earlier context") || !strings.Contains(got.Content, "@bot help") {
		t.Fatalf("group inbound = %+v", got)
	}
}

func TestSendChunksMessagesWithinMezonLimit(t *testing.T) {
	client := &fakeSDKClient{}
	channel := newWithClient(config.MezonConfig{BotID: "bot-1", Token: "token"}, bus.New(), nil, nil, client)
	channel.SetRunning(true)

	err := channel.Send(context.Background(), bus.OutboundMessage{ChatID: "channel-1", Content: strings.Repeat("a", 16000)})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(client.sent) < 3 {
		t.Fatalf("sent %d chunks, want at least 3", len(client.sent))
	}
	for _, sent := range client.sent {
		if sent.channelID != "channel-1" || mezonsdk.UTF16Len(sent.content) > maxOutboundChunkBytes {
			t.Fatalf("invalid chunk: channel=%q units=%d", sent.channelID, mezonsdk.UTF16Len(sent.content))
		}
	}
}

func TestSendAccountsForJSONEscapingInMezonLimit(t *testing.T) {
	client := &fakeSDKClient{}
	channel := newWithClient(config.MezonConfig{BotID: "bot-1", Token: "token"}, bus.New(), nil, nil, client)
	channel.SetRunning(true)
	if err := channel.Send(context.Background(), bus.OutboundMessage{ChatID: "channel-1", Content: strings.Repeat(`"`, 8000)}); err != nil {
		t.Fatalf("Send quoted content: %v", err)
	}
	if len(client.sent) < 3 {
		t.Fatalf("sent %d escaped chunks, want at least 3", len(client.sent))
	}
}

func TestSendRejectsMediaExplicitly(t *testing.T) {
	channel := newWithClient(config.MezonConfig{BotID: "bot-1", Token: "token"}, bus.New(), nil, nil, &fakeSDKClient{})
	channel.SetRunning(true)
	err := channel.Send(context.Background(), bus.OutboundMessage{ChatID: "channel-1", Content: "caption", Media: []bus.MediaAttachment{{URL: "/tmp/file"}}})
	if !errors.Is(err, channels.ErrMediaUnsupported) {
		t.Fatalf("Send media error = %v", err)
	}
}

func TestStartFailureCleansUpClient(t *testing.T) {
	client := &fakeSDKClient{loginErr: errors.New("login failed")}
	channel := newWithClient(config.MezonConfig{BotID: "bot-1", Token: "token"}, bus.New(), nil, nil, client)
	if err := channel.Start(context.Background()); err == nil {
		t.Fatal("Start error = nil")
	}
	if !client.closed || channel.IsRunning() {
		t.Fatalf("closed = %t, running = %t", client.closed, channel.IsRunning())
	}
}
