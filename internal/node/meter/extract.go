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
	// Only the request hint is bounded; response metadata is read incrementally.
	maxRequestPeekBytes = 256 << 10
	// Kept as a regression-test boundary; SSE lines may now exceed this size.
	maxSSELineBytes = 512 << 10
)

// unknownModel books usage whose model id could not be read; the server counts
// the tokens and flags the event unpriced rather than guessing.
const unknownModel = "unknown"

// RequestInfo is what the meter learns from one intercepted LLM API request.
type RequestInfo struct {
	Model     string
	Source    string
	Transport string
	Responses bool
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
	return ExtractRequestAt(provider, "", req)
}

// ExtractRequestAt classifies using the CONNECT destination, never a client
// header claiming a provider or login method. ChatGPT's other endpoints are
// forwarded without collecting metadata.
func ExtractRequestAt(provider, host string, req *http.Request) RequestInfo {
	info := RequestInfo{
		Model: unknownModel, Source: "provider_api", Transport: "http",
	}
	if req == nil {
		return info
	}
	path := requestPath(req)
	allowed := inferencePaths[provider][path]
	info.Responses = provider == "openai" && path == "/v1/responses"
	if strings.EqualFold(host, "chatgpt.com") {
		info.Source = "codex_chatgpt"
		allowed = provider == "openai" && path == "/backend-api/codex/responses"
		info.Responses = allowed
	}
	info.Inference = allowed && req.Method == http.MethodPost
	if info.Responses && req.Method == http.MethodGet && strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
		for _, token := range strings.Split(req.Header.Get("Connection"), ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				info.Inference, info.Transport = true, "websocket"
			}
		}
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
	// ServiceTier is the provider's reported tier: OpenAI's top-level field or
	// Anthropic's usage.service_tier (standard, priority, batch).
	ServiceTier string
	// Speed is Anthropic's usage.speed (fast or standard); empty elsewhere.
	Speed       string
	HasInput    bool
	Invalid     bool
	Model       string
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
	ServiceTier      string
	Model            string
	Status           string
	IncompleteReason string
	Type             string         `json:"type"`
	Usage            *usageBlock    `json:"usage"`
	Message          *usageEnvelope `json:"message"`
	Delta            *wireDelta     `json:"delta"`
	Response         *usageEnvelope `json:"response"`
	Choices          []wireChoice   `json:"choices"`
	StopReason       string         `json:"stop_reason"`
}

type usageEnvelope struct {
	ServiceTier      string
	Model            string
	Status           string
	IncompleteReason string
	Usage            *usageBlock `json:"usage"`
}

type wireDelta struct {
	StopReason string `json:"stop_reason"`
}

type wireChoice struct {
	FinishReason *string `json:"finish_reason"`
}

