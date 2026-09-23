// Package streamguard adapts third-party OpenAI-compatible streaming
// responses to the closed event enumerations that Grok Build deserializes.
//
// The Grok client models Responses streaming events as a Rust enum with a
// fixed set of variants. An upstream that emits any other `type` — the API
// pool gateway sends `{"type":"keepalive"}` during silent periods — aborts
// the whole turn with
//
//	serialization error: unknown variant `keepalive`, expected one of ...
//
// and does not retry. Grok Build never sees the surrounding frames, so the
// gateway's own diagnostics are lost too. Dropping the frame before the
// client reads it keeps the turn alive; every frame the client does
// understand is forwarded byte-for-byte.
package streamguard

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

// ResponsesEventTypes is the exact set of Responses streaming event types the
// Grok client accepts. It mirrors the variant list in the client's error
// message; anything outside it terminates the turn.
var ResponsesEventTypes = map[string]bool{
	"response.created":                             true,
	"response.in_progress":                         true,
	"response.completed":                           true,
	"response.failed":                              true,
	"response.incomplete":                          true,
	"response.output_item.added":                   true,
	"response.output_item.done":                    true,
	"response.content_part.added":                  true,
	"response.content_part.done":                   true,
	"response.output_text.delta":                   true,
	"response.output_text.done":                    true,
	"response.refusal.delta":                       true,
	"response.refusal.done":                        true,
	"response.function_call_arguments.delta":       true,
	"response.function_call_arguments.done":        true,
	"response.file_search_call.in_progress":        true,
	"response.file_search_call.searching":          true,
	"response.file_search_call.completed":          true,
	"response.web_search_call.in_progress":         true,
	"response.web_search_call.searching":           true,
	"response.web_search_call.completed":           true,
	"response.reasoning_summary_part.added":        true,
	"response.reasoning_summary_part.done":         true,
	"response.reasoning_summary_text.delta":        true,
	"response.reasoning_summary_text.done":         true,
	"response.reasoning_text.delta":                true,
	"response.reasoning_text.done":                 true,
	"response.image_generation_call.completed":     true,
	"response.image_generation_call.generating":    true,
	"response.image_generation_call.in_progress":   true,
	"response.image_generation_call.partial_image": true,
	"response.mcp_call_arguments.delta":            true,
	"response.mcp_call_arguments.done":             true,
	"response.mcp_call.completed":                  true,
	"response.mcp_call.failed":                     true,
	"response.mcp_call.in_progress":                true,
	"response.mcp_list_tools.completed":            true,
	"response.mcp_list_tools.failed":               true,
	"response.mcp_list_tools.in_progress":          true,
	"response.code_interpreter_call.in_progress":   true,
	"response.code_interpreter_call.interpreting":  true,
	"response.code_interpreter_call.completed":     true,
	"response.code_interpreter_call_code.delta":    true,
	"response.code_interpreter_call_code.done":     true,
	"response.output_text.annotation.added":        true,
	"response.queued":                              true,
	"response.custom_tool_call_input.delta":        true,
	"response.custom_tool_call_input.done":         true,
	"error":                                        true,
}

// SanitizeResponsesStream copies a Responses SSE stream from src to dst,
// dropping frames whose data payload carries an event type the Grok client
// cannot deserialize. See SanitizeStream.
func SanitizeResponsesStream(dst io.Writer, src io.Reader) error {
	return SanitizeStream(dst, src, ModeResponses)
}

func isBlankLine(line []byte) bool {
	return len(bytes.TrimRight(line, "\r\n")) == 0
}

// EventTypeUnsupported reports whether any `data:` line in one SSE frame
// carries an event type outside the Grok client's enum.
func EventTypeUnsupported(frame []byte) bool {
	for _, payload := range dataPayloads(frame) {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			continue
		}
		if envelope.Type != "" && !ResponsesEventTypes[envelope.Type] {
			return true
		}
	}
	return false
}

// ChatChunkUnsupported reports whether any `data:` line in one SSE frame is a
// non-chunk heartbeat on an OpenAI Chat Completions stream.
//
// The same gateway heartbeat that breaks the Responses path also breaks this
// one, but with a different client error: `{"type":"keepalive"}` is parsed as
// a chat chunk and rejected for `missing field \`id\“. Chat streams never
// carry a `type` field for legitimate content, so a frame is dropped only when
// its `type` is a heartbeat name or a Responses event name. Error frames keep
// their `{"error":{...}}` shape and are deliberately left alone.
func ChatChunkUnsupported(frame []byte) bool {
	for _, payload := range dataPayloads(frame) {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			continue
		}
		if envelope.Type == "" {
			continue
		}
		if ResponsesEventTypes[envelope.Type] || HeartbeatEventTypes[envelope.Type] {
			return true
		}
	}
	return false
}

