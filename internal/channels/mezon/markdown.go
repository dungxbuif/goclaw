package mezon

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode/utf16"

	mezonsdk "github.com/dungxbuif/mezon-sdk-go"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

var mezonMarkdownToken = regexp.MustCompile("(?s:```.*?```)|`[^`\\n]+`|\\*\\*[^*\\n]+\\*\\*|\\[[^\\]\\n]+\\]\\(https?://[^)\\s]+\\)|https?://[^\\s]+")
var mezonMarkdownLink = regexp.MustCompile(`^\[([^\]\n]+)\]\((https?://[^)\s]+)\)$`)

// mezonMarkdownContent converts common LLM Markdown into Mezon's native model:
// plain text in t and end-exclusive UTF-16 ranges in mk. Mezon clients add the
// visual markers while rendering; leaving Markdown delimiters in t displays
// duplicate punctuation.
func mezonMarkdownContent(source string) mezonsdk.Content {
	payload := map[string]any{}
	var plain strings.Builder
	spans := make([]map[string]any, 0)
	last := 0
	for _, loc := range mezonMarkdownToken.FindAllStringIndex(source, -1) {
		plain.WriteString(source[last:loc[0]])
		token := source[loc[0]:loc[1]]
		start := utf16StringLen(plain.String())
		typ, rendered := "", token
		switch {
		case strings.HasPrefix(token, "```"):
			typ, rendered = "pre", strings.TrimSuffix(strings.TrimPrefix(token, "```"), "```")
			if newline := strings.IndexByte(rendered, '\n'); newline >= 0 && !strings.ContainsAny(rendered[:newline], " \t") {
				rendered = rendered[newline+1:]
			}
		case strings.HasPrefix(token, "`"):
			typ, rendered = "c", token[1:len(token)-1]
		case strings.HasPrefix(token, "**"):
			typ, rendered = "b", token[2:len(token)-2]
		case strings.HasPrefix(token, "["):
			parts := mezonMarkdownLink.FindStringSubmatch(token)
			if len(parts) == 3 {
				plain.WriteString(parts[1] + " (")
				start = utf16StringLen(plain.String())
				rendered, typ = parts[2], "lk"
			}
		case strings.HasPrefix(token, "http://"), strings.HasPrefix(token, "https://"):
			typ = "lk"
		}
		plain.WriteString(rendered)
		end := utf16StringLen(plain.String())
		if strings.HasPrefix(token, "[") && typ == "lk" {
			plain.WriteByte(')')
		}
		if typ != "" && end > start {
			spans = append(spans, map[string]any{"type": typ, "s": start, "e": end})
		}
		last = loc[1]
	}
	plain.WriteString(source[last:])
	payload["t"] = plain.String()
	if len(spans) > 0 {
		payload["mk"] = spans
	}
	return payload
}

func utf16StringLen(value string) int { return len(utf16.Encode([]rune(value))) }

func mezonOutboundAttachments(media []bus.MediaAttachment) ([]mezonsdk.Attachment, error) {
	attachments := make([]mezonsdk.Attachment, 0, len(media))
	for _, item := range media {
		if item.URL == "" {
			return nil, fmt.Errorf("%w: mezon attachment URL is empty", channels.ErrMediaUnsupported)
		}
		parsed, err := url.Parse(item.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, fmt.Errorf("%w: mezon local-file upload is not available", channels.ErrMediaUnsupported)
		}
		filename := path.Base(parsed.Path)
		if filename == "." || filename == "/" || filename == "" {
			filename = "attachment"
		}
		attachments = append(attachments, mezonsdk.Attachment{URL: item.URL, Filename: filename, Filetype: item.ContentType})
	}
	return attachments, nil
}
