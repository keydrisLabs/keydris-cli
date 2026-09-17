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
	flushInterval         = 10 * time.Second
	flushBatchTrigger     = 25
	maxBufferedPerSession = 1000
	shipTimeout           = 5 * time.Second
	finalFlushTimeout     = 15 * time.Second
)

type sessionShip struct {
	// sendMu serializes network operations without blocking Record.
	sendMu sync.Mutex
	// The remaining fields are protected by Meter.mu.
	scheduled bool
	closing   bool
	cancel    context.CancelFunc
}

type Meter struct {
	client   *http.Client
	baseURL  string
	registry *attest.SessionRegistry
	logf     func(string, ...any)
	mu       sync.Mutex
	buffers  map[string][]runtimecontract.SessionUsageEvent
	ships    map[string]*sessionShip
	dropped  int
	closed   bool
	workers  sync.WaitGroup
	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

func New(client *http.Client, baseURL string, registry *attest.SessionRegistry, logf func(string, ...any)) *Meter {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	m := &Meter{client: client, baseURL: baseURL, registry: registry, logf: logf, buffers: map[string][]runtimecontract.SessionUsageEvent{}, ships: map[string]*sessionShip{}, stop: make(chan struct{}), done: make(chan struct{})}
	go m.loop()
	return m
}

func NewRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return "llm-" + hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("llm-%d", time.Now().UnixNano())
}

func (m *Meter) state(handle string) *sessionShip {
	s := m.ships[handle]
	if s == nil {
		s = &sessionShip{}
		m.ships[handle] = s
	}
	return s
}

func (m *Meter) Record(handle string, event runtimecontract.SessionUsageEvent) {
	if m == nil || handle == "" {
		return
	}
	// Coordinate registry validation with the final flush. Otherwise a Record
	// paused before this lock could recreate a buffer after revocation.
	m.mu.Lock()
	if m.registry != nil {
		if _, ok := m.registry.Lookup(handle); !ok {
			m.mu.Unlock()
			return
		}
	}
	if m.closed {
		m.mu.Unlock()
		return
	}
	s := m.state(handle)
	if s.closing {
		m.mu.Unlock()
		return
	}
	buffer := m.buffers[handle]
	if len(buffer) >= maxBufferedPerSession {
		buffer = buffer[1:]
		m.dropped++
		if m.dropped == 1 || m.dropped%100 == 0 {
			m.logf("meter: usage buffer full; %d event(s) dropped", m.dropped)
		}
	}
	m.buffers[handle] = append(buffer, event)
	if len(buffer) == 0 {
		m.logf("meter: usage observed provider=%s source=%s transport=%s", event.Provider, event.UsageSource, event.UsageTransport)
	}
	if len(m.buffers[handle]) >= flushBatchTrigger && !s.scheduled {
		s.scheduled = true
		m.workers.Add(1)
		go func() {
			defer m.workers.Done()
			m.flushHandle(handle, nil)
			m.mu.Lock()
			s.scheduled = false
			m.mu.Unlock()
		}()
	}
	m.mu.Unlock()
}

// Final flush stops new shipping, cancels and joins any in-flight attempt,
// then retries its returned events with the departing credential snapshot.
// Its total network budget fits inside the session socket's unregister deadline.
func (m *Meter) FlushSessionFinal(session attest.Session) {
	if m == nil {
		return
	}
	m.mu.Lock()
	s := m.state(session.Handle)
	s.closing = true
	if s.cancel != nil {
		s.cancel()
	}
	m.mu.Unlock()
	m.flushHandle(session.Handle, &session)
	m.mu.Lock()
	delete(m.buffers, session.Handle)
	delete(m.ships, session.Handle)
	m.mu.Unlock()
}

func (m *Meter) Close() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()
		close(m.stop)
		<-m.done
		m.workers.Wait()
		m.flushAll()
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

func (m *Meter) flushHandle(handle string, fallback *attest.Session) {
	m.mu.Lock()
	s := m.state(handle)
	m.mu.Unlock()
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	m.mu.Lock()
	if s.closing && fallback == nil {
		m.mu.Unlock()
		return
	}
	events := m.buffers[handle]
	delete(m.buffers, handle)
	budget := shipTimeout
	if fallback != nil {
		budget = finalFlushTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	s.cancel = cancel
	m.mu.Unlock()
	defer func() { cancel(); m.mu.Lock(); s.cancel = nil; m.mu.Unlock() }()
	if len(events) == 0 {
		return
	}
	for start := 0; start < len(events); start += runtimecontract.MaxUsageEventsPerReport {
		end := min(start+runtimecontract.MaxUsageEventsPerReport, len(events))
		// Resolve for every batch, so renewal during a large flush takes effect.
		token := m.resolveToken(handle, fallback)
		if token == "" {
			if fallback == nil {
				// Take may remove the registry entry between two batches.
				// Leave the unsent tail for the final flush's credential snapshot.
				m.requeue(handle, events[start:])
				return
			}
			m.logf("meter: dropping %d event(s) without a session credential", len(events)-start)
			return
		}
		err := m.ship(ctx, token, events[start:end])
		if err != nil && fallback != nil && ctx.Err() == nil {
			err = m.ship(ctx, token, events[start:end])
		}
		if err != nil {
			if fallback == nil {
				m.requeue(handle, events[start:])
				m.logf("meter: usage report failed; %d event(s) queued for retry: %v", len(events)-start, err)
			} else {
				m.logf("meter: final usage report failed; %d event(s) could not be delivered before revocation: %v", len(events)-start, err)
			}
			return
		}
	}
}

func (m *Meter) ship(ctx context.Context, token string, batch []runtimecontract.SessionUsageEvent) error {
	result, err := runtimecontract.ReportSessionUsage(ctx, m.client, m.baseURL, token, batch)
	if err == nil {
		m.logf("meter: report accepted=%d duplicates=%d", result.Accepted, result.Duplicates)
	}
	return err
}

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
