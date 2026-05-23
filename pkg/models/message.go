package models

import (
	"encoding/json"
	"strings"
)

// ContentBlock is a single block within a message's content array.
// The Type field is the discriminant: "text", "tool_use", or "tool_result".
type ContentBlock struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// tool_use
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`

	// tool_result (content is stored in Text)
	ToolUseID string `json:"tool_use_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// TextBlock returns a text content block.
func TextBlock(text string) ContentBlock {
	return ContentBlock{Type: "text", Text: text}
}

// ToolUseBlock returns a tool_use content block.
func ToolUseBlock(id, name string, input any) ContentBlock {
	return ContentBlock{Type: "tool_use", ID: id, Name: name, Input: input}
}

// ToolResultBlock returns a tool_result content block. content may be a string
// or any JSON-serializable value; non-strings are JSON-encoded and stored as text.
func ToolResultBlock(toolUseID string, content any) ContentBlock {
	var text string
	switch v := content.(type) {
	case string:
		text = v
	default:
		if b, err := json.Marshal(v); err == nil {
			text = string(b)
		}
	}
	return ContentBlock{Type: "tool_result", ToolUseID: toolUseID, Text: text}
}

// ContentList is a slice of ContentBlock that unmarshals from either a JSON
// string (legacy wire format) or a JSON array of content blocks.
type ContentList []ContentBlock

func (c *ContentList) UnmarshalJSON(data []byte) error {
	// Try string first for backward compatibility with simple text messages.
	var s string
	if json.Unmarshal(data, &s) == nil {
		*c = ContentList{TextBlock(s)}
		return nil
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(data, &blocks); err != nil {
		return err
	}
	*c = blocks
	return nil
}

// TextContent returns a ContentList with a single text block.
func TextContent(text string) ContentList {
	return ContentList{TextBlock(text)}
}

// TextOf returns the concatenated text of all text blocks in the list.
func (c ContentList) TextOf() string {
	var sb strings.Builder
	for _, b := range c {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// Message is one turn in a conversation transcript.
type Message struct {
	// Role is "system", "user", or "assistant".
	Role    string      `json:"role"`
	Content ContentList `json:"content"`

	// Provenance -- optional, for tracing which agent/step produced this message.
	Agent string `json:"agent,omitempty"`
	Step  int    `json:"step,omitempty"`
}

// TextMessage creates a message with a single text content block.
func TextMessage(role, text string) Message {
	return Message{Role: role, Content: TextContent(text)}
}
