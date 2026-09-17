package meter

import (
	"net/http"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

const maxTrackedResponses = 128

// ResponsesCollector owns logical response metadata, not the connection or
// credentials. Each terminal response is emitted immediately so a persistent
// WebSocket never delays reporting until disconnect. Only its single reader
// calls the collector; Record handles concurrent sessions and shipping separately.
type ResponsesCollector struct {
	provider string
	info     RequestInfo
	emit     func(runtimecontract.SessionUsageEvent)
	warn     func(string)
	started  map[string]time.Time
	seen     map[string]bool
	order    []string
	warned   bool
}

func NewResponsesCollector(provider string, info RequestInfo, emit func(runtimecontract.SessionUsageEvent), warn func(string)) *ResponsesCollector {
	return &ResponsesCollector{provider: provider, info: info, emit: emit, warn: warn,
		started: make(map[string]time.Time), seen: make(map[string]bool)}
}

func (c *ResponsesCollector) gap(reason string) {
	if !c.warned && c.warn != nil {
		c.warn(reason)
		c.warned = true
	}
}

func (c *ResponsesCollector) observe(p metadataJSON) {
	if !p.done || p.bad {
		c.gap("invalid response metadata")
		return
	}
	id := p.text("response.id", 128)
	if id == "" {
		id = p.text("id", 128)
	}
	typ := p.text("type", 64)
	if typ == "response.created" {
		if id != "" && len(c.started) < maxTrackedResponses {
			if _, exists := c.started[id]; !exists {
				c.started[id] = time.Now()
			}
		}
		return
	}
	terminal := typ == "response.completed" || typ == "response.incomplete" || typ == "response.failed" || typ == "response.cancelled"
	if typ == "" {
		s := p.text("status", 64)
		terminal = s == "completed" || s == "incomplete" || s == "failed" || s == "cancelled"
	}
	if !terminal {
		return
	}
	if id == "" {
		c.gap("terminal response has no identity")
		return
	}
	if c.seen[id] {
		return
	}
	var acc accumulator
	p.observe(&acc)
	start, hasStart := c.started[id]
	delete(c.started, id)
	if !hasStart {
		start = time.Now()
	}
	event := BuildEvent(c.provider, c.info, acc.totals, start)
	if !hasStart {
		event.LatencyMS = 0
	}
	event.RequestID = responseRequestID(c.provider, c.info.Source, id)
	if event.OutputTokens == nil {
		c.gap("terminal response has missing or invalid usage")
	}
	c.seen[id] = true
	c.order = append(c.order, id)
	if len(c.order) > maxTrackedResponses {
		delete(c.seen, c.order[0])
		c.order = c.order[1:]
	}
	if c.emit != nil {
		c.emit(event)
	}
}

// HTTP and WebSocket share the same metadata-only collector. Existing
// Anthropic and Chat Completions sinks keep their cumulative semantics.
func (c *ResponsesCollector) HTTPSink(resp *http.Response) ResponseSink {
	sink := NewResponseSink(resp)
	switch s := sink.(type) {
	case *sseSink:
		s.observe = c.observe
	case *jsonSink:
		s.observe = c.observe
	default:
		c.gap("unsupported response content type")
	}
	return sink
}
