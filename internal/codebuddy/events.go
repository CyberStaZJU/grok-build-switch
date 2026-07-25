package codebuddy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Event is a stable abstraction over CodeBuddy's stream-json NDJSON output.
// Raw is retained for forward compatibility without exposing protocol details
// to callers that only need text, completion, or error events.
type Event struct {
	Kind      string          `json:"kind"`
	Type      string          `json:"type,omitempty"`
	Subtype   string          `json:"subtype,omitempty"`
	Text      string          `json:"text,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Error     string          `json:"error,omitempty"`
	Tool      *ToolEvent      `json:"tool,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

// ToolEvent describes a native tool invocation observed in CodeBuddy's stream.
type ToolEvent struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	InputJSON string `json:"input_json,omitempty"`
	UseID     string `json:"tool_use_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

const (
	EventText       = "text"
	EventResult     = "result"
	EventError      = "error"
	EventMeta       = "meta"
	EventToolUse    = "tool_use"
	EventToolResult = "tool_result"
)

// EventDecoder reads one JSON object per line and tolerates future fields.
// The accumulator is reused across calls to collect streamed tool input.
type EventDecoder struct {
	scanner     *bufio.Scanner
	accumulator toolAccumulator
}

func NewEventDecoder(reader io.Reader) *EventDecoder {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	return &EventDecoder{scanner: scanner}
}

func (d *EventDecoder) Next() (Event, error) {
	for d.scanner.Scan() {
		line := bytes.TrimSpace(d.scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		return ParseEvent(line, &d.accumulator)
	}
	if err := d.scanner.Err(); err != nil {
		return Event{}, err
	}
	return Event{}, io.EOF
}

// toolAccumulator collects streamed tool input across multiple delta events.
type toolAccumulator struct {
	id    string
	name  string
	input strings.Builder
}

func (t *toolAccumulator) reset() {
	t.id = ""
	t.name = ""
	t.input.Reset()
}

func (t *toolAccumulator) append(delta string) {
	t.input.WriteString(delta)
}

func (t *toolAccumulator) inputJSON() string {
	return t.input.String()
}

// ParseEvent converts a single NDJSON line into a stable Event. Tool-use
// blocks are accumulated across content_block_start/delta/stop and emitted as
// a single EventToolUse when the block closes. Tool results are emitted as
// EventToolResult immediately.
func ParseEvent(line []byte, acc *toolAccumulator) (Event, error) {
	var envelope struct {
		Type          string          `json:"type"`
		Subtype       string          `json:"subtype"`
		Index         int             `json:"index"`
		Result        json.RawMessage `json:"result"`
		Message       json.RawMessage `json:"message"`
		Content       json.RawMessage `json:"content"`
		Delta         json.RawMessage `json:"delta"`
		Error         json.RawMessage `json:"error"`
		SessionID     string          `json:"session_id"`
		IsError       bool            `json:"is_error"`
		ContentBlock  json.RawMessage `json:"content_block"`
		ToolUseID     string          `json:"tool_use_id"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return Event{}, fmt.Errorf("解析 CodeBuddy stream-json 事件失败: %w", err)
	}
	event := Event{
		Kind:      EventMeta,
		Type:      envelope.Type,
		Subtype:   envelope.Subtype,
		SessionID: envelope.SessionID,
		Raw:       append(json.RawMessage(nil), line...),
	}

	// tool_result: emitted immediately
	if envelope.Type == "tool_result" {
		event.Kind = EventToolResult
		event.Tool = &ToolEvent{
			UseID:   envelope.ToolUseID,
			IsError: envelope.IsError,
		}
		if text := firstJSONText(envelope.Content, envelope.Message); text != "" {
			event.Text = text
		}
		return event, nil
	}

	// content_block_start: detect tool_use block
	if envelope.Type == "content_block_start" {
		var block struct {
			Type string          `json:"type"`
			ID   string          `json:"id"`
			Name string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(envelope.ContentBlock, &block); err == nil && block.Type == "tool_use" {
			acc.reset()
			acc.id = block.ID
			acc.name = block.Name
			if len(block.Input) > 0 && !bytes.Equal(block.Input, []byte("null")) {
				acc.input.Write(block.Input)
			}
		}
		return event, nil
	}

	// content_block_delta: accumulate tool input JSON
	if envelope.Type == "content_block_delta" {
		var delta struct {
			Type        string `json:"type"`
			PartialJSON string `json:"partial_json"`
			Text        string `json:"text"`
		}
		if err := json.Unmarshal(envelope.Delta, &delta); err == nil {
			if delta.Type == "input_json_delta" && acc.id != "" {
				acc.append(delta.PartialJSON)
				return event, nil
			}
			if delta.Type == "text_delta" && delta.Text != "" {
				event.Kind = EventText
				event.Text = delta.Text
				return event, nil
			}
		}
		// Fallback: try generic text extraction
		if text := firstJSONText(envelope.Delta, envelope.Content, envelope.Message); text != "" {
			event.Kind = EventText
			event.Text = text
		}
		return event, nil
	}

	// content_block_stop: emit accumulated tool_use
	if envelope.Type == "content_block_stop" && acc.id != "" {
		event.Kind = EventToolUse
		event.Tool = &ToolEvent{
			ID:        acc.id,
			Name:      acc.name,
			InputJSON: acc.inputJSON(),
		}
		acc.reset()
		return event, nil
	}

	if envelope.IsError || strings.Contains(strings.ToLower(envelope.Type), "error") {
		event.Kind = EventError
		event.Error = firstJSONText(envelope.Error, envelope.Result, envelope.Message)
		if event.Error == "" {
			event.Error = "CodeBuddy 返回错误事件"
		}
		return event, nil
	}
	if strings.EqualFold(envelope.Type, "result") || strings.EqualFold(envelope.Subtype, "success") {
		event.Kind = EventResult
		event.Text = firstJSONText(envelope.Result, envelope.Content, envelope.Message)
		return event, nil
	}
	if text := firstJSONText(envelope.Delta, envelope.Content, envelope.Message); text != "" {
		event.Kind = EventText
		event.Text = text
	}
	return event, nil
}

func firstJSONText(values ...json.RawMessage) string {
	for _, value := range values {
		if text := jsonText(value); text != "" {
			return text
		}
	}
	return ""
}

func jsonText(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	var object struct {
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
		Delta   json.RawMessage `json:"delta"`
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(raw, &object) == nil {
		if strings.TrimSpace(object.Text) != "" {
			return strings.TrimSpace(object.Text)
		}
		return firstJSONText(object.Content, object.Delta, object.Message)
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) == nil {
		parts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if text := jsonText(block); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}
