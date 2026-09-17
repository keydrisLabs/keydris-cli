package proxy

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
	"github.com/keydrisLabs/keydris-cli/internal/node/meter"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

func TestWebSocketUsagePipelineReportsWhileConnectionRemainsOpen(t *testing.T) {
	reports := make(chan runtimecontract.SessionUsageReport, 2)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != runtimecontract.SessionUsagePath || r.Header.Get("Authorization") != "Bearer renewed-kit" {
			t.Error("wrong usage endpoint or session credential")
		}
		var report runtimecontract.SessionUsageReport
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&report); err != nil {
			t.Error(err)
			http.Error(w, "invalid", 400)
			return
		}
		reports <- report
		json.NewEncoder(w).Encode(runtimecontract.SessionUsageReportResponse{SchemaVersion: 1, Accepted: len(report.Events)})
	}))
	defer backend.Close()
	reg := attest.NewSessionRegistry()
	session := attest.Session{Handle: "session-one", SVID: "renewed-kit"}
	reg.Register(session)
	reg.Register(attest.Session{Handle: "other", SVID: "must-not-be-used"})
	m := meter.New(backend.Client(), backend.URL, reg, t.Logf)
	defer m.Close()
	observed := make(chan struct{}, 1)
	info := meter.RequestInfo{Model: "unknown", Source: "codex_chatgpt", Transport: "websocket", Inference: true, Responses: true}
	collector := meter.NewResponsesCollector("openai", info, func(e runtimecontract.SessionUsageEvent) { m.Record(session.Handle, e); observed <- struct{}{} }, func(reason string) { t.Errorf("unexpected observation gap: %s", reason) })
	client, proxyClient := net.Pipe()
	proxyUpstream, upstream := net.Pipe()
	for _, c := range []net.Conn{client, proxyClient, proxyUpstream, upstream} {
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
	}
	payload := []byte(`{"type":"response.completed","response":{"id":"response-one","model":"gpt-test","usage":{"input_tokens":100,"output_tokens":10,"input_tokens_details":{"cached_tokens":40}}}}`)
	frame := []byte{0x81, 126}
	frame = binary.BigEndian.AppendUint16(frame, uint16(len(payload)))
	frame = append(frame, payload...)
	req, _ := http.NewRequest("GET", "https://chatgpt.com/backend-api/codex/responses", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	serverDone := make(chan error, 1)
	go func() {
		_, err := http.ReadRequest(bufio.NewReader(upstream))
		if err != nil {
			serverDone <- err
			return
		}
		_, err = upstream.Write(append([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"), frame...))
		serverDone <- err
	}()
	forwardDone := make(chan error, 1)
	go func() {
		forwardDone <- forwardObserved(proxyClient, req, proxyUpstream, ResponseObservers{WebSocket: collector.WebSocketSink})
	}()
	reader := bufio.NewReader(client)
	resp, err := http.ReadResponse(reader, req)
	if err != nil || resp.StatusCode != 101 {
		t.Fatal(resp, err)
	}
	got := make([]byte, len(frame))
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, frame) {
		t.Fatal("observer changed forwarded bytes")
	}
	<-observed
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-forwardDone:
		t.Fatalf("connection closed prematurely: %v", err)
	default:
	}
	// Flush without closing the socket: the response was recorded before EOF.
	m.FlushSessionFinal(session)
	select {
	case report := <-reports:
		if len(report.Events) != 1 {
			t.Fatalf("%+v", report)
		}
		e := report.Events[0]
		if e.UsageSource != "codex_chatgpt" || e.UsageTransport != "websocket" || e.InputTokens != 60 || e.CacheReadTokens != 40 || e.OutputTokens == nil || *e.OutputTokens != 10 {
			t.Fatalf("%+v", e)
		}
		encoded, _ := json.Marshal(report)
		if strings.Contains(string(encoded), "renewed-kit") || strings.Contains(string(encoded), "session-one") {
			t.Fatal("credentials leaked into report")
		}
	default:
		t.Fatal("usage not delivered")
	}
	client.Close()
	upstream.Close()
	if err := <-forwardDone; err != nil {
		t.Fatal(err)
	}
}

type failingObserver struct{}

func (failingObserver) Write(p []byte) (int, error) { return 0, errors.New("observer failed") }

func TestObservationFailureCannotTruncateForwarding(t *testing.T) {
	r := observedReader{Reader: strings.NewReader("unaltered response"), sink: failingObserver{}}
	got, err := io.ReadAll(r)
	if err != nil || string(got) != "unaltered response" {
		t.Fatal(string(got), err)
	}
}
