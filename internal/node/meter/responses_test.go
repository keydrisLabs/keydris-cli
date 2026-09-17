package meter

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

func TestCodexInferenceClassification(t *testing.T) {
	for _, tc := range []struct {
		host, method, path, upgrade, source, transport string
		inference                                      bool
	}{
		{"chatgpt.com", "POST", "/backend-api/codex/responses?foo=bar", "", "codex_chatgpt", "http", true},
		{"chatgpt.com", "GET", "/backend-api/codex/responses", "websocket", "codex_chatgpt", "websocket", true},
		{"api.openai.com", "GET", "/v1/responses", "websocket", "provider_api", "websocket", true},
		{"api.openai.com", "POST", "/v1/responses", "", "provider_api", "http", true},
		{"chatgpt.com", "POST", "/v1/responses", "", "codex_chatgpt", "http", false},
		{"chatgpt.com", "GET", "/backend-api/codex/responses", "", "codex_chatgpt", "http", false},
		{"chatgpt.com", "POST", "/backend-api/codex/responses/compact", "", "codex_chatgpt", "http", false},
		{"chatgpt.com", "GET", "/backend-api/codex/models", "", "codex_chatgpt", "http", false},
		{"api.openai.com", "POST", "/backend-api/codex/responses", "", "provider_api", "http", false},
	} {
		t.Run(tc.host+tc.method+tc.path+tc.upgrade, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "https://"+tc.host+tc.path, strings.NewReader(`{"model":"gpt-test"}`))
			if tc.upgrade != "" {
				r.Header.Set("Upgrade", tc.upgrade)
				r.Header.Set("Connection", "keep-alive, Upgrade")
			}
			info := ExtractRequestAt("openai", tc.host, r)
			if info.Inference != tc.inference || info.Source != tc.source || info.Transport != tc.transport {
				t.Fatalf("%+v", info)
			}
		})
	}
	o := NewOrigins(nil)
	if provider, ok := o.Provider("CHATGPT.COM", 443); !ok || provider != "openai" {
		t.Fatal("ChatGPT origin missing")
	}
	if _, ok := o.Provider("evil.chatgpt.com", 443); ok {
		t.Fatal("wildcard metering")
	}
	if _, ok := NewOrigins([]string{"chatgpt.com=off"}).Provider("chatgpt.com", 443); ok {
		t.Fatal("opt-out ignored")
	}
}

