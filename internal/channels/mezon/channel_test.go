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
	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type fakeSDKClient struct {
	mu                 sync.Mutex
	handler            func(*mezonsdk.ChannelMessage)
	loginErr           error
	updateErr          error
	closed             bool
	sent               []fakeSDKSend
	updated            []fakeSDKUpdate
	channelClans       map[string]string
	validated          []string
	reacted            []fakeSDKReaction
	deleted            []fakeSDKDelete
	interactiveHandler func(*sdkInteraction)
	interactive        []channels.InteractiveMessage
	interactiveTopics  []string
	interactionSources map[string]sdkInteractionSource
}

type fakeSDKReaction struct{ channelID, messageID, emoji string }
type fakeSDKDelete struct{ channelID, messageID string }

func (f *fakeSDKClient) ReactMessage(_ context.Context, channelID, messageID, emoji string) error {
	f.reacted = append(f.reacted, fakeSDKReaction{channelID, messageID, emoji})
	return nil
}

func (f *fakeSDKClient) DeleteOwnMessage(_ context.Context, channelID, messageID string) error {
	f.deleted = append(f.deleted, fakeSDKDelete{channelID, messageID})
	return nil
}

func (f *fakeSDKClient) OnInteraction(handler func(*sdkInteraction)) func() {
	f.mu.Lock()
	f.interactiveHandler = handler
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.interactiveHandler = nil
		f.mu.Unlock()
	}
}

func (f *fakeSDKClient) FetchInteractionSource(_ context.Context, channelID, messageID string) (sdkInteractionSource, error) {
	source, ok := f.interactionSources[channelID+":"+messageID]
	if !ok {
		return sdkInteractionSource{}, errors.New("source message not found")
	}
	return source, nil
}

func (f *fakeSDKClient) SendInteractive(_ context.Context, _ string, topicID string, message channels.InteractiveMessage) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interactive = append(f.interactive, message)
	f.interactiveTopics = append(f.interactiveTopics, topicID)
	return "interactive-1876543210987654321", nil
}

