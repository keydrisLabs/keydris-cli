package meter

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
)

func TestLargeResponsesUsageAndModel(t *testing.T) {
	for _, stream := range []bool{false, true} {
		response := `{"model":"gpt-5.4","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"content":"` + strings.Repeat("x", 5<<20) + `"}],"usage":{"input_tokens":900,"output_tokens":77,"input_tokens_details":{"cached_tokens":100}}}`
		var sink ResponseSink
		if stream {
			response = `data: {"type":"response.incomplete","response":` + response + "}\n\n"
			sink = NewResponseSink(sseResponse(response))
		} else {
			sink = NewResponseSink(jsonResponse())
		}
		totals := drain(t, sink, response)
		if totals.Model != "gpt-5.4" || totals.InputTokens != 800 || totals.OutputTokens == nil || *totals.OutputTokens != 77 || totals.StopReason != "max_output_tokens" {
			t.Fatalf("stream=%v totals=%+v", stream, totals)
		}
	}
}
func TestResponseModelRecoversRequestHint(t *testing.T) {
	req := httptest.NewRequest("POST", "https://api.openai.com/v1/responses", strings.NewReader(`{"input":"`+strings.Repeat("x", 257<<10)+`","model":"gpt-5.4"}`))
	info := ExtractRequest("openai", req)
	totals := drain(t, NewResponseSink(jsonResponse()), `{"model":"gpt-5.4","usage":{"input_tokens":80000,"output_tokens":12}}`)
	if event := BuildEvent("openai", info, totals, time.Now()); event.Model != "gpt-5.4" {
		t.Fatal(event.Model)
	}
}
func TestMetadataIgnoresNestedSpoofAndMalformedDocument(t *testing.T) {
	for _, body := range []string{`{"output":{"usage":{"input_tokens":9,"output_tokens":9}},"model":"gpt-5.4"}`, `{"usage":{"input_tokens":9,"output_tokens":9},}`, `{"usage":{"input_tokens":9,"output_tokens":9}}garbage`} {
		totals := drain(t, NewResponseSink(jsonResponse()), body)
		if totals.Observed {
			t.Fatalf("unexpected usage: %+v", totals)
		}
	}
}
func TestMeterFinalJoinsInFlightAndRetriesBeforeReturning(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			close(started)
			<-r.Context().Done()
			return
		}
		if r.Header.Get("Authorization") != "Bearer current-kit" {
			t.Error("wrong token")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"schema_version":1,"accepted":1,"duplicates":0}`))
	}))
	defer ts.Close()
	reg := attest.NewSessionRegistry()
	reg.Register(attest.Session{Handle: testHandle, SVID: "current-kit"})
	m := New(ts.Client(), ts.URL, reg, t.Logf)
	defer m.Close()
	m.Record(testHandle, testEvent("retry-me"))
	done := make(chan struct{})
	go func() { m.flushHandle(testHandle, nil); close(done) }()
	<-started
	departed, _ := reg.Take(testHandle)
	m.FlushSessionFinal(departed)
	<-done
	if calls.Load() != 2 {
		t.Fatalf("got %d calls; final flush must deliver the retried event", calls.Load())
	}
	m.mu.Lock()
	remaining := len(m.buffers[testHandle])
	m.mu.Unlock()
	if remaining != 0 {
		t.Fatal(remaining)
	}
}
func FuzzMetadataJSON(f *testing.F) {
	f.Add([]byte(`{"model":"gpt-5.4","usage":{"input_tokens":42,"output_tokens":5}}`))
	f.Add([]byte(`{"content":"huge ignored content","usage":null}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		var s metadataJSON
		for _, c := range b {
			s.write([]byte{c})
		}
		var a accumulator
		s.observe(&a)
		if len(s.token) > maxMetadataScalar || len(s.frames) > maxJSONDepth {
			t.Fatal("unbounded parser")
		}
	})
}

