package mezon

import (
	"context"
	"strings"
	"testing"
	"time"

	mezonsdk "github.com/dungxbuif/mezon-sdk-go"
	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type cronPermissionStore struct {
	granted *store.ConfigPermission
	rows    []store.ConfigPermission
}

func (s *cronPermissionStore) CheckPermission(context.Context, uuid.UUID, string, string, string) (bool, error) {
	return false, nil
}
func (s *cronPermissionStore) Grant(_ context.Context, perm *store.ConfigPermission) error {
	copy := *perm
	s.granted = &copy
	s.rows = append(s.rows, copy)
	return nil
}
func (s *cronPermissionStore) Revoke(context.Context, uuid.UUID, string, string, string) error {
	return nil
}
func (s *cronPermissionStore) List(_ context.Context, _ uuid.UUID, configType, scope string) ([]store.ConfigPermission, error) {
	var out []store.ConfigPermission
	for _, row := range s.rows {
		if row.ConfigType == configType && row.Scope == scope {
			out = append(out, row)
		}
	}
	return out, nil
}
func (s *cronPermissionStore) ListFileWriters(context.Context, uuid.UUID, string) ([]store.ConfigPermission, error) {
	return nil, nil
}

func TestMezonAddCronBootstrapsFromRepliedUser(t *testing.T) {
	permissions := &cronPermissionStore{}
	client := &fakeSDKClient{}
	msgBus := bus.New()
	channel := newWithClient(testMezonConfig(), msgBus, nil, nil, client)
	channel.SetName("mezon-prod")
	agentID := uuid.New()
	channel.SetAgentID(agentID.String())
	channel.configPermStore = permissions
	if err := channel.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = channel.Stop(context.Background()) })

	client.emit(&mezonsdk.ChannelMessage{
		MessageID: "command-1", ChannelID: "channel-1", ClanID: "clan-1", TopicID: "topic-7",
		SenderID: "admin-1", Content: []byte(`{"t":"/addcron"}`), Mode: int32(mezonsdk.StreamModeChannel),
		References: []mezonsdk.MessageRef{{MessageSenderID: "user-2", MessageSenderDisplayName: "Alice"}},
	})

	if permissions.granted == nil || permissions.granted.UserID != "user-2" || permissions.granted.Scope != "group:mezon-prod:channel-1" || permissions.granted.ConfigType != store.ConfigTypeCron {
		t.Fatalf("grant = %#v", permissions.granted)
	}
	if len(client.sent) != 1 || client.sent[0].topicID != "topic-7" || client.sent[0].replyToID != "command-1" {
		t.Fatalf("command response = %#v", client.sent)
	}
	readCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, ok := msgBus.ConsumeInbound(readCtx); ok {
		t.Fatal("cron permission command leaked into the agent conversation")
	}
}

func TestMezonCronersListsExplicitManagers(t *testing.T) {
	permissions := &cronPermissionStore{rows: []store.ConfigPermission{{
		ConfigType: store.ConfigTypeCron, Scope: "group:mezon-prod:channel-1", UserID: "user-2", Permission: "allow",
	}}}
	client := &fakeSDKClient{}
	channel := newWithClient(testMezonConfig(), bus.New(), nil, nil, client)
	channel.SetName("mezon-prod")
	channel.SetAgentID(uuid.NewString())
	channel.configPermStore = permissions
	if err := channel.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = channel.Stop(context.Background()) })
	client.emit(&mezonsdk.ChannelMessage{MessageID: "command-2", ChannelID: "channel-1", ClanID: "clan-1", SenderID: "user-2", Content: []byte(`{"t":"/croners"}`), Mode: int32(mezonsdk.StreamModeChannel)})
	if len(client.sent) != 1 || !containsAll(client.sent[0].content, "user-2", "Cron managers") {
		t.Fatalf("command response = %#v", client.sent)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
