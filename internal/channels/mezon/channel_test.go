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
	mu        sync.Mutex
	handler   func(*mezonsdk.ChannelMessage)
	loginErr  error
	updateErr error
	closed    bool
	sent      []fakeSDKSend
	updated   []fakeSDKUpdate
}

type fakeSDKSend struct {
	channelID string
	content   string
	topicID   string
	replyToID string
}

type fakeSDKUpdate struct {
	channelID string
	messageID string
	content   string
	topicID   string
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
	_, err := f.SendMessage(context.Background(), channelID, content, "", "")
	return err
}

func (f *fakeSDKClient) SendMessage(_ context.Context, channelID, content, topicID, replyToID string) (string, error) {
	encoded, err := json.Marshal(mezonsdk.Text(content))
	if err != nil {
		return "", err
	}
	if mezonsdk.UTF16Len(string(encoded)) > 8000 {
		return "", errors.New("content exceeds Mezon wire limit")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, fakeSDKSend{channelID: channelID, content: content, topicID: topicID, replyToID: replyToID})
	if content == "⏳ Đang xử lý..." {
		return "placeholder-message-ack", nil
	}
	return "sent-" + content, nil
}

func (f *fakeSDKClient) UpdateMessage(_ context.Context, channelID, messageID, content, topicID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = append(f.updated, fakeSDKUpdate{channelID: channelID, messageID: messageID, content: content, topicID: topicID})
	return f.updateErr
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

func TestGroupTopicUsesIsolatedLocalKeyAndHistory(t *testing.T) {
	requireMention := true
	client := &fakeSDKClient{}
	msgBus := bus.New()
	channel := newWithClient(config.MezonConfig{
		BotID: "bot-1", Token: "token", GroupPolicy: "open",
		RequireMention: &requireMention, HistoryLimit: 20,
	}, msgBus, nil, nil, client)
	if err := channel.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = channel.Stop(context.Background()) })

	client.emit(&mezonsdk.ChannelMessage{
		MessageID: "message-1", ChannelID: "channel-1", ClanID: "clan-1", TopicID: "topic-a",
		SenderID: "user-1", Username: "alice", Content: []byte(`{"t":"topic context"}`),
		Mode: int32(mezonsdk.StreamModeChannel),
	})
	client.emit(&mezonsdk.ChannelMessage{
		MessageID: "message-2", ChannelID: "channel-1", ClanID: "clan-1", TopicID: "topic-a",
		SenderID: "user-2", Username: "bob", Content: []byte(`{"t":"@bot answer"}`),
		Mentions: []mezonsdk.Mention{{UserID: "bot-1", Username: "bot"}},
		Mode:     int32(mezonsdk.StreamModeChannel),
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, ok := msgBus.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("no topic message published")
	}
	if got.Metadata["local_key"] != "channel-1:thread:topic-a" {
		t.Fatalf("local_key = %q, want topic-scoped key", got.Metadata["local_key"])
	}
	if got.Metadata["topic_id"] != "topic-a" || !strings.Contains(got.Content, "topic context") {
		t.Fatalf("topic inbound = %+v", got)
	}
}

func TestMentionedMessageSendsPlaceholderAndFinalEditsIt(t *testing.T) {
	requireMention := true
	client := &fakeSDKClient{}
	msgBus := bus.New()
	channel := newWithClient(config.MezonConfig{BotID: "bot-1", Token: "token", GroupPolicy: "open", RequireMention: &requireMention}, msgBus, nil, nil, client)
	if err := channel.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = channel.Stop(context.Background()) })

	client.emit(&mezonsdk.ChannelMessage{
		MessageID: "message-ack", ChannelID: "channel-1", ClanID: "clan-1", TopicID: "topic-7",
		SenderID: "user-1", Username: "alice", Content: []byte(`{"t":"@bot hello"}`),
		Mentions: []mezonsdk.Mention{{UserID: "bot-1", Username: "bot"}}, Mode: int32(mezonsdk.StreamModeChannel),
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, ok := msgBus.ConsumeInbound(ctx); !ok {
		t.Fatal("no inbound message published")
	}
	if len(client.sent) != 1 || client.sent[0].content != "⏳ Đang xử lý..." || client.sent[0].topicID != "topic-7" {
		t.Fatalf("placeholder sends = %+v", client.sent)
	}
	if err := channel.Send(context.Background(), bus.OutboundMessage{
		ChatID: "channel-1", Content: "final answer",
		Metadata: map[string]string{"placeholder_key": "message-ack", "topic_id": "topic-7"},
	}); err != nil {
		t.Fatalf("final Send: %v", err)
	}
	if len(client.updated) != 1 || client.updated[0].messageID != "placeholder-message-ack" || client.updated[0].content != "final answer" || client.updated[0].topicID != "topic-7" {
		t.Fatalf("placeholder updates = %+v", client.updated)
	}
}

func TestInboundMessagePlaceholderRepliesToUserMessage(t *testing.T) {
	requireMention := true
	client := &fakeSDKClient{}
	msgBus := bus.New()
	channel := newWithClient(config.MezonConfig{
		BotID: "bot-1", Token: "token", GroupPolicy: "open", RequireMention: &requireMention,
	}, msgBus, nil, nil, client)
	if err := channel.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = channel.Stop(context.Background()) })

	client.emit(&mezonsdk.ChannelMessage{
		MessageID: "2088492982985560042", ChannelID: "channel-1", ClanID: "clan-1", TopicID: "topic-7",
		SenderID: "user-1", Username: "alice", Content: []byte(`{"t":"@bot hello"}`),
		Mentions: []mezonsdk.Mention{{UserID: "bot-1", Username: "bot"}}, Mode: int32(mezonsdk.StreamModeChannel),
	})

	if len(client.sent) != 1 {
		t.Fatalf("placeholder sends = %+v, want one", client.sent)
	}
	if client.sent[0].replyToID != "2088492982985560042" {
		t.Fatalf("placeholder reply target = %q, want %q", client.sent[0].replyToID, "2088492982985560042")
	}
}

func TestFinalMessageRepliesToUserWhenPlaceholderUpdateFails(t *testing.T) {
	client := &fakeSDKClient{updateErr: errors.New("update unavailable")}
	channel := newWithClient(config.MezonConfig{BotID: "bot-1", Token: "token"}, bus.New(), nil, nil, client)
	channel.SetRunning(true)
	channel.placeholders.Store("2088492982985560042", "placeholder-99")

	err := channel.Send(context.Background(), bus.OutboundMessage{
		ChatID: "channel-1", Content: "final answer",
		Metadata: map[string]string{"placeholder_key": "2088492982985560042", "topic_id": "topic-7"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(client.sent) != 1 {
		t.Fatalf("fallback sends = %+v, want one", client.sent)
	}
	if client.sent[0].replyToID != "2088492982985560042" {
		t.Fatalf("fallback reply target = %q, want %q", client.sent[0].replyToID, "2088492982985560042")
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
