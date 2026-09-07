package agent

import (
	"context"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

func TestInjectContextPropagatesTrustedContainerID(t *testing.T) {
	req := &RunRequest{
		Channel:     "mezon-prod",
		ChannelType: "mezon",
		ContainerID: "clan-1",
	}
	setup, err := newArtifactTestLoop(t.TempDir()).injectContext(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got := tools.ToolContainerIDFromCtx(setup.ctx); got != "clan-1" {
		t.Fatalf("container ID = %q, want clan-1", got)
	}
}
