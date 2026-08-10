package runtimecontract

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

//go:embed bundle/v1/fixtures/provider-execution-request.json
var canonicalProviderExecutionRequest string

//go:embed bundle/v1/fixtures/provider-execution-response.json
var canonicalProviderExecutionResponse string

//go:embed bundle/v1/schemas/provider-execution-request.schema.json
var canonicalProviderExecutionRequestSchema string

//go:embed bundle/v1/schemas/provider-execution-response.schema.json
var canonicalProviderExecutionResponseSchema string

func TestVendoredProviderExecutionArtifactsChecksums(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     string
	}{
		{
			name:     "request fixture",
			contents: canonicalProviderExecutionRequest,
			want:     "1d39bfc8441deeed3a40ca85014a1e09c9d869928c73136c880c446675001c4d",
		},
		{
			name:     "response fixture",
			contents: canonicalProviderExecutionResponse,
			want:     "e6c58ea9e3cdedd80b4d0f40b072387cff997fdbe278e67ed93b72ee1973dfaa",
		},
		{
			name:     "request schema",
			contents: canonicalProviderExecutionRequestSchema,
			want:     "d1a0d1842227a8cf7dff364c5e9d00349126693d6a328c84167e82dbf3d14454",
		},
		{
			name:     "response schema",
			contents: canonicalProviderExecutionResponseSchema,
			want:     "b5bf9233bbed2ab01e9ff89306337a87b64f1ada57804f97d34f3f48f6d6e977",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := fmt.Sprintf("%x", sha256.Sum256([]byte(test.contents)))
			if got != test.want {
				t.Fatalf("checksum = %s, want %s", got, test.want)
			}
		})
	}
}

func TestExecuteProviderUsesSessionIdentityAndStrictRelativeEndpoint(t *testing.T) {
	var captured ProviderExecutionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runtime/providers/github/execute" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer runtime-session-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		_, _ = fmt.Fprintf(
			w,
			`{"schema_version":1,"request_id":%q,"decision":{"schema_version":1,"decision_id":"Keydris-01K1X4Y5Z6A7B8C9D0E1F2G3H4","request_id":%q,"attempt_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H5","correlation_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H6","decided_at":"2026-07-30T12:00:00Z","decision":"allow","reason_code":"keydris_policy_allowed","obligations":[]},"execution_status":"succeeded","replayed":false,"error_code":null,"provider_response":{"status":201,"headers":{"content-type":"application/json"},"body":{"number":42}}}`,
			captured.RequestID,
			captured.RequestID,
		)
	}))
	defer server.Close()

	input := providerTestRequest()
	result, err := ExecuteProvider(
		context.Background(),
		server.Client(),
		server.URL,
		"runtime-session-token",
		"/v1/runtime/providers/github/execute",
		input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExecutionStatus != "succeeded" ||
		result.ProviderResponse == nil ||
		result.ProviderResponse.Status != http.StatusCreated {
		t.Fatalf("provider result = %+v", result)
	}
	if captured.ConnectionID != input.ConnectionID ||
		captured.ResourceID != input.ResourceID ||
		captured.Request.Path != "/repos/acme/widgets/pulls" {
		t.Fatalf("captured request = %+v", captured)
	}

	_, err = ExecuteProvider(
		context.Background(),
		server.Client(),
		server.URL,
		"runtime-session-token",
		"https://attacker.example/execute",
		input,
	)
	if err == nil || !strings.Contains(err.Error(), "untrusted runtime endpoint") {
		t.Fatalf("absolute endpoint error = %v", err)
	}
}

func TestExecuteProviderRejectsInconsistentOrExtendedResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "denial with provider response",
			body: `{"schema_version":1,"request_id":"provider-001","decision":{"schema_version":1,"decision_id":"Keydris-01K1X4Y5Z6A7B8C9D0E1F2G3H4","request_id":"provider-001","attempt_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H5","correlation_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H6","decided_at":"2026-07-30T12:00:00Z","decision":"deny","reason_code":"keydris_policy_denied","obligations":[]},"execution_status":"denied","replayed":false,"error_code":null,"provider_response":{"status":200,"headers":{},"body":null}}`,
		},
		{
			name: "unknown response field",
			body: `{"schema_version":1,"request_id":"provider-001","decision":{"schema_version":1,"decision_id":"Keydris-01K1X4Y5Z6A7B8C9D0E1F2G3H4","request_id":"provider-001","attempt_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H5","correlation_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H6","decided_at":"2026-07-30T12:00:00Z","decision":"deny","reason_code":"keydris_policy_denied","obligations":[]},"execution_status":"denied","replayed":false,"error_code":null,"provider_response":null,"credentials":"must-not-exist"}`,
		},
		{
			name: "duplicate response identity",
			body: `{"schema_version":1,"request_id":"provider-001","request_id":"provider-002","decision":{"schema_version":1,"decision_id":"Keydris-01K1X4Y5Z6A7B8C9D0E1F2G3H4","request_id":"provider-001","attempt_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H5","correlation_id":"01K1X4Y5Z6A7B8C9D0E1F2G3H6","decided_at":"2026-07-30T12:00:00Z","decision":"deny","reason_code":"keydris_policy_denied","obligations":[]},"execution_status":"denied","replayed":false,"error_code":null,"provider_response":null}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			_, err := ExecuteProvider(
				context.Background(),
				server.Client(),
				server.URL,
				"runtime-session-token",
				"/v1/runtime/providers/github/execute",
				providerTestRequest(),
			)
			if err == nil {
				t.Fatal("invalid provider response was accepted")
			}
		})
	}
}

func providerTestRequest() ProviderExecutionRequest {
	return ProviderExecutionRequest{
		SchemaVersion: SchemaVersion,
		RequestID:     "provider-001",
		ConnectionID:  "77777777-7777-4777-8777-777777777777",
		ResourceID:    "88888888-8888-4888-8888-888888888888",
		Request: ProviderHTTPRequest{
			Method:  http.MethodPost,
			Path:    "/repos/acme/widgets/pulls",
			Query:   map[string][]string{},
			Headers: ProviderRequestHeaders{Accept: "application/json"},
			Body: map[string]any{
				"title": "Governed change",
				"head":  "feature",
				"base":  "main",
			},
		},
	}
}
