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
)

// Metered-origin handling (ENG-261): LLM provider APIs (api.anthropic.com,
// api.openai.com) are MITM'd for METERING ONLY — no policy evaluation, no
// blocking, no header/body mutation beyond forcing an uncompressed response so
// usage is parseable. Every failure degrades to plain forwarding; a metering
// bug must never break an agent's model call.
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

	req, _, err := readRequest(tconn)
	if err != nil {
		return
	}
	if requestTarget, err := requestDestination(req, "https"); err != nil || requestTarget != target {
		writeReject(tconn, "request authority does not match CONNECT target")
		return
	}

	started := time.Now()
	info := meter.ExtractRequest(provider, req)

	var sink meter.ResponseSink
	var tap func(*http.Response) io.Writer
	if info.Inference && p.meter != nil {
		// An uncompressed response keeps the usage tail parseable; the client
		// never sees a difference beyond transfer size.
		req.Header.Set("Accept-Encoding", "identity")
		tap = func(resp *http.Response) io.Writer {
			sink = meter.NewResponseSink(resp)
			if sink == nil {
				return nil
			}
			return sink
		}
	}

	forwardErr := proxy.ForwardTLSOneTapped(tconn, req, target, host, tap)
	if !info.Inference || p.meter == nil {
		return
	}
	var totals meter.UsageTotals
	if sink != nil {
		totals = sink.Totals()
	}
	if forwardErr != nil && !totals.Observed {
		// Nothing reached the model (or nothing came back): no usage to book.
		p.logf("dataplane(sandbox): metered forward %s: %v", target, forwardErr)
		return
	}
	p.meter.Record(sess.Handle, meter.BuildEvent(provider, info, totals, started))
}
