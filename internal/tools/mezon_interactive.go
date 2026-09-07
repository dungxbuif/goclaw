package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

type InteractiveMessageSender func(context.Context, string, string, string, channels.InteractiveMessage) (string, error)

type InteractiveMessageSenderAware interface {
	SetInteractiveMessageSender(InteractiveMessageSender)
}

// MezonInteractiveTool sends native Mezon cards in the current trusted clan
// channel. It intentionally has no target argument: cross-channel interactive
// delivery must never be inferred by the model.
type MezonInteractiveTool struct {
	sender InteractiveMessageSender
}

func NewMezonInteractiveTool() *MezonInteractiveTool { return &MezonInteractiveTool{} }

func (t *MezonInteractiveTool) SetInteractiveMessageSender(sender InteractiveMessageSender) {
	t.sender = sender
}

func (t *MezonInteractiveTool) Name() string { return "mezon_interactive" }

func (t *MezonInteractiveTool) Description() string {
	return "Send a native interactive card in the current Mezon clan channel. Supports button rows and embed inputs: text/textarea, select, radio/multi-choice, date picker, and animation. Button clicks and dropdown choices return to the bot as user messages so it can reply."
}

func (t *MezonInteractiveTool) RequiredChannelTypes() []string { return []string{channels.TypeMezon} }

func (t *MezonInteractiveTool) Parameters() map[string]any {
	option := map[string]any{"type": "object", "properties": map[string]any{
		"label": map[string]any{"type": "string"}, "value": map[string]any{"type": "string"},
		"name": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
		"style": map[string]any{"type": "string", "enum": interactiveStyles()}, "disabled": map[string]any{"type": "boolean"},
	}, "required": []string{"label", "value"}}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{"type": "string"},
			"button_rows": map[string]any{"type": "array", "maxItems": 5, "items": map[string]any{
				"type": "array", "minItems": 1, "maxItems": 5, "items": map[string]any{"type": "object", "properties": map[string]any{
					"id": map[string]any{"type": "string"}, "label": map[string]any{"type": "string"},
					"style": map[string]any{"type": "string", "enum": interactiveStyles(), "default": "primary"},
				}, "required": []string{"id", "label"}},
			}},
			"embed": map[string]any{"type": "object", "properties": map[string]any{
				"title": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
				"thumbnail_url": map[string]any{"type": "string"},
				"author":        map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "icon_url": map[string]any{"type": "string"}, "url": map[string]any{"type": "string"}}},
				"image":         map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string"}, "width": map[string]any{"type": "string"}, "height": map[string]any{"type": "string"}}, "required": []string{"url"}},
				"fields":        map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "value": map[string]any{"type": "string"}, "inline": map[string]any{"type": "boolean"}}, "required": []string{"name", "value"}}},
				"inputs": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
					"kind": map[string]any{"type": "string", "enum": []string{"input", "select", "radio", "date", "animation"}},
					"id":   map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
					"placeholder": map[string]any{"type": "string"}, "default_value": map[string]any{}, "input_type": map[string]any{"type": "string"},
					"textarea": map[string]any{"type": "boolean"}, "disabled": map[string]any{"type": "boolean"},
					"options": map[string]any{"type": "array", "items": option}, "selected": map[string]any{"type": "string"},
					"max_options": map[string]any{"type": "integer", "minimum": 1},
					"url_image":   map[string]any{"type": "string"}, "url_position": map[string]any{"type": "string"},
					"pool":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"repeat": map[string]any{"type": "integer", "minimum": 1}, "duration": map[string]any{"type": "integer", "minimum": 1},
				}, "required": []string{"kind", "id", "name"}}},
			}},
		},
	}
}

type interactiveArgs struct {
	Text       string                         `json:"text"`
	Embed      *channels.InteractiveEmbed     `json:"embed"`
	ButtonRows [][]channels.InteractiveButton `json:"button_rows"`
}

