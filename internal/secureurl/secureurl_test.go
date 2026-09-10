package secureurl

import (
	"strings"
	"testing"
)

func TestAssert(t *testing.T) {
	t.Setenv(AllowInsecureEnv, "")

	allowed := []string{
		"https://api.keydris.com",
		"https://api.keydris.com:8443/base",
		"http://127.0.0.1:8081",
		"http://localhost:8081",
		"http://[::1]:8081",
		"http://127.0.0.2:9000", // whole 127/8 block is loopback
	}
	for _, raw := range allowed {
		if err := Assert("KEYDRIS_CONTROL_URL", raw); err != nil {
			t.Errorf("Assert(%q) = %v, want nil", raw, err)
		}
	}

	refused := []string{
		"http://control.internal:8081",
		"http://10.0.0.5",
		"http://api.keydris.com",
		"ftp://api.keydris.com",
		"",
		"not a url",
	}
	for _, raw := range refused {
		if err := Assert("KEYDRIS_CONTROL_URL", raw); err == nil {
			t.Errorf("Assert(%q) = nil, want error", raw)
		}
	}
}

func TestAssertNamesTheFixForPlaintext(t *testing.T) {
	t.Setenv(AllowInsecureEnv, "")
	err := Assert("KEYDRIS_CONTROL_URL", "http://control.internal:8081")
	if err == nil || !strings.Contains(err.Error(), AllowInsecureEnv) {
		t.Fatalf("error should name the escape hatch, got %v", err)
	}
}

func TestAssertEscapeHatch(t *testing.T) {
	t.Setenv(AllowInsecureEnv, "1")
	if err := Assert("KEYDRIS_CONTROL_URL", "http://control.internal:8081"); err != nil {
		t.Fatalf("Assert with escape hatch = %v, want nil", err)
	}
	// The hatch never rescues a URL that is not http(s) at all.
	if err := Assert("KEYDRIS_CONTROL_URL", "ftp://x"); err == nil {
		t.Fatal("ftp should stay refused")
	}
}
