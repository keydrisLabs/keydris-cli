package authz

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthorizeSendsToolMetadata(t *testing.T) {
	var got AuthorizeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/authorize" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"allow"}`))
	}))
	defer server.Close()

	_, err := Authorize(context.Background(), server.Client(), server.URL, AuthorizeRequest{
		DstAddr:    "api.example.test:443",
		ToolCall:   "POST /v1/deploy",
		ToolParams: json.RawMessage(`{"repository":"keydris-cli"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ToolCall != "POST /v1/deploy" {
		t.Fatalf("ToolCall = %q", got.ToolCall)
	}
	if string(got.ToolParams) != `{"repository":"keydris-cli"}` {
		t.Fatalf("ToolParams = %s", got.ToolParams)
	}
}

func TestAuthorizeReturnsSanitizedBrokerHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<html>secret ingress response</html>`))
	}))
	defer server.Close()

	_, err := Authorize(context.Background(), server.Client(), server.URL, AuthorizeRequest{DstAddr: "example.test:443"})
	var httpErr *BrokerHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if httpErr.StatusCode != http.StatusForbidden || ErrorKind(err) != "broker_http" {
		t.Fatalf("http error = %+v, kind=%q", httpErr, ErrorKind(err))
	}
	if got := err.Error(); got == "" || strings.Contains(got, "secret ingress response") {
		t.Fatalf("unsuitable sanitized error: %q", got)
	}
}