func TestOpenAIActualTierAndCacheWriteSplit(t *testing.T) {
	body := "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra\",\"service_tier\":\"fast\",\"usage\":{\"input_tokens\":300000,\"output_tokens\":12,\"input_tokens_details\":{\"cached_tokens\":200000,\"cache_write_tokens\":50000}}}}\r\n\r\n"
	totals := drain(t, NewResponseSink(sseResponse(body)), body)
	event := BuildEvent("openai", RequestInfo{Model: "request-alias"}, totals, time.Now())
	if event.Model != "gpt-6-astra" || event.ServiceTier != "priority" || event.InputTokens != 50000 || event.CacheReadTokens != 200000 || event.CacheCreationTokens != 50000 || event.OutputTokens == nil || *event.OutputTokens != 12 {
		t.Fatalf("%+v", event)
	}
}

func TestAnthropicSpeedAndServiceTier(t *testing.T) {
	cases := []struct{ usage, tier string }{
		{`,"service_tier":"standard","speed":"standard"`, "default"},
		{`,"service_tier":"standard","speed":"fast"`, "priority"},
		{`,"service_tier":"priority"`, "unknown"},
		{``, "default"},
	}
	for _, tc := range cases {
		body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-opus-5\",\"usage\":{\"input_tokens\":8,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0,\"output_tokens\":1" + tc.usage + "}}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":12}}\n\n"
		totals := drain(t, NewResponseSink(sseResponse(body)), body)
		event := BuildEvent("anthropic", RequestInfo{Model: "request-alias"}, totals, time.Now())
		if event.Model != "claude-opus-5" || event.ServiceTier != tc.tier || event.OutputTokens == nil || *event.OutputTokens != 12 {
			t.Fatalf("%q: %+v", tc.usage, event)
		}
	}
	body := `{"model":"claude-opus-4-8","usage":{"input_tokens":8,"output_tokens":3,"speed":"fast"}}`
	event := BuildEvent("anthropic", RequestInfo{Model: "claude-opus-4-8"}, drain(t, NewResponseSink(jsonResponse()), body), time.Now())
	if event.ServiceTier != "priority" || event.Model != "claude-opus-4-8" {
		t.Fatalf("non-streaming fast mode: %+v", event)
	}
}

func TestIncompleteOrInvalidUsageCannotProducePricedEvent(t *testing.T) {
	for _, body := range []string{
		`{"usage":{"output_tokens":5}}`,
		`{"usage":{"input_tokens":2,"output_tokens":5,"input_tokens_details":{"cached_tokens":3}}}`,
		`{"usage":{"input_tokens":-1,"output_tokens":5}}`,
	} {
		totals := drain(t, NewResponseSink(jsonResponse()), body)
		event := BuildEvent("openai", RequestInfo{Model: "gpt-6-astra"}, totals, time.Now())
		if event.OutputTokens != nil || event.ServiceTier != "unknown" {
			t.Fatalf("%s: %+v", body, event)
		}
	}
}

// TestBuildEventWireShape pins the 1.4.0 usage event contract: the outbound
// event carries only provider counters. The Codex-specific usage_source and
// usage_transport fields the reverted release added must not reappear.
func TestBuildEventWireShape(t *testing.T) {
	output := 5
	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages",
		strings.NewReader(`{"model":"claude-opus-5"}`))
	info := ExtractRequest("anthropic", req)
	totals := UsageTotals{
		Observed:     true,
		HasInput:     true,
		Model:        "claude-opus-5",
		InputTokens:  10,
		OutputTokens: &output,
	}
	raw, err := json.Marshal(BuildEvent("anthropic", info, totals, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"request_id": true, "provider": true, "model": true, "service_tier": true,
		"input_tokens": true, "output_tokens": true, "cache_creation_tokens": true,
		"cache_read_tokens": true, "occurred_at": true,
	}
	if len(fields) != len(want) {
		t.Fatalf("event fields = %v", fields)
	}
	for name := range fields {
		if !want[name] {
			t.Fatalf("unexpected wire field %q in %s", name, raw)
		}
	}
}
