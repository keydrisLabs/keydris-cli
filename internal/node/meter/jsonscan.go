package meter

import (
	"encoding/json"
	"strings"
)

// metadataJSON walks JSON incrementally. It keeps only allowlisted scalar
// metadata; prompt/output strings and arrays never accumulate in memory.
// Limits apply to metadata and nesting, not to the response's total size.
const maxMetadataScalar = 1024
const maxJSONDepth = 64

type jsonFrame struct {
	kind  byte
	state byte
	path  string
	key   string
}

type metadataJSON struct {
	frames                                               []jsonFrame
	values                                               map[string]json.RawMessage
	started, done, bad                                   bool
	inString, escaped, isKey, literal, capture, overflow bool
	unicodeLeft                                          int
	token                                                []byte
	tokenPath                                            string
}

var metadataContainers = map[string]bool{
	"": true, "usage": true, "message": true, "message.usage": true,
	"delta": true, "response": true, "response.usage": true,
	"choices": true, "choices.*": true,
	"incomplete_details": true, "response.incomplete_details": true,
}

func metadataContainer(path string) bool {
	if metadataContainers[path] {
		return true
	}
	for _, prefix := range []string{"usage.", "message.usage.", "response.usage."} {
		if strings.HasPrefix(path, prefix) {
			key := strings.TrimPrefix(path, prefix)
			return key == "prompt_tokens_details" || key == "input_tokens_details"
		}
	}
	return false
}

func metadataScalar(path string) bool {
	switch path {
	case "model", "type", "status", "stop_reason", "message.model", "response.model", "service_tier", "response.service_tier",
		"response.status", "delta.stop_reason", "choices.*.finish_reason",
		"incomplete_details.reason", "response.incomplete_details.reason":
		return true
	}
	for _, prefix := range []string{"usage.", "message.usage.", "response.usage."} {
		if strings.HasPrefix(path, prefix) {
			switch strings.TrimPrefix(path, prefix) {
			case "input_tokens", "output_tokens", "prompt_tokens", "completion_tokens",
				"cache_creation_input_tokens", "cache_read_input_tokens", "service_tier", "speed",
				"prompt_tokens_details.cached_tokens", "input_tokens_details.cached_tokens",
				"prompt_tokens_details.cache_write_tokens", "input_tokens_details.cache_write_tokens":
				return true
			}
		}
	}
	return false
}

func (s *metadataJSON) childPath() string {
	if len(s.frames) == 0 {
		return ""
	}
	f := &s.frames[len(s.frames)-1]
	if f.path == "!" {
		return "!"
	}
	key := f.key
	if f.kind == '[' {
		key = "*"
	}
	if f.path == "" {
		return key
	}
	return f.path + "." + key
}

func (s *metadataJSON) valueStarted() {
	if len(s.frames) > 0 {
		s.frames[len(s.frames)-1].state = 'c'
	}
}

func (s *metadataJSON) appendToken(b byte) {
	if !s.capture {
		return
	}
	if len(s.token) >= maxMetadataScalar {
		s.overflow = true
		return
	}
	s.token = append(s.token, b)
}

func (s *metadataJSON) saveToken() {
	if !s.capture || s.overflow {
		return
	}
	if s.values == nil {
		s.values = make(map[string]json.RawMessage)
	}
	s.values[s.tokenPath] = append(json.RawMessage(nil), s.token...)
}

func (s *metadataJSON) finishLiteral() {
	s.literal = false
	if s.overflow || !json.Valid(s.token) {
		s.bad = true
		return
	}
	if metadataScalar(s.tokenPath) {
		s.saveToken()
	}
}

