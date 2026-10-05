package cli

import (
	"runtime"
	"slices"
	"testing"
)

func TestAppendProxyEnvironmentPointsCodexAtTheKeydrisCA(t *testing.T) {
	env := appendProxyEnvironment(nil, "http://keydris:handle@127.0.0.1:15001", "/keydris/ca-bundle.crt")

	for _, want := range []string{
		"HTTPS_PROXY=http://keydris:handle@127.0.0.1:15001",
		"NODE_EXTRA_CA_CERTS=/keydris/ca-bundle.crt",
		// Codex adds this bundle to the native roots. On Windows it is the only
		// way Codex trusts the Keydris CA without `init --trust-store`.
		"CODEX_CA_CERTIFICATE=/keydris/ca-bundle.crt",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("environment is missing %q: %v", want, env)
		}
	}

	replacesRoots := slices.Contains(env, "SSL_CERT_FILE=/keydris/ca-bundle.crt")
	if runtime.GOOS == "windows" && replacesRoots {
		t.Errorf("SSL_CERT_FILE must not replace the Windows root store: %v", env)
	}
	if runtime.GOOS != "windows" && !replacesRoots {
		t.Errorf("SSL_CERT_FILE is missing: %v", env)
	}
}