func terminalResponse(id string) string {
	return fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"model":"gpt-test","status":"completed","usage":{"input_tokens":100,"output_tokens":12,"input_tokens_details":{"cached_tokens":30}}}}`, id)
}

func responseCollector(t *testing.T, transport string) (*ResponsesCollector, *[]runtimecontract.SessionUsageEvent, *int) {
	t.Helper()
	events := []runtimecontract.SessionUsageEvent{}
	warnings := 0
	c := NewResponsesCollector("openai", RequestInfo{Model: unknownModel, Source: "codex_chatgpt", Transport: transport},
		func(e runtimecontract.SessionUsageEvent) { events = append(events, e) }, func(string) { warnings++ })
	return c, &events, &warnings
}

func observeMessage(c *ResponsesCollector, message string) {
	var p metadataJSON
	p.write([]byte(message))
	c.observe(p)
}

func TestResponsesPerResponseIdentityAndInterleaving(t *testing.T) {
	c, events, _ := responseCollector(t, "websocket")
	for _, id := range []string{"a", "b"} {
		observeMessage(c, fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, id))
	}
	observeMessage(c, `{"type":"response.output_text.delta","delta":"private output"}`)
	for _, id := range []string{"b", "a", "b"} {
		observeMessage(c, terminalResponse(id))
	}
	if len(*events) != 2 {
		t.Fatalf("got %d events", len(*events))
	}
	for _, e := range *events {
		if e.InputTokens != 70 || e.CacheReadTokens != 30 || e.OutputTokens == nil || *e.OutputTokens != 12 || e.UsageSource != "codex_chatgpt" || e.UsageTransport != "websocket" || e.ServiceTier != "unknown" {
			t.Fatalf("%+v", e)
		}
	}
	h, replay, _ := responseCollector(t, "http")
	observeMessage(h, terminalResponse("b"))
	if (*replay)[0].RequestID != (*events)[0].RequestID {
		t.Fatal("fallback/reconnect changes identity")
	}
	if (*events)[0].RequestID == (*events)[1].RequestID {
		t.Fatal("different responses collapsed")
	}
}

func TestResponsesHTTPReportsBeforeStreamCloses(t *testing.T) {
	c, events, _ := responseCollector(t, "http")
	s := c.HTTPSink(&http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream"}}})
	s.Write([]byte("data: " + terminalResponse("live") + "\n\n"))
	if len(*events) != 1 {
		t.Fatal("report waits for socket EOF")
	}
	s.Totals()
	if len(*events) != 1 {
		t.Fatal("EOF duplicated response")
	}
	c2, jsonEvents, _ := responseCollector(t, "http")
	s2 := c2.HTTPSink(&http.Response{Header: http.Header{"Content-Type": []string{"application/json"}}})
	s2.Write([]byte(`{"id":"json","status":"incomplete","model":"gpt-test","usage":{"input_tokens":5,"output_tokens":2}}`))
	s2.Totals()
	s2.Totals()
	if len(*jsonEvents) != 1 || (*jsonEvents)[0].StopReason != "incomplete" {
		t.Fatalf("%+v", *jsonEvents)
	}
}

func TestResponsesRejectAmbiguousMetadataAndBoundState(t *testing.T) {
	c, events, warnings := responseCollector(t, "websocket")
	for _, message := range []string{
		strings.Replace(terminalResponse("a"), `"input_tokens":100,"output_tokens":12,"input_tokens_details":{"cached_tokens":30}`, `"input_tokens":100},"usage":{"output_tokens":12`, 1),
		strings.Replace(terminalResponse("a"), `"input_tokens":100`, `"input_tokens":100,"input_tokens":200`, 1),
		terminalResponse("a") + `{}`,
		strings.Replace(terminalResponse("a"), `"id":"a"`, `"id":"a","id":"b"`, 1),
	} {
		observeMessage(c, message)
	}
	if len(*events) != 0 || *warnings != 1 {
		t.Fatalf("events=%v warnings=%d", *events, *warnings)
	}
	for i := 0; i < 300; i++ {
		observeMessage(c, fmt.Sprintf(`{"type":"response.created","response":{"id":"%d"}}`, i))
		observeMessage(c, terminalResponse(fmt.Sprint(i)))
	}
	if len(c.seen) > maxTrackedResponses || len(c.started) > maxTrackedResponses || len(c.order) > maxTrackedResponses {
		t.Fatal("unbounded response state")
	}
}

func TestResponsesSSEDoesNotEmitBeforeEventBoundary(t *testing.T) {
	c, events, warnings := responseCollector(t, "http")
	s := c.HTTPSink(&http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream"}}})
	s.Write([]byte("data: " + terminalResponse("invalid") + "\n"))
	if len(*events) != 0 {
		t.Fatal("SSE emitted before event boundary")
	}
	s.Write([]byte("data: {}\n\n"))
	if len(*events) != 0 || *warnings != 1 {
		t.Fatal("SSE accepted trailing JSON")
	}
}

func TestWebSocketCompressedObservationLimits(t *testing.T) {
	c, events, warnings := responseCollector(t, "websocket")
	s := c.WebSocketSink(wsResponse("permessage-deflate")).(*websocketSink)
	frame := wsFrame(1, true, true, make([]byte, maxCompressedMessage+1))
	if n, err := s.Write(frame); n != len(frame) || err != nil {
		t.Fatal(n, err)
	}
	if !s.disabled || len(*events) != 0 || *warnings != 1 || len(s.packed) != 0 {
		t.Fatal("compression limit did not disable and release observer")
	}
}

func TestWebSocketInvalidDeflateNeverReportsUsage(t *testing.T) {
	c, events, warnings := responseCollector(t, "websocket")
	s := c.WebSocketSink(wsResponse("permessage-deflate"))
	frame := wsFrame(1, true, true, []byte{0xff, 0xff, 0xff})
	if n, err := s.Write(frame); n != len(frame) || err != nil {
		t.Fatal(n, err)
	}
	if len(*events) != 0 || *warnings != 1 {
		t.Fatal("invalid deflate was accepted")
	}
}

func wsFrame(op byte, final, compressed bool, payload []byte) []byte {
	b := op
	if final {
		b |= 128
	}
	if compressed {
		b |= 64
	}
	frame := []byte{b}
	switch {
	case len(payload) < 126:
		frame = append(frame, byte(len(payload)))
	case len(payload) < 65536:
		frame = append(frame, 126, byte(len(payload)>>8), byte(len(payload)))
	default:
		frame = append(frame, 127)
		frame = binary.BigEndian.AppendUint64(frame, uint64(len(payload)))
	}
	return append(frame, payload...)
}

func wsResponse(extension string) *http.Response {
	return &http.Response{StatusCode: 101, Header: http.Header{"Sec-Websocket-Extensions": []string{extension}}}
}

func TestWebSocketFragmentsControlFramesAndMultipleResponses(t *testing.T) {
	c, events, warnings := responseCollector(t, "websocket")
	s := c.WebSocketSink(wsResponse(""))
	p := []byte(terminalResponse("first"))
	frames := wsFrame(1, false, false, p[:20])
	frames = append(frames, wsFrame(9, true, false, []byte("ping"))...)
	frames = append(frames, wsFrame(0, true, false, p[20:])...)
	frames = append(frames, wsFrame(1, true, false, []byte(terminalResponse("second")))...)
	for _, b := range frames {
		if n, err := s.Write([]byte{b}); n != 1 || err != nil {
			t.Fatal(n, err)
		}
	}
	if len(*events) != 2 || *warnings != 0 {
		t.Fatalf("events=%d warnings=%d", len(*events), *warnings)
	}
}

func TestWebSocketDeflateWithAndWithoutContextTakeover(t *testing.T) {
	for _, noContext := range []bool{false, true} {
		t.Run(fmt.Sprint(noContext), func(t *testing.T) {
			c, events, warnings := responseCollector(t, "websocket")
			ext := "permessage-deflate"
			if noContext {
				ext += "; server_no_context_takeover"
			}
			s := c.WebSocketSink(wsResponse(ext))
			var compressed bytes.Buffer
			w, _ := flate.NewWriter(&compressed, flate.DefaultCompression)
			defer w.Close()
			for i := 0; i < 3; i++ {
				if noContext {
					w.Reset(&compressed)
				}
				before := compressed.Len()
				w.Write([]byte(terminalResponse(fmt.Sprint(i))))
				w.Flush()
				packed := append([]byte(nil), compressed.Bytes()[before:compressed.Len()-4]...)
				frame := wsFrame(1, true, true, packed)
				for start := 0; start < len(frame); start += 3 {
					s.Write(frame[start:min(start+3, len(frame))])
				}
			}
			if len(*events) != 3 || *warnings != 0 {
				t.Fatalf("events=%d warnings=%d", len(*events), *warnings)
			}
		})
	}
}

func TestWebSocketUnsupportedOrMalformedNeverBreaksForwarding(t *testing.T) {
	for _, frame := range [][]byte{{0x81, 0x80}, {0xc1, 0}, wsFrame(0, true, false, []byte("orphan")), wsFrame(9, false, false, nil), wsFrame(2, true, false, []byte{1, 2})} {
		c, events, warnings := responseCollector(t, "websocket")
		s := c.WebSocketSink(wsResponse(""))
		if n, err := s.Write(frame); n != len(frame) || err != nil {
			t.Fatal(n, err)
		}
		if len(*events) != 0 || *warnings != 1 {
			t.Fatalf("events=%d warnings=%d", len(*events), *warnings)
		}
	}
	c, _, warnings := responseCollector(t, "websocket")
	if c.WebSocketSink(wsResponse("unknown-extension")) != nil || *warnings != 1 {
		t.Fatal("unknown extension not diagnosed")
	}
}

func TestWebSocketLargeContentIsNotRetained(t *testing.T) {
	c, events, _ := responseCollector(t, "websocket")
	s := c.WebSocketSink(wsResponse("")).(*websocketSink)
	p := strings.Replace(terminalResponse("large"), `"model":"gpt-test"`, `"output":[{"content":"`+strings.Repeat("x", 5<<20)+`"}],"model":"gpt-test"`, 1)
	frame := wsFrame(1, true, false, []byte(p))
	for start := 0; start < len(frame); start += 8192 {
		s.Write(frame[start:min(start+8192, len(frame))])
	}
	if len(*events) != 1 || len(s.packed) != 0 || len(s.parser.token) != 0 {
		t.Fatal("large content lost usage or retained content")
	}
}

func FuzzWebSocketObservation(f *testing.F) {
	f.Add(wsFrame(1, true, false, []byte(terminalResponse("seed"))))
	f.Add([]byte{0x81, 127, 255, 255, 255, 255, 255, 255, 255, 255})
	f.Fuzz(func(t *testing.T, data []byte) {
		c, _, _ := responseCollector(t, "websocket")
		s := c.WebSocketSink(wsResponse("")).(*websocketSink)
		for start := 0; start < len(data); start += 7 {
			p := data[start:min(start+7, len(data))]
			if n, err := s.Write(p); n != len(p) || err != nil {
				t.Fatal(n, err)
			}
		}
		if len(s.packed) > maxCompressedMessage || len(s.dictionary) > deflateWindow {
			t.Fatal("unbounded observation")
		}
	})
}