func (t *MezonInteractiveTool) Execute(ctx context.Context, args map[string]any) *Result {
	if t.sender == nil {
		return ErrorResult("mezon_interactive: no channel sender available")
	}
	channel, chatID, clanID := ToolChannelFromCtx(ctx), ToolChatIDFromCtx(ctx), ToolContainerIDFromCtx(ctx)
	if channel == "" || chatID == "" || clanID == "" {
		return ErrorResult("mezon_interactive requires the current channel and trusted clan context")
	}
	data, err := json.Marshal(args)
	if err != nil {
		return ErrorResult(fmt.Sprintf("mezon_interactive: encode arguments: %v", err))
	}
	var parsed interactiveArgs
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ErrorResult(fmt.Sprintf("mezon_interactive: invalid arguments: %v", err))
	}
	message := channels.InteractiveMessage{Text: strings.TrimSpace(parsed.Text), Embed: parsed.Embed, ButtonRows: parsed.ButtonRows}
	if err := validateInteractiveMessage(message); err != nil {
		return ErrorResult("mezon_interactive: " + err.Error())
	}
	messageID, err := t.sender(ctx, channel, chatID, clanID, message)
	if err != nil {
		return ErrorResult(fmt.Sprintf("mezon_interactive failed: %v", err))
	}
	out, _ := json.Marshal(map[string]any{"status": "sent", "channel_id": chatID, "message_id": messageID})
	return NewResult(string(out))
}

func validateInteractiveMessage(message channels.InteractiveMessage) error {
	controls := len(message.ButtonRows)
	if message.Embed != nil {
		controls += len(message.Embed.Inputs)
	}
	if controls == 0 {
		return errors.New("at least one button or embed input is required")
	}
	if message.Text == "" && message.Embed == nil {
		return errors.New("text or embed is required")
	}
	if len(message.ButtonRows) > 5 {
		return errors.New("at most 5 button rows are allowed")
	}
	seen := make(map[string]struct{})
	for _, row := range message.ButtonRows {
		if len(row) == 0 || len(row) > 5 {
			return errors.New("each button row must contain 1 to 5 buttons")
		}
		for _, button := range row {
			if err := validateInteractiveID(seen, button.ID); err != nil {
				return err
			}
			if strings.TrimSpace(button.Label) == "" {
				return errors.New("button label is required")
			}
			if !validInteractiveStyle(button.Style) {
				return fmt.Errorf("unsupported button style %q", button.Style)
			}
		}
	}
	if message.Embed == nil {
		return nil
	}
	for _, input := range message.Embed.Inputs {
		if err := validateInteractiveID(seen, input.ID); err != nil {
			return err
		}
		if strings.TrimSpace(input.Name) == "" {
			return fmt.Errorf("input %q requires a name", input.ID)
		}
		switch input.Kind {
		case "input", "date":
		case "select", "radio":
			if len(input.Options) == 0 {
				return fmt.Errorf("%s input %q requires options", input.Kind, input.ID)
			}
			for _, option := range input.Options {
				if strings.TrimSpace(option.Label) == "" || strings.TrimSpace(option.Value) == "" {
					return fmt.Errorf("%s input %q has an empty option", input.Kind, input.ID)
				}
				if option.Style != "" && !validInteractiveStyle(option.Style) {
					return fmt.Errorf("unsupported option style %q", option.Style)
				}
			}
			if input.MaxOptions > len(input.Options) {
				return fmt.Errorf("max_options exceeds options for %q", input.ID)
			}
			if input.MaxOptions < 0 {
				return fmt.Errorf("max_options must be positive for %q", input.ID)
			}
			if input.Selected != "" {
				found := false
				for _, option := range input.Options {
					found = found || option.Value == input.Selected
				}
				if !found {
					return fmt.Errorf("selected value is not an option for %q", input.ID)
				}
			}
		case "animation":
			if strings.TrimSpace(input.URLImage) == "" || len(input.Pool) == 0 {
				return fmt.Errorf("animation %q requires url_image and pool", input.ID)
			}
			if input.Repeat < 0 || input.Duration < 0 {
				return fmt.Errorf("animation %q repeat and duration must be positive", input.ID)
			}
		default:
			return fmt.Errorf("unsupported input kind %q", input.Kind)
		}
	}
	return nil
}

func validateInteractiveID(seen map[string]struct{}, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("component id is required")
	}
	if _, ok := seen[id]; ok {
		return fmt.Errorf("duplicate component id %q", id)
	}
	seen[id] = struct{}{}
	return nil
}

func interactiveStyles() []string {
	return []string{"primary", "secondary", "success", "danger", "link"}
}

func validInteractiveStyle(style string) bool {
	if style == "" {
		return true
	}
	for _, allowed := range interactiveStyles() {
		if style == allowed {
			return true
		}
	}
	return false
}
