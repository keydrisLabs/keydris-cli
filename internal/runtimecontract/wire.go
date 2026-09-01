package runtimecontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// decodeStrict decodes raw JSON into output, rejecting unknown fields and
// trailing data. Callers that accept attacker-influenced bytes must reject
// duplicate keys first (rejectDuplicateJSONKeys, session.go).
func decodeStrict(raw []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func readBounded(reader io.Reader, limit int64, label string) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%s exceeds the v1 size limit", label)
	}
	return raw, nil
}

// validRuntimePath accepts only server-relative /v1/runtime/ endpoint paths,
// keeping the daemon from being steered to an arbitrary origin.
func validRuntimePath(value string) bool {
	if !strings.HasPrefix(value, "/v1/runtime/") ||
		strings.Contains(value, "://") || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.IsAbs() == false && parsed.Host == ""
}

// trustedRuntimeURL joins a validated runtime endpoint path onto the
// configured Keydris base URL, never trusting the path to carry its own
// scheme, host, or fragment.
func trustedRuntimeURL(baseURL, endpointPath string) (string, error) {
	if !validRuntimePath(endpointPath) {
		return "", fmt.Errorf("untrusted runtime endpoint path %q", endpointPath)
	}
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" ||
		(base.Scheme != "https" && base.Scheme != "http") {
		return "", fmt.Errorf("invalid Keydris runtime URL")
	}
	endpoint, err := url.Parse(endpointPath)
	if err != nil || endpoint.IsAbs() || endpoint.Host != "" {
		return "", fmt.Errorf("invalid runtime endpoint path")
	}
	base.Path = endpoint.Path
	base.RawPath = endpoint.RawPath
	base.RawQuery = endpoint.RawQuery
	base.Fragment = ""
	return base.String(), nil
}
