package mezon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	mezonsdk "github.com/dungxbuif/mezon-sdk-go"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	channelmedia "github.com/nextlevelbuilder/goclaw/internal/channels/media"
)

const (
	defaultMezonMediaMaxBytes int64 = 20 * 1024 * 1024
	maxMezonAttachments             = 15
	mezonAttachmentTimeout          = 30 * time.Second
)

func resolveMezonAttachments(ctx context.Context, attachments []mezonsdk.Attachment, maxBytes int64) ([]bus.MediaFile, string) {
	downloadCtx, cancel := context.WithTimeout(ctx, mezonAttachmentTimeout)
	defer cancel()
	if maxBytes <= 0 {
		maxBytes = defaultMezonMediaMaxBytes
	}
	if len(attachments) > maxMezonAttachments {
		attachments = attachments[:maxMezonAttachments]
	}
	files := make([]bus.MediaFile, 0, len(attachments))
	infos := make([]channelmedia.MediaInfo, 0, len(attachments))
	var extracted strings.Builder
	for _, attachment := range attachments {
		if attachment.URL == "" || int64(attachment.Size) > maxBytes {
			continue
		}
		path, err := downloadMezonAttachment(downloadCtx, attachment.URL, attachment.Filename, maxBytes)
		if err != nil {
			slog.Warn("mezon: attachment download failed", "filename", attachment.Filename, "error", err)
			continue
		}
		mimeType := strings.TrimSpace(attachment.Filetype)
		if mimeType == "" {
			mimeType = channelmedia.DetectMIMEType(attachment.Filename)
		}
		kind := classifyMezonMedia(mimeType)
		info := channelmedia.MediaInfo{Type: kind, FilePath: path, SourceURL: attachment.URL, ContentType: mimeType, FileName: attachment.Filename, FileSize: int64(attachment.Size)}
		infos = append(infos, info)
		files = append(files, bus.MediaFile{Path: path, MimeType: mimeType, Filename: attachment.Filename})
		if kind == channelmedia.TypeDocument {
			if text, extractErr := channelmedia.ExtractDocumentContent(path, attachment.Filename); extractErr == nil && text != "" {
				extracted.WriteString("\n\n" + text)
			}
		}
	}
	contextText := channelmedia.BuildMediaTags(infos) + extracted.String()
	return files, strings.TrimSpace(contextText)
}

func downloadMezonAttachment(ctx context.Context, rawURL, filename string, maxBytes int64) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", fmt.Errorf("invalid attachment URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // URL is supplied by Mezon's attachment event
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download status %d", resp.StatusCode)
	}
	ext := filepath.Ext(filename)
	if ext == "" {
		ext = filepath.Ext(parsed.Path)
	}
	file, err := os.CreateTemp("", "mezon_media_*"+ext)
	if err != nil {
		return "", err
	}
	path := file.Name()
	written, copyErr := io.Copy(file, io.LimitReader(resp.Body, maxBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written > maxBytes {
		_ = os.Remove(path)
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		return "", fmt.Errorf("attachment exceeds %d bytes", maxBytes)
	}
	return path, nil
}

func classifyMezonMedia(contentType string) string {
	switch {
	case strings.HasPrefix(strings.ToLower(contentType), "image/"):
		return channelmedia.TypeImage
	case strings.HasPrefix(strings.ToLower(contentType), "video/"):
		return channelmedia.TypeVideo
	case strings.HasPrefix(strings.ToLower(contentType), "audio/"):
		return channelmedia.TypeAudio
	default:
		return channelmedia.TypeDocument
	}
}
