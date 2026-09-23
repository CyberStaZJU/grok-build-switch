package streamguard

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestChatChunkUnsupportedRecognisesHeartbeatsButKeepsChunks(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		want  bool
	}{
		{"keepalive", "data: {\"type\":\"keepalive\"}\n\n", true},
		{"heartbeat spaced", "data:  {\"type\": \"heartbeat\"} \n\n", true},
		{"responses event leaked into chat", "data: {\"type\":\"response.created\"}\n\n", true},
		{"normal chunk", "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n", false},
		{"empty choices chunk", "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[]}\n\n", false},
		{"error frame preserved", "data: {\"error\":{\"message\":\"boom\",\"type\":\"api_error\"}}\n\n", false},
		{"done sentinel", "data: [DONE]\n\n", false},
		{"unparseable passes", "data: not-json\n\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ChatChunkUnsupported([]byte(tc.frame)); got != tc.want {
				t.Fatalf("ChatChunkUnsupported(%q) = %v, want %v", tc.frame, got, tc.want)
			}
		})
	}
}

func TestSanitizeStreamChatModeDropsHeartbeatKeepsChunks(t *testing.T) {
	in := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"type\":\"keepalive\"}\n\n" +
		"data: [DONE]\n\n"

	var out bytes.Buffer
	if err := SanitizeStream(&out, strings.NewReader(in), ModeChatCompletions); err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "keepalive") {
		t.Fatalf("heartbeat survived: %q", got)
	}
	want := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: [DONE]\n\n"
	if got != want {
		t.Fatalf("stream changed beyond the dropped frame:\n got %q\nwant %q", got, want)
	}
}

func TestSanitizeStreamResponsesModeKeepsChatShapedFrames(t *testing.T) {
	// A Responses stream containing a bare chunk still passes through: the
	// Responses filter only rejects unknown `type` values.
	in := "data: {\"id\":\"c1\",\"choices\":[{\"delta\":{}}]}\n\n"
	var out bytes.Buffer
	if err := SanitizeStream(&out, strings.NewReader(in), ModeResponses); err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if out.String() != in {
		t.Fatalf("got %q want %q", out.String(), in)
	}
}

func TestSanitizeResponsesStreamDropsUnknownEventType(t *testing.T) {
	in := "event: response.created\n" +
		`data: {"type":"response.created","sequence_number":0}` + "\n\n" +
		"event: keepalive\n" +
		`data: {"type":"keepalive"}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","sequence_number":2}` + "\n\n"

	var out bytes.Buffer
	if err := SanitizeResponsesStream(&out, strings.NewReader(in)); err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "keepalive") {
		t.Fatalf("keepalive frame survived: %q", got)
	}
	want := "event: response.created\n" +
		`data: {"type":"response.created","sequence_number":0}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","sequence_number":2}` + "\n\n"
	if got != want {
		t.Fatalf("stream changed beyond the dropped frame:\n got %q\nwant %q", got, want)
	}
}

func TestSanitizeResponsesStreamPassesConformingStreamByteForByte(t *testing.T) {
	in := "event: response.created\n" +
		`data: {"type":"response.created","sequence_number":0}` + "\n\n" +
		":\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","sequence_number":3}` + "\n\n" +
		"data: [DONE]\n\n"

	var out bytes.Buffer
	if err := SanitizeResponsesStream(&out, strings.NewReader(in)); err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if out.String() != in {
		t.Fatalf("conforming stream not passed through:\n got %q\nwant %q", out.String(), in)
	}
}

func TestEventTypeUnsupportedRecognisesOffendingFrame(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		want  bool
	}{
		{"keepalive", "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n", true},
		{"heartbeat", "data: {\"type\":\"heartbeat\"}\n\n", true},
		{"spaced json", "data:   {\"type\": \"keepalive\"}  \n\n", true},
		{"known type", "data: {\"type\":\"response.in_progress\"}\n\n", false},
		{"error type allowed", "data: {\"type\":\"error\"}\n\n", false},
		{"done sentinel", "data: [DONE]\n\n", false},
		{"unparseable passes", "data: not-json\n\n", false},
		{"no type field", "data: {\"foo\":1}\n\n", false},
		{"comment only", ":\n\n", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EventTypeUnsupported([]byte(tc.frame)); got != tc.want {
				t.Fatalf("EventTypeUnsupported(%q) = %v, want %v", tc.frame, got, tc.want)
			}
		})
	}
}

func TestSanitizeResponsesStreamFlushesFrameWithoutTrailingBlankLine(t *testing.T) {
	in := "event: response.created\n" + `data: {"type":"response.created"}` + "\n"
	var out bytes.Buffer
	if err := SanitizeResponsesStream(&out, strings.NewReader(in)); err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if out.String() != in {
		t.Fatalf("final unterminated frame dropped: got %q want %q", out.String(), in)
	}
}

func TestSanitizeResponsesStreamKeepsTrailingUnknownFrameOut(t *testing.T) {
	in := "event: keepalive\n" + `data: {"type":"keepalive"}` + "\n"
	var out bytes.Buffer
	if err := SanitizeResponsesStream(&out, strings.NewReader(in)); err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("trailing unknown frame survived: %q", out.String())
	}
}

type failWriter struct{ err error }

func (f failWriter) Write([]byte) (int, error) { return 0, f.err }

func TestSanitizeResponsesStreamPropagatesWriteError(t *testing.T) {
	sentinel := errors.New("boom")
	in := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"
	err := SanitizeResponsesStream(failWriter{err: sentinel}, strings.NewReader(in))
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
}

func TestSanitizeResponsesStreamPropagatesReadError(t *testing.T) {
	sentinel := errors.New("readfail")
	err := SanitizeResponsesStream(io.Discard, errReader{err: sentinel})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
