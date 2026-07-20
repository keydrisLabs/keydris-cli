//go:build !linux

package dataplane

import (
	"errors"

	"github.com/keydrisLabs/keydris-cli/internal/node/attest"
	"github.com/keydrisLabs/keydris-cli/internal/proxyscope"
)

// NewTransparent is only implementable on Linux (iptables REDIRECT +
// SO_ORIGINAL_DST). This stub lets the project build on macOS for development;
// use the proxy-env plane for a cross-platform fallback.
func NewTransparent(_ string, _ attest.Resolver, _ *proxyscope.Scope) (DataPlane, error) {
	return nil, errors.New("transparent data plane requires Linux; run inside a VM or use KEYDRIS_DATAPLANE=proxyenv")
}
