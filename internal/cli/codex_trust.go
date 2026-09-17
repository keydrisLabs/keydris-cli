package cli

import (
	"bytes"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
)

// Codex is native, not a Node process: NODE_EXTRA_CA_CERTS does not configure
// its HTTPS/WebSocket client. Supply its own CA setting only to the child and
// preserve any pre-existing custom trust. The per-launch file avoids races
// between concurrent sessions with different custom roots.
func codexTrustEnvironment(env []string, bundlePath, dataDir string) ([]string, func(), error) {
	custom := ""
	for _, name := range []string{"CODEX_CA_CERTIFICATE", "SSL_CERT_FILE"} {
		for _, entry := range env {
			key, value, _ := strings.Cut(entry, "=")
			if strings.EqualFold(key, name) {
				custom = value
			}
		}
		if custom != "" {
			break
		}
	}
	combined, err := os.ReadFile(bundlePath)
	if err != nil {
		return nil, nil, fmt.Errorf("read Codex proxy CA bundle: %w", err)
	}
	if !x509.NewCertPool().AppendCertsFromPEM(combined) {
		return nil, nil, fmt.Errorf("Codex proxy CA bundle has no valid certificates")
	}
	if custom != "" && custom != bundlePath {
		additional, err := os.ReadFile(custom)
		if err != nil {
			return nil, nil, fmt.Errorf("read existing Codex CA bundle: %w", err)
		}
		if !x509.NewCertPool().AppendCertsFromPEM(additional) {
			return nil, nil, fmt.Errorf("existing Codex CA bundle has no valid certificates")
		}
		combined = append(append(bytes.TrimSpace(combined), '\n'), additional...)
	}
	f, err := os.CreateTemp(dataDir, "codex-ca-*.pem")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	_, writeErr := f.Write(combined)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		cleanup()
		return nil, nil, fmt.Errorf("write Codex CA bundle: %v, %v", writeErr, closeErr)
	}
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "CODEX_CA_CERTIFICATE") {
			result = append(result, entry)
		}
	}
	return append(result, "CODEX_CA_CERTIFICATE="+f.Name()), cleanup, nil
}
