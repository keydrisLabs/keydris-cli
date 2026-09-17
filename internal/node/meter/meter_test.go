package meter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

const testHandle = "handle-1"

type usageServer struct {
	mu       sync.Mutex
	batches  [][]runtimecontract.SessionUsageEvent
	tokens   []string
	failNext int
}

func (s *usageServer) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path != runtimecontract.SessionUsagePath {
		http.NotFound(w, r)
		return
	}
	if s.failNext > 0 {
		s.failNext--
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	var report runtimecontract.SessionUsageReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.batches = append(s.batches, report.Events)
	s.tokens = append(s.tokens, r.Header.Get("Authorization"))
	_ = json.NewEncoder(w).Encode(runtimecontract.SessionUsageReportResponse{
		SchemaVersion: runtimecontract.SchemaVersion,
		Accepted:      len(report.Events),
	})
}

func (s *usageServer) batchSizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	sizes := make([]int, len(s.batches))
	for i, batch := range s.batches {
		sizes[i] = len(batch)
	}
	return sizes
}

func testEvent(id string) runtimecontract.SessionUsageEvent {
	output := 5
	return runtimecontract.SessionUsageEvent{
		RequestID:    id,
		Provider:     "anthropic",
		Model:        "claude-opus-5",
		InputTokens:  10,
		OutputTokens: &output,
		OccurredAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func newTestMeter(t *testing.T, server *usageServer) (*Meter, *attest.SessionRegistry) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(server.handler))
	t.Cleanup(ts.Close)
	registry := attest.NewSessionRegistry()
	registry.Register(attest.Session{Handle: testHandle, SPIFFEID: "spiffe://test", SVID: "live-kit"})
	m := New(ts.Client(), ts.URL, registry, t.Logf)
	t.Cleanup(m.Close)
	return m, registry
}

func TestFlushShipsWithTheLiveToken(t *testing.T) {
	server := &usageServer{}
	m, _ := newTestMeter(t, server)

	m.Record(testHandle, testEvent("llm-1"))
	m.flushHandle(testHandle, nil)

	if sizes := server.batchSizes(); len(sizes) != 1 || sizes[0] != 1 {
		t.Fatalf("batches: %v", sizes)
	}
	if server.tokens[0] != "Bearer live-kit" {
		t.Fatalf("token: %q — the registry's CURRENT credential must be used", server.tokens[0])
	}
}

func TestMeterRejectsLateEventsAfterSessionFinalFlush(t *testing.T) {
	server := &usageServer{}
	m, registry := newTestMeter(t, server)
	m.Record(testHandle, testEvent("before-exit"))
	departed, ok := registry.Take(testHandle)
	if !ok {
		t.Fatal("session missing")
	}
	m.FlushSessionFinal(departed)
	m.Record(testHandle, testEvent("after-exit"))
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.buffers) != 0 || len(m.ships) != 0 {
		t.Fatal("late event resurrected retired session")
	}
	if got := server.batchSizes(); len(got) != 1 || got[0] != 1 {
		t.Fatal(got)
	}
}

func TestFlushChunksToTheContractLimit(t *testing.T) {
	server := &usageServer{}
	m, _ := newTestMeter(t, server)

	// Buffer directly: Record's early-flush trigger would race the assertion.
	m.mu.Lock()
	for i := 0; i < 150; i++ {
		m.buffers[testHandle] = append(m.buffers[testHandle], testEvent(fmt.Sprintf("llm-%d", i)))
	}
	m.mu.Unlock()
	m.flushHandle(testHandle, nil)

	sizes := server.batchSizes()
	total := 0
	for _, size := range sizes {
		if size > runtimecontract.MaxUsageEventsPerReport {
			t.Fatalf("batch exceeds the contract limit: %v", sizes)
		}
		total += size
	}
	if total != 150 {
		t.Fatalf("shipped %d of 150 events (%v)", total, sizes)
	}
}

func TestShipFailureRequeuesForTheNextTick(t *testing.T) {
	server := &usageServer{failNext: 1}
	m, _ := newTestMeter(t, server)

	m.Record(testHandle, testEvent("llm-1"))
	m.flushHandle(testHandle, nil) // fails, requeues
	m.flushHandle(testHandle, nil) // retries with the same request id

	if sizes := server.batchSizes(); len(sizes) != 1 || sizes[0] != 1 {
		t.Fatalf("batches after retry: %v", sizes)
	}
	server.mu.Lock()
	requestID := server.batches[0][0].RequestID
	server.mu.Unlock()
	if requestID != "llm-1" {
		t.Fatalf("retry must reuse the idempotent request id, got %q", requestID)
	}
}

func TestFinalFlushUsesTheSnapshotAfterUnregister(t *testing.T) {
	server := &usageServer{}
	m, registry := newTestMeter(t, server)

	m.Record(testHandle, testEvent("llm-1"))
	departed, _ := registry.Take(testHandle) // hook unregistered the session
	m.FlushSessionFinal(departed)

	if sizes := server.batchSizes(); len(sizes) != 1 {
		t.Fatalf("final flush did not ship: %v", sizes)
	}
	if server.tokens[0] != "Bearer live-kit" {
		t.Fatalf("token: %q", server.tokens[0])
	}
}

func TestEventsForUnknownSessionsAreDropped(t *testing.T) {
	server := &usageServer{}
	m, registry := newTestMeter(t, server)
	registry.Unregister(testHandle)

	m.Record("unknown-handle", testEvent("llm-1"))
	m.flushHandle("unknown-handle", nil)

	if sizes := server.batchSizes(); len(sizes) != 0 {
		t.Fatalf("events without a session credential must be dropped, shipped %v", sizes)
	}
}

// TestCloseFlushesBufferedEvents covers the daemon shutdown path: buffered
// counters are shipped before Close returns, then the session state is gone.
func TestCloseFlushesBufferedEvents(t *testing.T) {
	server := &usageServer{}
	m, _ := newTestMeter(t, server)

	m.Record(testHandle, testEvent("llm-close"))
	m.Close()

	if sizes := server.batchSizes(); len(sizes) != 1 || sizes[0] != 1 {
		t.Fatalf("Close did not flush buffered events: %v", sizes)
	}
	m.mu.Lock()
	remaining := len(m.buffers[testHandle])
	m.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("events left buffered after Close: %d", remaining)
	}
}

// TestNilMeterIsInert pins the nil-receiver checks the daemon relies on when
// metering is disabled: it still passes (nil *Meter).FlushSessionFinal to the
// renewal loop, and that call must not panic.
func TestNilMeterIsInert(t *testing.T) {
	var m *Meter
	m.Record(testHandle, testEvent("llm-1"))
	m.FlushSessionFinal(attest.Session{Handle: testHandle, SVID: "kit"})
	m.Close()
}
