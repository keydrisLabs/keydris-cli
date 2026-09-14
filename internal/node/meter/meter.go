package meter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

const (
	// flushInterval bounds how long an observed event sits unshipped.
	flushInterval = 10 * time.Second
	// flushBatchTrigger flushes a session's buffer early once it grows.
	flushBatchTrigger = 25
	// maxBufferedPerSession caps memory per session; beyond it the oldest
	// events are dropped (and logged) rather than growing without bound.
	maxBufferedPerSession = 1000
	shipTimeout           = 5 * time.Second
)

// Meter buffers usage events per session handle and ships them in batches to
// POST /v1/runtime/sessions/usage under the session's current KIT. Events keep
// their request ids across retries, so the server-side dedupe makes every
// retransmission a no-op.
type Meter struct {
	client   *http.Client
	baseURL  string
	registry *attest.SessionRegistry
	logf     func(string, ...any)

	mu      sync.Mutex
	buffers map[string][]runtimecontract.SessionUsageEvent
	dropped int

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// New starts the background flush loop. registry resolves a session handle to
// its CURRENT credential at ship time, so background KIT renewal never strands
// buffered events with a stale token.
func New(
	client *http.Client,
	baseURL string,
	registry *attest.SessionRegistry,
	logf func(string, ...any),
) *Meter {
	m := &Meter{
		client:   client,
		baseURL:  baseURL,
		registry: registry,
		logf:     logf,
		buffers:  map[string][]runtimecontract.SessionUsageEvent{},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go m.loop()
	return m
}

// NewRequestID mints the per-event idempotency key.
func NewRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return "llm-" + hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("llm-%d", time.Now().UnixNano())
}

// Record buffers one event for the session behind handle.
func (m *Meter) Record(handle string, event runtimecontract.SessionUsageEvent) {
	if m == nil || handle == "" {
		return
	}
	m.mu.Lock()
	buffer := m.buffers[handle]
	if len(buffer) >= maxBufferedPerSession {
		buffer = buffer[1:]
		m.dropped++
		if m.dropped == 1 || m.dropped%100 == 0 {
			m.logf("meter: usage buffer full; %d event(s) dropped", m.dropped)
		}
	}
	m.buffers[handle] = append(buffer, event)
	trigger := len(m.buffers[handle]) >= flushBatchTrigger
	m.mu.Unlock()
	if trigger {
		go m.flushHandle(handle, nil)
	}
}

// FlushSessionFinal synchronously ships a departing session's remaining events
// using the given snapshot, BEFORE the hook revokes the KIT. Called from the
// session socket's unregister path, where the registry entry is already gone.
func (m *Meter) FlushSessionFinal(session attest.Session) {
	if m == nil {
		return
	}
	m.flushHandle(session.Handle, &session)
}

// Close ships whatever is still buffered and stops the loop.
func (m *Meter) Close() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() {
		close(m.stop)
		<-m.done
	})
}

func (m *Meter) loop() {
	defer close(m.done)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.flushAll()
		case <-m.stop:
			m.flushAll()
			return
		}
	}
}

func (m *Meter) flushAll() {
	m.mu.Lock()
	handles := make([]string, 0, len(m.buffers))
	for handle := range m.buffers {
		handles = append(handles, handle)
	}
	m.mu.Unlock()
	for _, handle := range handles {
		m.flushHandle(handle, nil)
	}
}

// flushHandle ships one session's buffer in contract-sized batches. On any
// ship failure the remaining events are re-queued for the next tick — the
// per-event request ids make the retry safe.
func (m *Meter) flushHandle(handle string, fallback *attest.Session) {
	m.mu.Lock()
	events := m.buffers[handle]
	delete(m.buffers, handle)
	m.mu.Unlock()
	if len(events) == 0 {
		return
	}

	token := m.resolveToken(handle, fallback)
	if token == "" {
		// No live session and no snapshot: the events can never be shipped.
		m.logf("meter: dropping %d usage event(s) for an unknown session", len(events))
		return
	}

	for start := 0; start < len(events); start += runtimecontract.MaxUsageEventsPerReport {
		end := min(start+runtimecontract.MaxUsageEventsPerReport, len(events))
		if err := m.ship(token, events[start:end]); err != nil {
			m.logf("meter: usage report failed (%d event(s) requeued): %v", len(events)-start, err)
			if fallback == nil {
				m.requeue(handle, events[start:])
			}
			return
		}
	}
}

func (m *Meter) ship(token string, batch []runtimecontract.SessionUsageEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), shipTimeout)
	defer cancel()
	_, err := runtimecontract.ReportSessionUsage(ctx, m.client, m.baseURL, token, batch)
	return err
}

// resolveToken prefers the registry's live session (renewals swap the KIT
// underneath long sessions); the fallback snapshot serves the final flush,
// which runs after the registry entry was taken.
func (m *Meter) resolveToken(handle string, fallback *attest.Session) string {
	if m.registry != nil {
		if session, ok := m.registry.Lookup(handle); ok && session.SVID != "" {
			return session.SVID
		}
	}
	if fallback != nil {
		return fallback.SVID
	}
	return ""
}

func (m *Meter) requeue(handle string, events []runtimecontract.SessionUsageEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	combined := append(events, m.buffers[handle]...)
	if overflow := len(combined) - maxBufferedPerSession; overflow > 0 {
		combined = combined[overflow:]
		m.dropped += overflow
	}
	m.buffers[handle] = combined
}
