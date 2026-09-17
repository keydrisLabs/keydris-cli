package dataplane

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
	"github.com/keydrisLabs/keydris-cli/internal/node/meter"
	"github.com/keydrisLabs/keydris-cli/internal/node/proxy"
	"github.com/keydrisLabs/keydris-cli/internal/runtimecontract"
)

// Metered-origin handling: provider APIs and ChatGPT-backed Codex are
// TLS-terminated for METERING ONLY after managed policy routing — no additional
// policy evaluation, no blocking, no mutation beyond an uncompressed response so
// usage is parseable. Observation failures leave forwarding untouched; normal
// network and TLS failures can still terminate the connection.
//
// Only counters leave the machine: model id, token counts, stop reason,
// latency. Prompt and completion bytes are spliced through and discarded.

// meterConnect terminates TLS for a metered CONNECT target, forwards the
// request unchanged, and records usage for the attributed session.
func (p *sandboxPlane) meterConnect(
	conn net.Conn,
	br *bufio.Reader,
	target, host, provider string,
	sess *attest.Session,
) {
	tconn, ok := p.terminateTLS(conn, br, target, host)
	if !ok {
		return
	}
	defer tconn.Close()

	req, requestReader, err := readRequest(tconn)
	if err != nil {
		return
	}
	if requestTarget, err := requestDestination(req, "https"); err != nil || requestTarget != target {
		writeReject(tconn, "request authority does not match CONNECT target")
		return
	}

	started := time.Now()
	info := meter.ExtractRequestAt(provider, host, req)

	var sink meter.ResponseSink
	var observers proxy.ResponseObservers
	warn := func(reason string) {
		p.logf("meter: observation unavailable provider=%s source=%s transport=%s: %s", provider, info.Source, info.Transport, reason)
	}
	if info.Inference && p.meter != nil {
		// An uncompressed response keeps the usage tail parseable; the client
		// never sees a difference beyond transfer size.
		if info.Transport == "http" {
			req.Header.Set("Accept-Encoding", "identity")
		}
		var collector *meter.ResponsesCollector
		if info.Responses {
			collector = meter.NewResponsesCollector(provider, info, func(event runtimecontract.SessionUsageEvent) { p.meter.Record(sess.Handle, event) }, warn)
			observers.WebSocket = collector.WebSocketSink
		}
		observers.HTTP = func(resp *http.Response) io.Writer {
			if info.Transport == "websocket" {
				warn("WebSocket upgrade was not accepted")
				return nil
			}
			sink = meter.NewResponseSink(resp)
			if collector != nil {
				sink = collector.HTTPSink(resp)
			}
			if sink == nil {
				return nil
			}
			return sink
		}
	}

	forwardErr := proxy.ForwardTLSObserved(tconn, req, target, host, observers, requestReader)
	if !info.Inference || p.meter == nil {
		return
	}
	var totals meter.UsageTotals
	if sink != nil {
		totals = sink.Totals()
	}
	if info.Responses {
		// The per-response observer already recorded usage.
		if forwardErr != nil {
			warn("response forwarding failed")
		}
		return
	}
	if forwardErr != nil && !totals.Observed {
		// Nothing reached the model (or nothing came back): no usage to book.
		p.logf("dataplane(sandbox): metered forward %s: %v", target, forwardErr)
		return
	}
	p.meter.Record(sess.Handle, meter.BuildEvent(provider, info, totals, started))
}
