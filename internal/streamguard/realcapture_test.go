package streamguard

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// realKeepaliveFrame is the heartbeat the API pool gateway actually sent during
// a live capture on 2026-09-19, reproduced byte for byte (including the
// non-standard sequence_number member).
const realKeepaliveFrame = "event: keepalive\ndata: {\"type\":\"keepalive\",\"sequence_number\":2}\n\n"

// realFailedPayload is the response.failed body that immediately followed that
// heartbeat. The gateway omitted the schema-required `output` member while
// reporting a transient server_error.
const realFailedPayload = `{"type":"response.failed","response":{"id":"resp_0000000000000000000000000000000000000000000000000","object":"response","created_at":1789807241,"status":"failed","background":false,"completed_at":null,"error":{"code":"server_error","message":"Our servers are currently overloaded. Please try again later."},"frequency_penalty":0.0,"max_tool_calls":null,"model":"gpt-5.6-sol","moderation":null,"presence_penalty":0.0,"previous_response_id":null,"prompt_cache_key":"0000000000000000","prompt_cache_retention":"24h","safety_identifier":"user-EXAMPLE00000000000000000000","service_tier":"default","store":false,"temperature":1.0,"top_logprobs":0,"top_p":0.98,"user":null},"sequence_number":4}`

func TestRepairFrameDropsRealCapturedKeepalive(t *testing.T) {
	repaired, changed := RepairFrame([]byte(realKeepaliveFrame), ModeResponses)
	if !changed {
		t.Fatal("real captured heartbeat was not dropped")
	}
	if len(repaired) != 0 {
		t.Fatalf("expected dropped frame, got %q", repaired)
	}
}

func TestRepairFrameBackfillsRealCapturedFailedResponse(t *testing.T) {
	frame := "event: response.failed\ndata: " + realFailedPayload + "\n\n"
	repaired, changed := RepairFrame([]byte(frame), ModeResponses)
	if !changed {
		t.Fatal("malformed response.failed was not repaired")
	}
	if !bytes.HasPrefix(repaired, []byte("event: response.failed\ndata: ")) {
		t.Fatalf("event line lost: %q", repaired)
	}

	payload := repaired[len("event: response.failed\ndata: "):]
	payload = bytes.TrimSpace(payload)
	var envelope struct {
		Type           string         `json:"type"`
		SequenceNumber *int           `json:"sequence_number"`
		Response       map[string]any `json:"response"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("repaired frame is not valid JSON: %v", err)
	}
	if envelope.Type != "response.failed" {
		t.Fatalf("type changed to %q", envelope.Type)
	}
	// The client requires sequence_number on the envelope. Dropping it during
	// the repair replaces one schema error with another.
	if envelope.SequenceNumber == nil || *envelope.SequenceNumber != 4 {
		t.Fatalf("sequence_number not preserved: %v", envelope.SequenceNumber)
	}
	if _, ok := envelope.Response["output"]; !ok {
		t.Fatal("output was not backfilled")
	}
	// The real cause must survive: the client needs it to decide to retry.
	errObj, _ := envelope.Response["error"].(map[string]any)
	if errObj["message"] != "Our servers are currently overloaded. Please try again later." {
		t.Fatalf("upstream error message lost: %#v", errObj)
	}
	if envelope.Response["id"] != "resp_0000000000000000000000000000000000000000000000000" {
		t.Fatalf("response id lost: %v", envelope.Response["id"])
	}
}

func TestRepairFrameLeavesWellFormedFailedResponseAlone(t *testing.T) {
	// A response.failed that already carries every required member must pass
	// through untouched, including its original key order.
	frame := "event: response.failed\ndata: " +
		`{"type":"response.failed","response":{"id":"r","object":"response","created_at":1,` +
		`"status":"failed","model":"m","output":[],"parallel_tool_calls":true,"tool_choice":"auto","tools":[],` +
		`"error":{"code":"server_error","message":"overloaded"}},"sequence_number":4}` + "\n\n"
	repaired, changed := RepairFrame([]byte(frame), ModeResponses)
	if changed {
		t.Fatalf("well-formed failed frame was rewritten: %q", repaired)
	}
	if string(repaired) != frame {
		t.Fatalf("frame changed:\n got %q\nwant %q", repaired, frame)
	}
}

func TestRepairFrameIgnoresChatModeForEnvelopeRepair(t *testing.T) {
	// The chat path has no Response envelope; a frame with a chat chunk shape
	// must not be rewritten by the Responses repair.
	frame := []byte("data: {\"id\":\"c1\",\"choices\":[]}\n\n")
	repaired, changed := RepairFrame(frame, ModeChatCompletions)
	if changed {
		t.Fatalf("chat chunk was rewritten: %q", repaired)
	}
}

func TestSanitizeStreamHandlesRealCapturedFailureSequence(t *testing.T) {
	// The exact sequence from the live capture: heartbeat, then the malformed
	// failure. Both defects must be handled in one pass.
	in := "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0}\n\n" +
		realKeepaliveFrame +
		"event: response.failed\ndata: " + realFailedPayload + "\n\n"

	var out bytes.Buffer
	if err := SanitizeResponsesStream(&out, strings.NewReader(in)); err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "keepalive") {
		t.Fatalf("heartbeat survived: %q", got)
	}
	if !strings.Contains(got, "response.failed") {
		t.Fatalf("failure notification was dropped: %q", got)
	}
	if !strings.Contains(got, "overloaded") {
		t.Fatalf("real cause was lost: %q", got)
	}
	if !strings.Contains(got, `"output"`) {
		t.Fatalf("output was not backfilled: %q", got)
	}
	if !strings.Contains(got, "response.created") {
		t.Fatalf("created frame lost: %q", got)
	}
}