type usageBlock struct {
	// Anthropic.
	ServiceTier              string `json:"service_tier"`
	Speed                    string `json:"speed"`
	InputTokens              *int   `json:"input_tokens"`
	OutputTokens             *int   `json:"output_tokens"`
	CacheCreationInputTokens *int   `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int   `json:"cache_read_input_tokens"`
	// OpenAI chat completions.
	PromptTokens        *int          `json:"prompt_tokens"`
	CompletionTokens    *int          `json:"completion_tokens"`
	PromptTokensDetails *tokenDetails `json:"prompt_tokens_details"`
	// OpenAI responses API.
	InputTokensDetails *tokenDetails `json:"input_tokens_details"`
}

type tokenDetails struct {
	CachedTokens     *int `json:"cached_tokens"`
	CacheWriteTokens *int `json:"cache_write_tokens"`
}

type accumulator struct {
	totals UsageTotals
}

// observe folds one decoded JSON object into the running totals. Later
// observations win (Anthropic's message_delta carries the cumulative output).
func (a *accumulator) observe(w wireUsage) {
	if w.ServiceTier != "" {
		a.totals.ServiceTier = w.ServiceTier
	}
	if w.Response != nil && w.Response.ServiceTier != "" {
		a.totals.ServiceTier = w.Response.ServiceTier
	}
	if w.Model != "" {
		a.totals.Model = w.Model
	}
	if w.Message != nil && w.Message.Model != "" {
		a.totals.Model = w.Message.Model
	}
	if w.Response != nil {
		if w.Response.Model != "" {
			a.totals.Model = w.Response.Model
		}
		if w.Response.Status != "" {
			a.totals.StopReason = w.Response.Status
		}
		if w.Response.IncompleteReason != "" {
			a.totals.StopReason = w.Response.IncompleteReason
		}
	}
	if w.Status != "" {
		a.totals.StopReason = w.Status
	}
	if w.IncompleteReason != "" {
		a.totals.StopReason = w.IncompleteReason
	}
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
	if block.ServiceTier != "" {
		a.totals.ServiceTier = block.ServiceTier
	}
	if block.Speed != "" {
		a.totals.Speed = block.Speed
	}
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
		a.totals.HasInput = true
		writes := 0
		if block.PromptTokensDetails != nil && block.PromptTokensDetails.CacheWriteTokens != nil {
			writes = *block.PromptTokensDetails.CacheWriteTokens
		}
		if cachedRead+writes > *block.PromptTokens {
			a.totals.Invalid = true
		}
		a.totals.InputTokens = max(0, *block.PromptTokens-cachedRead-writes)
		a.totals.CacheCreationTokens = writes
		a.totals.CacheReadTokens = cachedRead
	case block.InputTokens != nil:
		a.totals.HasInput = true
		input := *block.InputTokens
		if block.InputTokensDetails != nil && block.InputTokensDetails.CachedTokens != nil {
			// OpenAI responses API: input_tokens includes the cached share too.
			cachedRead = *block.InputTokensDetails.CachedTokens
			input = max(0, input-cachedRead)
		}
		if block.InputTokensDetails != nil && block.InputTokensDetails.CacheWriteTokens != nil {
			a.totals.CacheCreationTokens = *block.InputTokensDetails.CacheWriteTokens
			input -= a.totals.CacheCreationTokens
		}
		if block.InputTokensDetails != nil && (input < 0 || cachedRead > *block.InputTokens) {
			a.totals.Invalid = true
		}
		input = max(0, input)
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

// Both response formats use the same incremental, content-discarding parser.
type jsonSink struct {
	parser  metadataJSON
	observe func(metadataJSON)
}

func (s *jsonSink) Write(p []byte) (int, error) { s.parser.write(p); return len(p), nil }
func (s *jsonSink) Totals() UsageTotals {
	if s.observe != nil {
		s.observe(s.parser)
		s.observe = nil
	}
	var acc accumulator
	s.parser.observe(&acc)
	return acc.totals
}

type sseSink struct {
	observe   func(metadataJSON)
	eventData bool
	acc       accumulator
	parser    metadataJSON
	prefix    []byte
	data      bool
	ignore    bool
	lineBytes int
}

func newSSESink() *sseSink { return &sseSink{} }
func (s *sseSink) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		n := bytes.IndexByte(p, '\n')
		if n < 0 {
			s.writeLine(p)
			break
		}
		s.writeLine(p[:n])
		s.finishLine()
		p = p[n+1:]
	}
	return total, nil
}
func (s *sseSink) writeLine(p []byte) {
	s.lineBytes += len(p)
	if s.ignore {
		return
	}
	for len(p) > 0 && !s.data {
		s.prefix = append(s.prefix, p[0])
		p = p[1:]
		if !bytes.HasPrefix([]byte("data:"), s.prefix) {
			s.ignore = true
			return
		}
		if len(s.prefix) == 5 {
			s.data = true
		}
	}
	if s.data {
		s.parser.write(p)
	}
}
func (s *sseSink) finishLine() {
	if s.data {
		s.parser.write([]byte{'\n'})
		s.eventData = true
		if s.observe == nil && (s.parser.done || s.parser.bad) {
			s.parser.observe(&s.acc)
			s.parser = metadataJSON{}
		}
	} else if s.lineBytes == 0 || (s.lineBytes == 1 && bytes.Equal(s.prefix, []byte{'\r'})) {
		s.dispatchEvent()
		s.parser = metadataJSON{}
	}
	s.prefix = s.prefix[:0]
	s.data = false
	s.ignore = false
	s.lineBytes = 0
}

func (s *sseSink) dispatchEvent() {
	if s.observe != nil && s.eventData {
		s.observe(s.parser)
	}
	s.eventData = false
}

func (s *sseSink) Totals() UsageTotals { s.finishLine(); s.dispatchEvent(); return s.acc.totals }
