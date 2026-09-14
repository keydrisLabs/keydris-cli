package meter

import (
	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
