// Package meter implements LLM usage metering (ENG-261): the sandbox proxy
// terminates TLS on metered origins, reads the model id off the request and
// the token usage off the response, and ships counters to the control plane
// under the session's KIT.
//
// The meter-only guarantee: nothing content-shaped is retained or transmitted —
// only model ids, token counts, stop reasons, and latencies. Metered origins
// are never policy-enforced and every failure degrades to plain forwarding.
package meter

import (
	"strings"
)

// builtinOrigins maps LLM provider API hosts to their provider identifier. The
// list is deliberately small: an origin is metered only when its responses are
// parseable for usage.
var builtinOrigins = map[string]string{
	"api.anthropic.com": "anthropic",
	"api.openai.com":    "openai",
}

// Origins matches CONNECT targets against the metered set.
type Origins struct {
	byHost map[string]string
}

// NewOrigins builds the matcher from the built-in provider hosts plus optional
// overrides ("host=provider" entries, e.g. from KEYDRIS_METERED_ORIGINS). An
// override of "off" for a built-in host removes it.
func NewOrigins(overrides []string) *Origins {
	byHost := make(map[string]string, len(builtinOrigins)+len(overrides))
	for host, provider := range builtinOrigins {
		byHost[host] = provider
	}
	for _, entry := range overrides {
		host, provider, ok := strings.Cut(strings.TrimSpace(entry), "=")
		host = strings.ToLower(strings.TrimSpace(host))
		provider = strings.TrimSpace(provider)
		if !ok || host == "" {
			continue
		}
		if provider == "off" {
			delete(byHost, host)
			continue
		}
		byHost[host] = provider
	}
	return &Origins{byHost: byHost}
}

// Provider returns the provider identifier for a metered origin. Metering is
// TLS-only (the LLM APIs are HTTPS), so anything but port 443 does not match.
func (o *Origins) Provider(host string, port int) (string, bool) {
	if o == nil || port != 443 {
		return "", false
	}
	provider, ok := o.byHost[strings.ToLower(host)]
	return provider, ok
}
