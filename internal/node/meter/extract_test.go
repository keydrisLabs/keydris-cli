package meter

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sseResponse(body string) *http.Response {
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	response := recorder.Result()
	response.Body = io.NopCloser(strings.NewReader(body))
	return response
}

func jsonResponse() *http.Response {
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "application/json")
	return recorder.Result()
}

func drain(t *testing.T, sink ResponseSink, body string) UsageTotals {
	t.Helper()
	// Feed in small chunks so line reassembly across writes is exercised.
	for start := 0; start < len(body); start += 7 {
		end := start + 7
		if end > len(body) {
			end = len(body)
		}
		if _, err := sink.Write([]byte(body[start:end])); err != nil {
			t.Fatalf("sink write: %v", err)
		}
	}
	return sink.Totals()
}

func TestSSESinkAnthropicStream(t *testing.T) {
	body := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":1204,"cache_creation_input_tokens":16,"cache_read_input_tokens":98230,"output_tokens":3}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":356}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n"

	sink := NewResponseSink(sseResponse(body))
	totals := drain(t, sink, body)

	if !totals.Observed {
		t.Fatal("usage should be observed")
	}
	if totals.InputTokens != 1204 ||
		totals.CacheCreationTokens != 16 ||
		totals.CacheReadTokens != 98230 {
		t.Fatalf("input-side totals wrong: %+v", totals)
	}
	if totals.OutputTokens == nil || *totals.OutputTokens != 356 {
		t.Fatalf("output tokens should be the cumulative message_delta value: %+v", totals)
	}
	if totals.StopReason != "end_turn" {
		t.Fatalf("stop reason: %q", totals.StopReason)
	}
}

func TestSSESinkSkipsOverlongLinesButKeepsUsage(t *testing.T) {
	long := "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"" +
		strings.Repeat("x", maxSSELineBytes) + "\"}}\n"
	body := long +
		`data: {"type":"message_delta","usage":{"output_tokens":42}}` + "\n"

	sink := NewResponseSink(sseResponse(body))
	totals := drain(t, sink, body)
	if totals.OutputTokens == nil || *totals.OutputTokens != 42 {
		t.Fatalf("usage after an overlong line was lost: %+v", totals)
	}
}

func TestSSESinkOpenAIChatWithUsage(t *testing.T) {
	body := `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":null}],"usage":null}` + "\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":null}` + "\n" +
		`data: {"choices":[],"usage":{"prompt_tokens":640,"completion_tokens":120,"prompt_tokens_details":{"cached_tokens":512}}}` + "\n" +
		"data: [DONE]\n"

	sink := NewResponseSink(sseResponse(body))
	totals := drain(t, sink, body)
	// prompt_tokens includes the cached share; the meter splits the classes.
	if totals.InputTokens != 128 || totals.CacheReadTokens != 512 {
		t.Fatalf("OpenAI cached split wrong: %+v", totals)
	}
	if totals.OutputTokens == nil || *totals.OutputTokens != 120 {
		t.Fatalf("completion tokens: %+v", totals)
	}
	if totals.StopReason != "stop" {
		t.Fatalf("finish reason: %q", totals.StopReason)
	}
}

func TestSSESinkOpenAIStreamWithoutUsageLeavesOutputNil(t *testing.T) {
	body := `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":null}]}` + "\n" +
		"data: [DONE]\n"
	sink := NewResponseSink(sseResponse(body))
	totals := drain(t, sink, body)
	if totals.OutputTokens != nil {
		t.Fatalf("output tokens must never be guessed: %+v", totals)
	}
	if totals.Observed {
		t.Fatalf("no usage block was present: %+v", totals)
	}
}

func TestSSESinkOpenAIResponsesCompleted(t *testing.T) {
	body := `data: {"type":"response.output_text.delta","delta":"hi"}` + "\n" +
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":900,"output_tokens":77,"input_tokens_details":{"cached_tokens":100}}}}` + "\n"
	sink := NewResponseSink(sseResponse(body))
	totals := drain(t, sink, body)
	if totals.InputTokens != 800 || totals.CacheReadTokens != 100 {
		t.Fatalf("responses-API cached split wrong: %+v", totals)
	}
	if totals.OutputTokens == nil || *totals.OutputTokens != 77 {
		t.Fatalf("output tokens: %+v", totals)
	}
}

func TestJSONSinkAnthropicNonStreaming(t *testing.T) {
	body := `{"id":"msg_1","model":"claude-opus-5","stop_reason":"end_turn",` +
		`"usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":1,"cache_read_input_tokens":2}}`
	sink := NewResponseSink(jsonResponse())
	totals := drain(t, sink, body)
	if totals.InputTokens != 10 ||
		totals.OutputTokens == nil || *totals.OutputTokens != 20 ||
		totals.CacheCreationTokens != 1 || totals.CacheReadTokens != 2 ||
		totals.StopReason != "end_turn" {
		t.Fatalf("non-stream totals wrong: %+v", totals)
	}
}

func TestNewResponseSinkIgnoresUnparseableShapes(t *testing.T) {
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "application/octet-stream")
	if sink := NewResponseSink(recorder.Result()); sink != nil {
		t.Fatal("binary responses must not be tapped")
	}
}

func TestExtractRequestReadsTopLevelModelAndRestoresBody(t *testing.T) {
	// A nested "model" string inside messages must not win over the top-level.
	body := `{"messages":[{"role":"user","content":"the model field is a trap: \"model\":\"gpt-nope\""}],` +
		`"model":"claude-opus-5","stream":true,"max_tokens":64000}`
	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(body))

	info := ExtractRequest("anthropic", req)
	if !info.Inference {
		t.Fatal("POST /v1/messages is an inference request")
	}
	if info.Model != "claude-opus-5" {
		t.Fatalf("model: %q", info.Model)
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil || !bytes.Equal(restored, []byte(body)) {
		t.Fatalf("request body was not restored intact (err=%v)", err)
	}
}

func TestExtractRequestUnknownModelAndNonInferencePaths(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(`{"max_tokens":1}`))
	if info := ExtractRequest("anthropic", req); info.Model != unknownModel {
		t.Fatalf("missing model must degrade to %q, got %q", unknownModel, info.Model)
	}

	count := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages/count_tokens", strings.NewReader(`{"model":"claude-opus-5"}`))
	if info := ExtractRequest("anthropic", count); info.Inference {
		t.Fatal("count_tokens is not an inference request")
	}
}

// TestExtractRequestMetersOnlyPOSTInferenceCalls pins the reverted classifier:
// inference is exactly a POST to the provider's inference path. A WebSocket
// upgrade (a GET) and a nil request are not metered HTTP calls and must never
// reach the response sink.
func TestExtractRequestMetersOnlyPOSTInferenceCalls(t *testing.T) {
	upgrade := httptest.NewRequest(http.MethodGet, "https://api.openai.com/v1/responses", nil)
	upgrade.Header.Set("Connection", "Upgrade")
	upgrade.Header.Set("Upgrade", "websocket")
	if info := ExtractRequest("openai", upgrade); info.Inference {
		t.Fatalf("WebSocket upgrade classified as inference: %+v", info)
	}

	get := httptest.NewRequest(http.MethodGet, "https://api.openai.com/v1/chat/completions", nil)
	if info := ExtractRequest("openai", get); info.Inference {
		t.Fatalf("GET classified as inference: %+v", info)
	}

	if info := ExtractRequest("openai", nil); info.Inference || info.Model != unknownModel {
		t.Fatalf("nil request = %+v, want model %q and no inference", info, unknownModel)
	}
}
