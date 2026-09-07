package channels

import "context"

// InteractiveMessage is a platform-neutral interactive card. Adapters map the
// declarative shape to their native SDK and reject unsupported component kinds.
type InteractiveMessage struct {
	Text       string                `json:"text"`
	Embed      *InteractiveEmbed     `json:"embed"`
	ButtonRows [][]InteractiveButton `json:"button_rows"`
}

type InteractiveEmbed struct {
	Title        string             `json:"title"`
	Description  string             `json:"description"`
	Author       *InteractiveAuthor `json:"author"`
	ThumbnailURL string             `json:"thumbnail_url"`
	Image        *InteractiveImage  `json:"image"`
	Fields       []InteractiveField `json:"fields"`
	Inputs       []InteractiveInput `json:"inputs"`
}

type InteractiveAuthor struct {
	Name    string `json:"name"`
	IconURL string `json:"icon_url"`
	URL     string `json:"url"`
}

type InteractiveImage struct {
	URL    string `json:"url"`
	Width  string `json:"width"`
	Height string `json:"height"`
}

type InteractiveField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type InteractiveButton struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Style string `json:"style"`
}

type InteractiveOption struct {
	Label       string `json:"label"`
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Style       string `json:"style"`
	Disabled    bool   `json:"disabled"`
}

type InteractiveInput struct {
	Kind         string              `json:"kind"`
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Description  string              `json:"description"`
	Placeholder  string              `json:"placeholder"`
	DefaultValue any                 `json:"default_value"`
	InputType    string              `json:"input_type"`
	Textarea     bool                `json:"textarea"`
	Disabled     bool                `json:"disabled"`
	Options      []InteractiveOption `json:"options"`
	Selected     string              `json:"selected"`
	MaxOptions   int                 `json:"max_options"`
	URLImage     string              `json:"url_image"`
	URLPosition  string              `json:"url_position"`
	Pool         []string            `json:"pool"`
	Repeat       int                 `json:"repeat"`
	Duration     int                 `json:"duration"`
}

// InteractiveMessageProvider is implemented only by channel adapters that can
// render native interactive messages.
type InteractiveMessageProvider interface {
	SendInteractiveMessage(context.Context, string, string, InteractiveMessage) (string, error)
}