func (s *metadataJSON) write(p []byte) {
	for _, b := range p {
		if s.bad {
			return
		}
		if s.literal {
			if !strings.ContainsRune(" \t\r\n,}]", rune(b)) {
				s.appendToken(b)
				continue
			}
			s.finishLiteral()
			if s.bad {
				return
			}
		}
		if s.inString {
			s.appendToken(b)
			if s.unicodeLeft > 0 {
				if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')) {
					s.bad = true
				}
				s.unicodeLeft--
				continue
			}
			if s.escaped {
				s.escaped = false
				if b == 'u' {
					s.unicodeLeft = 4
				} else if !strings.ContainsRune("\"\\/bfnrt", rune(b)) {
					s.bad = true
				}
				continue
			}
			if b == '\\' {
				s.escaped = true
				continue
			}
			if b < 0x20 {
				s.bad = true
				continue
			}
			if b != '"' {
				continue
			}
			s.inString = false
			if s.isKey {
				key := "!"
				if !s.overflow {
					_ = json.Unmarshal(s.token, &key)
				}
				f := &s.frames[len(s.frames)-1]
				f.key, f.state = key, ':'
			} else {
				s.saveToken()
			}
			continue
		}
		if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
			continue
		}
		if s.done {
			s.bad = true
			return
		}
		if !s.started {
			if b != '{' {
				s.bad = true
				return
			}
			s.started = true
			s.frames = append(s.frames, jsonFrame{kind: '{', state: 'e'})
			continue
		}
		f := &s.frames[len(s.frames)-1]
		if (b == '}' && f.kind == '{') || (b == ']' && f.kind == '[') {
			if f.state != 'e' && f.state != 'c' {
				s.bad = true
				continue
			}
			s.frames = s.frames[:len(s.frames)-1]
			if len(s.frames) == 0 {
				s.done = true
			}
			continue
		}
		if f.state == 'c' {
			if b != ',' {
				s.bad = true
				continue
			}
			f.state = 'v'
			continue
		}
		if f.state == ':' {
			if b != ':' {
				s.bad = true
				continue
			}
			f.state = 'x'
			continue
		}
		key := f.kind == '{' && (f.state == 'e' || f.state == 'v')
		if key && b != '"' {
			s.bad = true
			continue
		}
		path := s.childPath()
		if b == '"' {
			s.inString, s.isKey, s.overflow = true, key, false
			s.capture = key || metadataScalar(path)
			s.token, s.tokenPath = s.token[:0], path
			s.appendToken(b)
			if !key {
				s.valueStarted()
			}
			continue
		}
		s.valueStarted()
		if b == '{' || b == '[' {
			if len(s.frames) >= maxJSONDepth {
				s.bad = true
				continue
			}
			if !metadataContainer(path) {
				path = "!"
			}
			if b == '{' && (path == "usage" || strings.HasSuffix(path, ".usage")) {
				if s.values == nil {
					s.values = make(map[string]json.RawMessage)
				}
				s.values[path] = json.RawMessage("{}")
			}
			s.frames = append(s.frames, jsonFrame{kind: b, state: 'e', path: path})
			continue
		}
		s.literal, s.capture, s.overflow = true, true, false
		s.token, s.tokenPath = s.token[:0], path
		s.appendToken(b)
	}
}

func (s *metadataJSON) text(path string, limit int) string {
	var result string
	if json.Unmarshal(s.values[path], &result) != nil || len(result) > limit {
		return ""
	}
	return result
}

func (s *metadataJSON) number(path string) *int {
	var result int
	raw := s.values[path]
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &result) != nil || result < 0 || result > 2147483647 {
		return nil
	}
	return &result
}

func (s *metadataJSON) usage(path string) *usageBlock {
	if len(s.values[path]) == 0 {
		return nil
	}
	block := &usageBlock{
		ServiceTier: s.text(path+".service_tier", 32), Speed: s.text(path+".speed", 16),
		InputTokens: s.number(path + ".input_tokens"), OutputTokens: s.number(path + ".output_tokens"),
		PromptTokens: s.number(path + ".prompt_tokens"), CompletionTokens: s.number(path + ".completion_tokens"),
		CacheCreationInputTokens: s.number(path + ".cache_creation_input_tokens"), CacheReadInputTokens: s.number(path + ".cache_read_input_tokens"),
		PromptTokensDetails: &tokenDetails{CachedTokens: s.number(path + ".prompt_tokens_details.cached_tokens"), CacheWriteTokens: s.number(path + ".prompt_tokens_details.cache_write_tokens")},
		InputTokensDetails:  &tokenDetails{CachedTokens: s.number(path + ".input_tokens_details.cached_tokens"), CacheWriteTokens: s.number(path + ".input_tokens_details.cache_write_tokens")},
	}
	if block.PromptTokensDetails.CachedTokens == nil && block.PromptTokensDetails.CacheWriteTokens == nil {
		block.PromptTokensDetails = nil
	}
	if block.InputTokensDetails.CachedTokens == nil && block.InputTokensDetails.CacheWriteTokens == nil {
		block.InputTokensDetails = nil
	}
	return block
}

func (s *metadataJSON) observe(acc *accumulator) {
	if !s.done || s.bad {
		return
	}
	w := wireUsage{
		ServiceTier: s.text("service_tier", 32),
		Usage:       s.usage("usage"), Model: s.text("model", 128), Status: s.text("status", 64),
		StopReason: s.text("stop_reason", 64), IncompleteReason: s.text("incomplete_details.reason", 64),
		Message:  &usageEnvelope{Usage: s.usage("message.usage"), Model: s.text("message.model", 128)},
		Response: &usageEnvelope{ServiceTier: s.text("response.service_tier", 32), Usage: s.usage("response.usage"), Model: s.text("response.model", 128), Status: s.text("response.status", 64), IncompleteReason: s.text("response.incomplete_details.reason", 64)},
		Delta:    &wireDelta{StopReason: s.text("delta.stop_reason", 64)},
	}
	if reason := s.text("choices.*.finish_reason", 64); reason != "" {
		w.Choices = []wireChoice{{FinishReason: &reason}}
	}
	acc.observe(w)
}