func TestInteractiveMessageStaysInsideCurrentMezonTopic(t *testing.T) {
	client := &fakeSDKClient{channelClans: map[string]string{"channel-1": "clan-1"}}
	channel := newWithClient(testMezonConfig(), bus.New(), nil, nil, client)
	channel.SetRunning(true)
	ctx := tools.WithToolLocalKey(context.Background(), "channel-1:thread:topic-7")
	if _, err := channel.SendInteractiveMessage(ctx, "channel-1", "clan-1", channels.InteractiveMessage{
		Text: "Approve?", ButtonRows: [][]channels.InteractiveButton{{{ID: "yes", Label: "Yes"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(client.interactiveTopics) != 1 || client.interactiveTopics[0] != "topic-7" {
		t.Fatalf("interactive topics = %#v", client.interactiveTopics)
	}
}

func (f *fakeSDKClient) ValidateClanDestination(_ context.Context, channelID, clanID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.validated = append(f.validated, channelID+":"+clanID)
	if destination, ok := f.channelClans[channelID]; ok && destination != clanID {
		return errors.New("destination is outside source clan")
	}
	return nil
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

func (f *fakeSDKClient) emitInteraction(event *sdkInteraction) {
	f.mu.Lock()
	handler := f.interactiveHandler
	f.mu.Unlock()
	if handler != nil {
		handler(event)
	}
}

func TestBuildInteractiveContentCoversNativeComponentShapes(t *testing.T) {
	content, err := buildInteractiveContent(channels.InteractiveMessage{
		Text:       "Choose",
		ButtonRows: [][]channels.InteractiveButton{{{ID: "ok", Label: "OK", Style: "success"}}},
		Embed: &channels.InteractiveEmbed{Title: "Form", Inputs: []channels.InteractiveInput{
			{Kind: "input", ID: "note", Name: "Note", Textarea: true},
			{Kind: "select", ID: "region", Name: "Region", Selected: "vn", Options: []channels.InteractiveOption{{Label: "VN", Value: "vn"}}},
			{Kind: "radio", ID: "plan", Name: "Plan", MaxOptions: 1, Options: []channels.InteractiveOption{{Label: "Pro", Value: "pro", Style: "danger"}}},
			{Kind: "date", ID: "due", Name: "Due"},
			{Kind: "animation", ID: "spin", Name: "Spin", URLImage: "https://example.com/s.png", Pool: []string{"a"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := content.(map[string]any)
	if !ok {
		t.Fatalf("content type = %T", content)
	}
	rows, _ := payload["components"].([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("component rows = %#v", payload["components"])
	}
	embeds, _ := payload["embed"].([]map[string]any)
	fields, _ := embeds[0]["fields"].([]map[string]any)
	if len(fields) != 5 {
		t.Fatalf("interactive fields = %#v", fields)
	}
}

func TestInteractionPublishesClanScopedInboundAndRepliesToClickedMessage(t *testing.T) {
	mb := bus.New()
	client := &fakeSDKClient{
		channelClans: map[string]string{"channel-1": "clan-1"},
		interactionSources: map[string]sdkInteractionSource{
			"channel-1:1876543210987654321": {
				SenderID: "bot-1", TopicID: "topic-7", Text: "Approve deployment?",
				ClanName: "Production", ChannelName: "release-control",
			},
		},
	}
	channel := newWithClient(testMezonConfig(), mb, nil, nil, client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := channel.Start(ctx); err != nil {
		t.Fatal(err)
	}
	client.emitInteraction(&sdkInteraction{Kind: "button", ClanID: "clan-1", ChannelID: "channel-1", MessageID: "1876543210987654321", SenderID: "user-1", OwnerID: "bot-1", ControlID: "confirm", ExtraData: `{"action":"deploy"}`})

	readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
	defer readCancel()
	inbound, ok := mb.ConsumeInbound(readCtx)
	if !ok {
		t.Fatal("interaction did not publish inbound message")
	}
	if inbound.ChatID != "channel-1" || inbound.SenderID != "user-1" || inbound.Metadata["clan_id"] != "clan-1" || inbound.Metadata["interaction_id"] != "confirm" {
		t.Fatalf("inbound = %#v", inbound)
	}
	if inbound.Metadata["topic_id"] != "topic-7" || inbound.Metadata["local_key"] != "channel-1:thread:topic-7" {
		t.Fatalf("topic metadata = %#v", inbound.Metadata)
	}
	if inbound.Metadata[tools.MetaChatTitle] != "Production / release-control" || inbound.Metadata["interaction_extra_data"] != `{"action":"deploy"}` {
		t.Fatalf("semantic metadata = %#v", inbound.Metadata)
	}
	if !strings.Contains(inbound.Content, "confirm") || !strings.Contains(inbound.Content, "Approve deployment?") {
		t.Fatalf("content = %q", inbound.Content)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.sent) != 1 || client.sent[0].replyToID != "1876543210987654321" || client.sent[0].topicID != "topic-7" {
		t.Fatalf("placeholder replies = %#v", client.sent)
	}
}

func TestInteractionRejectsComponentsNotOwnedByBot(t *testing.T) {
	mb := bus.New()
	client := &fakeSDKClient{interactionSources: map[string]sdkInteractionSource{
		"channel-1:message-1": {SenderID: "other-bot", Text: "Untrusted action"},
	}}
	channel := newWithClient(testMezonConfig(), mb, nil, nil, client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := channel.Start(ctx); err != nil {
		t.Fatal(err)
	}
	client.emitInteraction(&sdkInteraction{Kind: "button", ClanID: "clan-1", ChannelID: "channel-1", MessageID: "message-1", SenderID: "user-1", OwnerID: "other-bot", ControlID: "confirm"})

	readCtx, readCancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer readCancel()
	if _, ok := mb.ConsumeInbound(readCtx); ok {
		t.Fatal("interaction from another bot reached inbound bus")
	}
}

func TestGroupMessageCarriesMezonChannelTitle(t *testing.T) {
	mb := bus.New()
	client := &fakeSDKClient{}
	channel := newWithClient(testMezonConfig(), mb, nil, nil, client)
	if err := channel.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = channel.Stop(context.Background()) })
	client.emit(&mezonsdk.ChannelMessage{
		MessageID: "message-1", ChannelID: "channel-1", ChannelLabel: "release-control", ClanID: "clan-1",
		SenderID: "user-1", Username: "alice", Content: []byte(`{"t":"hello"}`), Mode: int32(mezonsdk.StreamModeChannel),
		Mentions: []mezonsdk.Mention{{UserID: "bot-1", Username: "bot"}},
	})
	readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	inbound, ok := mb.ConsumeInbound(readCtx)
	if !ok || inbound.Metadata[tools.MetaChatTitle] != "release-control" {
		t.Fatalf("inbound = %#v", inbound)
	}
}

type tenantCapturePendingStore struct{ tenant uuid.UUID }

func (s *tenantCapturePendingStore) AppendBatch(ctx context.Context, _ []store.PendingMessage) error {
	s.tenant = store.TenantIDFromContext(ctx)
	return nil
}
func (s *tenantCapturePendingStore) ListByKey(context.Context, string, string) ([]store.PendingMessage, error) {
	return nil, nil
}
func (s *tenantCapturePendingStore) DeleteByKey(ctx context.Context, _, _ string) error {
	s.tenant = store.TenantIDFromContext(ctx)
	return nil
}
func (s *tenantCapturePendingStore) Compact(context.Context, []uuid.UUID, *store.PendingMessage) error {
	return nil
}
func (s *tenantCapturePendingStore) DeleteStale(context.Context, time.Duration) (int64, error) {
	return 0, nil
}
func (s *tenantCapturePendingStore) ListArchivedByKey(context.Context, string, string, time.Time, int) ([]store.ArchivedMessage, error) {
	return nil, nil
}
func (s *tenantCapturePendingStore) ListGroups(context.Context) ([]store.PendingMessageGroup, error) {
	return nil, nil
}
func (s *tenantCapturePendingStore) CountAll(context.Context) (int64, error) { return 0, nil }
func (s *tenantCapturePendingStore) CountByKey(context.Context, string, string) (int, error) {
	return 0, nil
}
func (s *tenantCapturePendingStore) ResolveGroupTitles(context.Context, []store.PendingMessageGroup) (map[string]string, error) {
	return nil, nil
}

func TestSetPendingHistoryTenantIDScopesMezonPersistence(t *testing.T) {
	pending := &tenantCapturePendingStore{}
	channel := newWithClient(testMezonConfig(), bus.New(), nil, pending, &fakeSDKClient{})
	tenantID := uuid.New()
	channel.SetPendingHistoryTenantID(tenantID)
	channel.GroupHistory().Clear("channel-1")
	if pending.tenant != tenantID {
		t.Fatalf("pending history tenant = %s, want %s", pending.tenant, tenantID)
	}
}

func TestInteractionDropsDMUnknownClanAndBotEvents(t *testing.T) {
	mb := bus.New()
	client := &fakeSDKClient{}
	channel := newWithClient(testMezonConfig(), mb, nil, nil, client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := channel.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for _, event := range []*sdkInteraction{
		{Kind: "button", ChannelID: "dm", SenderID: "user", ControlID: "x"},
		{Kind: "button", ClanID: "clan-1", ChannelID: "channel-1", SenderID: testMezonConfig().BotID, ControlID: "x"},
	} {
		client.emitInteraction(event)
	}
	readCtx, readCancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer readCancel()
	if _, ok := mb.ConsumeInbound(readCtx); ok {
		t.Fatal("untrusted interaction reached inbound bus")
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

func TestSendRejectsCrossClanBeforeNetworkDelivery(t *testing.T) {
	client := &fakeSDKClient{channelClans: map[string]string{"channel-2": "clan-2"}}
	channel := newWithClient(testMezonConfig(), bus.New(), nil, nil, client)
	channel.SetRunning(true)
	err := channel.Send(context.Background(), bus.OutboundMessage{
		ChatID: "channel-2", Content: "secret",
		Metadata: map[string]string{bus.MetaSourceContainerID: "clan-1", "group_id": "channel-2"},
	})
	if err == nil || !strings.Contains(err.Error(), "outside source clan") {
		t.Fatalf("error = %v", err)
	}
	if len(client.sent) != 0 || len(client.updated) != 0 {
		t.Fatalf("network calls escaped guard: sent=%#v updated=%#v", client.sent, client.updated)
	}
}

func TestMezonEditReactDeletePreserveSnowflakeID(t *testing.T) {
	client := &fakeSDKClient{}
	channel := newWithClient(testMezonConfig(), bus.New(), nil, nil, client)
	const messageID = "2088492982985560042"
	if err := channel.EditMessage(context.Background(), "channel-1", messageID, "updated"); err != nil {
		t.Fatal(err)
	}
	if err := channel.ReactToMessage(context.Background(), "channel-1", messageID, "👍"); err != nil {
		t.Fatal(err)
	}
	if err := channel.DeleteMessage(context.Background(), "channel-1", messageID); err != nil {
		t.Fatal(err)
	}
	if len(client.updated) != 1 || client.updated[0].messageID != messageID || len(client.reacted) != 1 || client.reacted[0].messageID != messageID || len(client.deleted) != 1 || client.deleted[0].messageID != messageID {
		t.Fatalf("updated=%#v reacted=%#v deleted=%#v", client.updated, client.reacted, client.deleted)
	}
}

func TestEnsureBotOwnsMessageFailsClosed(t *testing.T) {
	if err := ensureBotOwnsMessage(&mezonsdk.Message{SenderID: "bot-1"}, "bot-1"); err != nil {
		t.Fatal(err)
	}
	for _, message := range []*mezonsdk.Message{nil, {}, {SenderID: "user-1"}} {
		if err := ensureBotOwnsMessage(message, "bot-1"); err == nil {
			t.Fatalf("message %#v should not be editable/deletable", message)
		}
	}
}

func TestSendAllowsSameClanDestination(t *testing.T) {
	client := &fakeSDKClient{channelClans: map[string]string{"channel-2": "clan-1"}}
	channel := newWithClient(testMezonConfig(), bus.New(), nil, nil, client)
	channel.SetRunning(true)
	err := channel.Send(context.Background(), bus.OutboundMessage{
		ChatID: "channel-2", Content: "hello",
		Metadata: map[string]string{bus.MetaSourceContainerID: "clan-1", "group_id": "channel-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.sent) != 1 {
		t.Fatalf("sent = %#v", client.sent)
	}
}

func TestSendRejectsGroupDeliveryWithoutTrustedClan(t *testing.T) {
	client := &fakeSDKClient{}
	channel := newWithClient(testMezonConfig(), bus.New(), nil, nil, client)
	channel.SetRunning(true)
	err := channel.Send(context.Background(), bus.OutboundMessage{ChatID: "channel-2", Content: "hello", Metadata: map[string]string{"group_id": "channel-2"}})
	if err == nil || !strings.Contains(err.Error(), "trusted source clan") {
		t.Fatalf("error = %v", err)
	}
	if len(client.sent) != 0 {
		t.Fatalf("sent = %#v", client.sent)
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
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.handler != nil || client.interactiveHandler != nil {
		t.Fatal("message and interaction handlers must be unsubscribed on stop")
	}
}

func TestInboundPublishesResolvedDisplayNameForContactPersistence(t *testing.T) {
	client := &fakeSDKClient{}
	msgBus := bus.New()
	channel := newWithClient(config.MezonConfig{
		BotID:    "bot-1",
		Token:    "token",
		DMPolicy: "open",
	}, msgBus, nil, nil, client)
	if err := channel.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = channel.Stop(context.Background()) })

	client.emit(&mezonsdk.ChannelMessage{
		MessageID:   "message-name",
		ChannelID:   "dm-1",
		ClanID:      "0",
		SenderID:    "user-1",
		DisplayName: "Alice Nguyen",
		Username:    "alice",
		Content:     []byte(`{"t":"hello"}`),
		Mode:        int32(mezonsdk.StreamModeDM),
	})

	consumeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, ok := msgBus.ConsumeInbound(consumeCtx)
	if !ok {
		t.Fatal("no inbound message published")
	}
	if got.Metadata["display_name"] != "Alice Nguyen" {
		t.Fatalf("display_name metadata = %q, want %q", got.Metadata["display_name"], "Alice Nguyen")
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

func TestMentionedMessageSendsFinalReplyBeforeRemovingPlaceholder(t *testing.T) {
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
	if len(client.sent) != 2 {
		t.Fatalf("sends = %+v, want placeholder and final reply", client.sent)
	}
	final := client.sent[1]
	if final.content != "final answer" || final.topicID != "topic-7" || final.replyToID != "message-ack" {
		t.Fatalf("final send = %+v, want reply to original message in topic", final)
	}
	if len(client.updated) != 0 {
		t.Fatalf("placeholder updates = %+v, want none", client.updated)
	}
	if len(client.deleted) != 1 || client.deleted[0].channelID != "channel-1" || client.deleted[0].messageID != "placeholder-message-ack" {
		t.Fatalf("placeholder deletes = %+v", client.deleted)
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
