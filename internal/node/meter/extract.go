package meter

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
)

const (
	// maxRequestPeekBytes bounds the request-body prefix scanned for the model
	// id. SDKs serialize "model" first, so a small prefix is almost always
	// enough; a miss degrades to model "unknown" (booked unpriced server-side).
	maxRequestPeekBytes = 256 << 10
	// maxJSONBodyBytes bounds buffering of a non-streaming response body.
	maxJSONBodyBytes = 4 << 20
	// maxSSELineBytes bounds one SSE line; longer lines (large content deltas)
	// are skipped — the usage-bearing events are small.
	maxSSELineBytes = 512 << 10
)

// unknownModel books usage whose model id could not be read; the server counts
// the tokens and flags the event unpriced rather than guessing.
const unknownModel = "unknown"

// RequestInfo is what the meter learns from one intercepted LLM API request.
type RequestInfo struct {
	Model string
	// Inference is true only for completion-generating endpoints; anything else
	// on a metered origin (model listings, token counting) is forwarded
	// unmetered.
	Inference bool
}

// inferencePaths lists the completion-generating endpoints per provider.
var inferencePaths = map[string]map[string]bool{
	"anthropic": {
		"/v1/messages": true,
	},
	"openai": {
		"/v1/chat/completions": true,
		"/v1/completions":      true,
		"/v1/responses":        true,
	},
}

// ExtractRequest reads the model id from a bounded prefix of the request body,
// restoring the body for forwarding. It never fails the request: any parse
// trouble degrades to model "unknown".
func ExtractRequest(provider string, req *http.Request) RequestInfo {
	info := RequestInfo{
		Model:     unknownModel,
		Inference: inferencePaths[provider][requestPath(req)],
	}
	if !info.Inference || req.Body == nil || req.Body == http.NoBody {
		return info
	}

	prefix, rest := peekBody(req.Body, maxRequestPeekBytes)
	req.Body = rest
	if model := topLevelModel(prefix); model != "" {
		info.Model = model
	}
	return info
}

func requestPath(req *http.Request) string {
	if req == nil || req.URL == nil {
		return "/"
	}
	path := req.URL.EscapedPath()
	if path == "" {
		return "/"
	}
	return path
}

// peekBody reads up to limit bytes and returns them plus a ReadCloser that
// replays the peeked prefix before the remainder of the original body.
func peekBody(body io.ReadCloser, limit int) ([]byte, io.ReadCloser) {
	prefix, err := io.ReadAll(io.LimitReader(body, int64(limit)))
	if err != nil {
		// The bytes read so far are still owed to the upstream request.
		return nil, replayedBody(prefix, body)
	}
	return prefix, replayedBody(prefix, body)
}

type replayBody struct {
	io.Reader
	closer io.Closer
}

func (b replayBody) Close() error { return b.closer.Close() }

func replayedBody(prefix []byte, rest io.ReadCloser) io.ReadCloser {
	return replayBody{
		Reader: io.MultiReader(bytes.NewReader(prefix), rest),
		closer: rest,
	}
}

// topLevelModel walks JSON tokens and returns the top-level "model" string.
// Truncated input (the prefix cap) simply ends the walk — depth tracking keeps
// a "model" key nested inside messages from being mistaken for the request's.
func topLevelModel(prefix []byte) string {
	decoder := json.NewDecoder(bytes.NewReader(prefix))
	depth := 0
	expectKey := false
	for {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		switch value := token.(type) {
		case json.Delim:
			switch value {
			case '{':
				depth++
				expectKey = true
			case '}':
				depth--
				expectKey = depth == 1
			case '[', ']':
				expectKey = false
			}
		case string:
			if depth == 1 && expectKey {
				if value == "model" {
					next, err := decoder.Token()
					if err != nil {
						return ""
					}
					model, ok := next.(string)
					if !ok || model == "" || len(model) > 128 {
						return ""
					}
					return model
				}
				// Skip this key's whole value so nested objects never surface.
				if err := skipValue(decoder); err != nil {
					return ""
				}
			}
		}
	}
}

func skipValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok || (delim != '{' && delim != '[') {
		return nil
	}
	depth := 1
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

// UsageTotals is what a response sink accumulated.
type UsageTotals struct {
	InputTokens int
	// OutputTokens is nil when the response carried no output usage — never
	// estimated.
	OutputTokens        *int
	CacheCreationTokens int
	CacheReadTokens     int
	StopReason          string
	// Observed is true once any usage block was seen.
	Observed bool
}

// ResponseSink consumes a copy of the upstream response body and accumulates
// usage. Write NEVER returns an error: the sink taps the byte stream and must
// never be able to break it.
type ResponseSink interface {
	io.Writer
	Totals() UsageTotals
}

// NewResponseSink selects a parser from the response Content-Type. A nil
// return means the response shape is not parseable — forward untapped.
func NewResponseSink(resp *http.Response) ResponseSink {
	if resp == nil {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return nil
	}
	switch {
	case mediaType == "text/event-stream":
		return newSSESink()
	case mediaType == "application/json" || strings.HasSuffix(mediaType, "+json"):
		return &jsonSink{}
	default:
		return nil
	}
}

