package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

func TestReadImageLoadPathMapsUploadsAlias(t *testing.T) {
	workspace := t.TempDir()
	uploads := filepath.Join(workspace, ".uploads")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatalf("create uploads directory: %v", err)
	}
	const imageData = "test-image"
	if err := os.WriteFile(filepath.Join(uploads, "receipt.jpg"), []byte(imageData), 0o600); err != nil {
		t.Fatalf("write staged image: %v", err)
	}

	images, err := NewReadImageTool(nil).loadImageFromPath(
		WithToolWorkspace(context.Background(), workspace),
		"/uploads/receipt.jpg",
	)
	if err != nil {
		t.Fatalf("load /uploads alias: %v", err)
	}
	if len(images) != 1 || images[0].MimeType != "image/jpeg" || images[0].Data == "" {
		t.Fatalf("unexpected image content: %#v", images)
	}
}

func TestReadImage_BothPathAndUrl_Error(t *testing.T) {
	tool := NewReadImageTool(nil)

	res := tool.Execute(context.Background(), map[string]any{
		"prompt": "describe this",
		"path":   "workspace/image.png",
		"url":    "https://example.com/image.png",
	})

	if !res.IsError {
		t.Fatalf("expected error when both path and url are provided")
	}

	if !strings.Contains(res.ForLLM, "Both 'path' and 'url' parameters cannot be specified") {
		t.Errorf("unexpected error message: %s", res.ForLLM)
	}
}

func TestReadImage_PrivateURL_Error(t *testing.T) {
	tool := NewReadImageTool(nil)

	res := tool.Execute(context.Background(), map[string]any{
		"prompt": "describe this",
		"url":    "http://127.0.0.1/image.png",
	})

	if !res.IsError {
		t.Fatalf("expected error for private image URL")
	}
	if !strings.Contains(res.ForLLM, "Invalid image URL") {
		t.Errorf("unexpected error message: %s", res.ForLLM)
	}
}

func TestResolveImageMediaRefRequiresExactID(t *testing.T) {
	ctx := WithMediaImageRefs(context.Background(), []providers.MediaRef{
		{ID: "first-id", Kind: "image", Path: ".uploads/first.png"},
		{ID: "second-id", Kind: "image", Path: ".uploads/second.png"},
	})

	got, err := resolveImageMediaRef(ctx, "first-id")
	if err != nil || got.ID != "first-id" {
		t.Fatalf("exact media ID = %#v, %v", got, err)
	}
	latest, err := resolveImageMediaRef(ctx, "latest")
	if err != nil || latest.ID != "second-id" {
		t.Fatalf("latest media ID = %#v, %v", latest, err)
	}
	if _, err := resolveImageMediaRef(ctx, "missing-id"); err == nil {
		t.Fatal("unknown media ID unexpectedly resolved")
	}
}

func TestReadImage_AnthropicURL_Error(t *testing.T) {
	tool := NewReadImageTool(nil)

	params := map[string]any{
		"prompt": "describe this",
		"images": []providers.ImageContent{
			{
				URL: "https://93.184.216.34/image.png",
			},
		},
	}

	_, _, err := tool.callProvider(context.Background(), nil, "anthropic", "claude-3-sonnet", params)
	if err == nil {
		t.Fatalf("expected error for anthropic provider with image URL")
	}

	if !strings.Contains(err.Error(), "does not support analyzing images directly from a URL") {
		t.Errorf("unexpected error message: %v", err)
	}

	// Should also error for claude-cli
	_, _, err = tool.callProvider(context.Background(), nil, "claude-cli", "claude-3-sonnet", params)
	if err == nil {
		t.Fatalf("expected error for claude-cli provider with image URL")
	}
}