// HeartbeatEventTypes are non-standard keep-alive frames seen from
// OpenAI-compatible gateways during silent periods.
var HeartbeatEventTypes = map[string]bool{
	"keepalive": true,
	"heartbeat": true,
	"ping":      true,
}

func dataPayloads(frame []byte) [][]byte {
	var out [][]byte
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		out = append(out, payload)
	}
	return out
}

// Mode selects which closed enumeration the stream is filtered against.
type Mode int

const (
	// ModeResponses filters a Responses (event: type) stream.
	ModeResponses Mode = iota
	// ModeChatCompletions filters an OpenAI Chat Completions stream.
	ModeChatCompletions
)

// unsupportedFor reports whether the frame must be dropped in the given mode.
func unsupportedFor(frame []byte, mode Mode) bool {
	if mode == ModeChatCompletions {
		return ChatChunkUnsupported(frame)
	}
	return EventTypeUnsupported(frame)
}

// RepairFrame rewrites frames the Grok client cannot deserialize, returning the
// replacement bytes. It reports whether the frame was changed.
//
// Two independent upstream defects are handled for Responses streams:
//
//   - a non-standard `type` outside the client's enum, which is dropped
//     outright;
//   - a `response.failed` payload that omits `output`, which is itself part of
//     the client's required schema. The API pool emits this while reporting a
//     transient `server_error`; without the field the client sees a
//     schema error instead of the real, retryable failure.
//
// Frames that are already well-formed pass through untouched.
func RepairFrame(frame []byte, mode Mode) ([]byte, bool) {
	if unsupportedFor(frame, mode) {
		return nil, true
	}
	if mode != ModeResponses {
		return frame, false
	}
	return repairFailedResponseFrame(frame)
}

// repairFailedResponseFrame backfills the required envelope fields on a
// `response.failed` frame that reported them as absent.
func repairFailedResponseFrame(frame []byte) ([]byte, bool) {
	changed := false
	var out []byte
	for _, line := range bytes.Split(frame, []byte("\n")) {
		trimmed := bytes.TrimRight(line, "\r")
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			out = appendLine(out, line)
			continue
		}
		payload := bytes.TrimSpace(trimmed[len("data:"):])
		fixed, ok := repairFailedResponsePayload(payload)
		if !ok {
			out = appendLine(out, line)
			continue
		}
		changed = true
		out = appendLine(out, append([]byte("data: "), fixed...))
	}
	if !changed {
		return frame, false
	}
	return out, true
}

func appendLine(dst, line []byte) []byte {
	dst = append(dst, line...)
	return append(dst, '\n')
}

// requiredResponseEnvelope are the member fields the client requires on any
// embedded `response` object, keyed by the value that satisfies each.
var requiredResponseEnvelope = map[string]any{
	"output":              []any{},
	"parallel_tool_calls": true,
	"tool_choice":         "auto",
	"tools":               []any{},
}

func repairFailedResponsePayload(payload []byte) ([]byte, bool) {
	// Decode into a map so every unrelated member (sequence_number among them)
	// survives the rewrite; only the embedded response object is touched.
	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, false
	}
	if envelope["type"] != "response.failed" {
		return nil, false
	}
	response, ok := envelope["response"].(map[string]any)
	if !ok {
		return nil, false
	}
	changed := false
	for key, placeholder := range requiredResponseEnvelope {
		if _, present := response[key]; !present {
			response[key] = placeholder
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// SanitizeStream copies an SSE stream from src to dst in the given mode,
// dropping whole frames the Grok client cannot deserialize. Frames it
// understands pass through byte-for-byte, so a conforming upstream is
// unaffected.
func SanitizeStream(dst io.Writer, src io.Reader, mode Mode) error {
	reader := bufio.NewReaderSize(src, 64*1024)
	frame := make([]byte, 0, 4096)

	flush := func() error {
		if len(frame) == 0 {
			return nil
		}
		repaired, changed := RepairFrame(frame, mode)
		if changed {
			if len(repaired) == 0 {
				frame = frame[:0]
				return nil
			}
			if _, err := dst.Write(repaired); err != nil {
				return err
			}
			frame = frame[:0]
			return nil
		}
		if _, err := dst.Write(frame); err != nil {
			return err
		}
		frame = frame[:0]
		return nil
	}

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			frame = append(frame, line...)
			if isBlankLine(line) {
				if ferr := flush(); ferr != nil {
					return ferr
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				// An upstream that ends without a trailing blank line still
				// has a complete frame to forward.
				return flush()
			}
			return err
		}
	}
}