// wireUsage covers every provider response shape the meter reads:
// Anthropic message / message_start / message_delta, OpenAI chat completions,
// and OpenAI responses events. Unknown fields are ignored by design.
type wireUsage struct {
	Type       string         `json:"type"`
	Usage      *usageBlock    `json:"usage"`
	Message    *usageEnvelope `json:"message"`
	Delta      *wireDelta     `json:"delta"`
	Response   *usageEnvelope `json:"response"`
	Choices    []wireChoice   `json:"choices"`
	StopReason string         `json:"stop_reason"`
}

type usageEnvelope struct {
	Usage *usageBlock `json:"usage"`
}

type wireDelta struct {
	StopReason string `json:"stop_reason"`
}

type wireChoice struct {
	FinishReason *string `json:"finish_reason"`
}

type usageBlock struct {
	// Anthropic.
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	// OpenAI chat completions.
	PromptTokens        *int `json:"prompt_tokens"`
	CompletionTokens    *int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	// OpenAI responses API.
	InputTokensDetails *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

type accumulator struct {
	totals UsageTotals
}

// observe folds one decoded JSON object into the running totals. Later
// observations win (Anthropic's message_delta carries the cumulative output).
func (a *accumulator) observe(w wireUsage) {
	blocks := []*usageBlock{w.Usage}
	if w.Message != nil {
		blocks = append(blocks, w.Message.Usage)
	}
	if w.Response != nil {
		blocks = append(blocks, w.Response.Usage)
	}
	for _, block := range blocks {
		if block != nil {
			a.observeBlock(block)
		}
	}
	if w.Delta != nil && w.Delta.StopReason != "" {
		a.totals.StopReason = w.Delta.StopReason
	}
	if w.StopReason != "" {
		a.totals.StopReason = w.StopReason
	}
	for _, choice := range w.Choices {
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			a.totals.StopReason = *choice.FinishReason
		}
	}
}

func (a *accumulator) observeBlock(block *usageBlock) {
	a.totals.Observed = true
	cachedRead := 0
	if block.CacheReadInputTokens != nil {
		cachedRead = *block.CacheReadInputTokens
	}
	switch {
	case block.PromptTokens != nil:
		// OpenAI: prompt_tokens INCLUDES the cached share; split it out so the
		// token classes carry Anthropic semantics (input excludes cache reads).
		if block.PromptTokensDetails != nil && block.PromptTokensDetails.CachedTokens != nil {
			cachedRead = *block.PromptTokensDetails.CachedTokens
		}
		a.totals.InputTokens = max(0, *block.PromptTokens-cachedRead)
		a.totals.CacheReadTokens = cachedRead
	case block.InputTokens != nil:
		input := *block.InputTokens
		if block.InputTokensDetails != nil && block.InputTokensDetails.CachedTokens != nil {
			// OpenAI responses API: input_tokens includes the cached share too.
			cachedRead = *block.InputTokensDetails.CachedTokens
			input = max(0, input-cachedRead)
		}
		a.totals.InputTokens = input
		a.totals.CacheReadTokens = cachedRead
	case cachedRead > 0:
		a.totals.CacheReadTokens = cachedRead
	}
	if block.CacheCreationInputTokens != nil {
		a.totals.CacheCreationTokens = *block.CacheCreationInputTokens
	}
	if block.OutputTokens != nil {
		value := *block.OutputTokens
		a.totals.OutputTokens = &value
	}
	if block.CompletionTokens != nil {
		value := *block.CompletionTokens
		a.totals.OutputTokens = &value
	}
}

// jsonSink buffers a bounded non-streaming JSON body and decodes it once.
type jsonSink struct {
	buf      bytes.Buffer
	overflow bool
}

func (s *jsonSink) Write(p []byte) (int, error) {
	if !s.overflow {
		remaining := maxJSONBodyBytes - s.buf.Len()
		if len(p) > remaining {
			s.overflow = true
			s.buf.Reset() // a truncated JSON document cannot be decoded anyway
		} else {
			s.buf.Write(p)
		}
	}
	return len(p), nil
}

func (s *jsonSink) Totals() UsageTotals {
	var acc accumulator
	var w wireUsage
	if !s.overflow && json.Unmarshal(s.buf.Bytes(), &w) == nil {
		acc.observe(w)
	}
	return acc.totals
}

// sseSink incrementally scans SSE "data:" lines for usage-bearing events.
// Over-long lines (large content deltas) are skipped without buffering.
type sseSink struct {
	acc     accumulator
	line    []byte
	discard bool
}

func newSSESink() *sseSink {
	return &sseSink{line: make([]byte, 0, 4096)}
}

func (s *sseSink) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		index := bytes.IndexByte(p, '\n')
		if index < 0 {
			s.append(p)
			break
		}
		s.append(p[:index])
		s.finishLine()
		p = p[index+1:]
	}
	return total, nil
}

func (s *sseSink) append(chunk []byte) {
	if s.discard {
		return
	}
	if len(s.line)+len(chunk) > maxSSELineBytes {
		s.discard = true
		s.line = s.line[:0]
		return
	}
	s.line = append(s.line, chunk...)
}

func (s *sseSink) finishLine() {
	line := bytes.TrimSuffix(s.line, []byte("\r"))
	discarded := s.discard
	s.line = s.line[:0]
	s.discard = false
	if discarded {
		return
	}
	payload, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return
	}
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	var w wireUsage
	if json.Unmarshal(payload, &w) == nil {
		s.acc.observe(w)
	}
}

func (s *sseSink) Totals() UsageTotals {
	s.finishLine() // a final line without a trailing newline still counts
	return s.acc.totals
}
