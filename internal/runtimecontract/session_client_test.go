package runtimecontract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/keydrisLabs/keydris-cli/internal/secureurl"
)

func TestCreateAndRevokeKitSessionReplacement(t *testing.T) {
	const oldSessionID = "01K1X4Y5Z6A7B8C9D0E1F2G3H5"
	var created, revoked bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/runtime/sessions":
			created = true
			if request.Header.Get("Idempotency-Key") != "renew-test-001" {
				t.Errorf("idempotency key = %q", request.Header.Get("Idempotency-Key"))
			}
			if request.Header.Get("X-Keydris-Replaces-Kit") != oldSessionID {
				t.Errorf("replacement header = %q", request.Header.Get("X-Keydris-Replaces-Kit"))
			}
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["agent_id"] != "agent-1" || body["session_handle"] != "handle-1" || body["agent_runtime"] != "claude_code" {
				t.Errorf("request body = %v", body)
			}
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprint(writer, canonicalKITSession)
		case "/runtime/sessions/01K1X4Y5Z6A7B8C9D0E1F2G3H4/revoke":
			revoked = true
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	session, err := CreateKitSession(context.Background(), server.Client(), server.URL, CreateKitSessionInput{
		AgentID:           "agent-1",
		SessionHandle:     "handle-1",
		AgentRuntime:      "claude_code",
		IdempotencyKey:    "renew-test-001",
		ReplacesSessionID: oldSessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created || session.SessionID != "01K1X4Y5Z6A7B8C9D0E1F2G3H4" {
		t.Fatalf("session was not created: %+v", session)
	}
	if err := RevokeKitSession(context.Background(), server.Client(), server.URL, session.SessionID); err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("session was not revoked")
	}
}

// AgentRuntime is optional: a renewal that does not know the coding tool must
// omit the key so the control plane keeps the replaced session's value.
func TestCreateKitSessionAgentRuntimeIsOptional(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runtime string
		present bool
	}{
		{name: "known runtime", runtime: "codex", present: true},
		{name: "unknown tool", runtime: "", present: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				writer.Header().Set("Content-Type", "application/json")
				fmt.Fprint(writer, canonicalKITSession)
			}))
			defer server.Close()

			if _, err := CreateKitSession(context.Background(), server.Client(), server.URL, CreateKitSessionInput{
				AgentID:        "agent-1",
				SessionHandle:  "handle-1",
				AgentRuntime:   tc.runtime,
				IdempotencyKey: "optional-runtime-1",
			}); err != nil {
				t.Fatal(err)
			}
			got, present := body["agent_runtime"]
			if present != tc.present || (tc.present && got != tc.runtime) {
				t.Errorf("agent_runtime = %q (present=%v), want %q (present=%v)", got, present, tc.runtime, tc.present)
			}
		})
	}
}

// Runtime and session calls carry bearer credentials; plaintext is allowed only
// to loopback, on top of the pre-existing http(s)-only check.
func TestRuntimeSessionURLRequiresSecureTransport(t *testing.T) {
	t.Setenv(secureurl.AllowInsecureEnv, "")

	for _, tc := range []struct{ base, path, want string }{
		{"https://api.keydris.test/base", "/runtime/sessions", "https://api.keydris.test/runtime/sessions"},
		{"http://127.0.0.1:8081", "/runtime/sessions", "http://127.0.0.1:8081/runtime/sessions"},
		{"http://localhost:8081", "/runtime/sessions/x/revoke", "http://localhost:8081/runtime/sessions/x/revoke"},
		{"http://[::1]:8081", "/runtime/sessions", "http://[::1]:8081/runtime/sessions"},
	} {
		got, err := runtimeSessionURL(tc.base, tc.path)
		if err != nil {
			t.Errorf("runtimeSessionURL(%q) = %v", tc.base, err)
		} else if got != tc.want {
			t.Errorf("runtimeSessionURL(%q) = %q, want %q", tc.base, got, tc.want)
		}
	}

	for _, base := range []string{
		"http://control.internal:8081",
		"http://10.0.0.5",
		"ftp://api.keydris.test",
		"",
		"https://",
	} {
		if _, err := runtimeSessionURL(base, "/runtime/sessions"); err == nil {
			t.Errorf("runtimeSessionURL(%q) accepted a token-bearing URL", base)
		}
	}
}

func TestTrustedRuntimeURLRequiresSecureTransport(t *testing.T) {
	t.Setenv(secureurl.AllowInsecureEnv, "")

	for _, tc := range []struct{ base, want string }{
		{"https://api.keydris.test", "https://api.keydris.test/v1/runtime/routes"},
		{"http://127.0.0.1:8081/base", "http://127.0.0.1:8081/v1/runtime/routes"},
	} {
		got, err := trustedRuntimeURL(tc.base, "/v1/runtime/routes")
		if err != nil {
			t.Errorf("trustedRuntimeURL(%q) = %v", tc.base, err)
		} else if got != tc.want {
			t.Errorf("trustedRuntimeURL(%q) = %q, want %q", tc.base, got, tc.want)
		}
	}

	for _, base := range []string{"http://control.internal:8081", "http://10.0.0.5", "ftp://api.keydris.test"} {
		if _, err := trustedRuntimeURL(base, "/v1/runtime/routes"); err == nil {
			t.Errorf("trustedRuntimeURL(%q) accepted a token-bearing URL", base)
		}
	}
	if _, err := trustedRuntimeURL("https://api.keydris.test", "https://evil.test/v1/runtime/routes"); err == nil {
		t.Error("trustedRuntimeURL accepted an absolute endpoint path")
	}
}
